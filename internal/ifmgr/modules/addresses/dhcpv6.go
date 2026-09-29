package addresses

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

func (module *Module) currentDHCPv6Client(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection) {
	id := connection.ID.String()
	wantsDHCPv6 := connection.IPv6 != nil && connection.IPv6.DHCP != nil && *connection.IPv6.DHCP &&
		(connection.IPv6.Delegation != nil || connection.IPv6.DHCPv6 != nil)
	ready, ok := module.Env.OwnedLinks.Get(id)
	identity := ""
	if wantsDHCPv6 && ok && ready.Status == netif.OwnedLinkReady && ready.ConnectionID == id &&
		ready.Name == connection.Name && ready.IfIndex != 0 {
		link, err := netlink.LinkByIndex(ready.IfIndex)
		if err == nil && link.Attrs().Name == ready.ActualName {
			attributes := link.Attrs()
			identity = fmt.Sprintf("%s/%s/%s/%d", link.Type(), attributes.HardwareAddr, attributes.Alias, attributes.ParentIndex)
		}
	}
	module.sessionMu.Lock()
	previous := module.dhcpv6Sessions[id]
	if identity == "" {
		if previous != nil {
			previous.cancel()
			delete(module.dhcpv6Sessions, id)
		}
		module.Env.Delegations.Delete(connection.Name)
		module.sessionMu.Unlock()
		return
	}
	if previous != nil && previous.ready.IfIndex == ready.IfIndex &&
		previous.ready.ActualName == ready.ActualName && previous.identity == identity {
		module.publishDHCPv6Lease(connection.Name, previous.client.LastLease())
		module.sessionMu.Unlock()
		return
	}
	if previous != nil {
		previous.cancel()
	}
	clientConfig, err := dhcpv6PDConfig(connection, ready.ActualName)
	if err != nil {
		module.Env.Delegations.Delete(connection.Name)
		module.sessionMu.Unlock()
		log.ErrorContext(ctx, "addresses: DHCPv6 configuration invalid", "connection_id", id, "err", err)
		return
	}
	var addrUpdates chan netlink.AddrUpdate
	var addrErrors chan error
	var addrDone chan struct{}
	if clientConfig.RequestAddress {
		addrUpdates = make(chan netlink.AddrUpdate, 128)
		addrErrors = make(chan error, 1)
		addrDone = make(chan struct{})
		err = netlink.AddrSubscribeWithOptions(addrUpdates, addrDone, netlink.AddrSubscribeOptions{
			ErrorCallback: func(subscriptionErr error) {
				select {
				case addrErrors <- subscriptionErr:
				default:
				}
			},
		})
		if err != nil {
			close(addrDone)
			module.Env.Delegations.Delete(connection.Name)
			module.sessionMu.Unlock()
			log.ErrorContext(ctx, "addresses: DHCPv6 address observer failed", "connection_id", id, "err", err)
			return
		}
	}
	clientContext, cancel := context.WithCancel(ctx)
	client, err := netif.StartDHCPv6PDClient(clientContext, log, clientConfig)
	if err != nil {
		cancel()
		if addrDone != nil {
			close(addrDone)
		}
		module.Env.Delegations.Delete(connection.Name)
		module.sessionMu.Unlock()
		log.ErrorContext(ctx, "addresses: DHCPv6 client failed", "connection_id", id, "err", err)
		return
	}
	module.nextGeneration++
	session := &dhcpv6Session{
		client: client, cancel: cancel, ready: ready, identity: identity, generation: module.nextGeneration,
		addrUpdates: addrUpdates, addrErrors: addrErrors, addrDone: addrDone,
	}
	module.dhcpv6Sessions[id] = session
	module.Env.Delegations.Delete(connection.Name)
	module.sessionMu.Unlock()
	module.startDHCPv6Watcher(clientContext, log, id, connection.Name, session)
}

func (module *Module) startDHCPv6Watcher(ctx context.Context, log *slog.Logger, id, iface string, session *dhcpv6Session) {
	go func() {
		defer func() {
			if session.addrDone != nil {
				close(session.addrDone)
			}
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "addresses: DHCPv6 event watcher panicked", "connection_id", id, "err", recovered)
			}
			if ctx.Err() != nil {
				return
			}
			module.sessionMu.Lock()
			if module.dhcpv6Sessions[id] != session {
				module.sessionMu.Unlock()
				return
			}
			delete(module.dhcpv6Sessions, id)
			session.cancel()
			module.Env.Delegations.Delete(iface)
			module.sessionMu.Unlock()
			if module.Env.RequestReconcile != nil {
				module.Env.RequestReconcile("dhcpv6 client stopped")
			}
		}()
		module.watchDHCPv6(ctx, id, iface, session)
	}()
}

func dhcpv6PDConfig(connection interfaceintent.Connection, iface string) (netif.DHCPv6PDConfig, error) {
	delegation := connection.IPv6.Delegation
	duid := ""
	var iaid *uint32
	withoutRA := ""
	requestPrefix := delegation != nil
	hint := netip.Prefix{}
	if delegation != nil {
		duid = delegation.DUID
		iaid = delegation.IAID
		withoutRA = delegation.WithoutRA
		hint = delegation.Hint
	}
	requestAddress := false
	client := connection.IPv6.DHCPv6
	if client == nil {
		client = new(interfaceintent.DHCPv6)
	}
	if client.WithoutRA != "" {
		withoutRA = client.WithoutRA
	}
	if client.DUID != "" {
		duid = client.DUID
	}
	if client.IAPDIAID != nil {
		iaid = client.IAPDIAID
	}
	ianaIAID := client.IANAIAID
	if client.RequestAddress != nil {
		requestAddress = *client.RequestAddress
	}
	if client.RequestPrefix != nil {
		requestPrefix = *client.RequestPrefix
	}
	if !requestAddress && !requestPrefix {
		return netif.DHCPv6PDConfig{}, errors.New("DHCPv6 requires an address or prefix request")
	}
	if requestPrefix && iaid == nil {
		return netif.DHCPv6PDConfig{}, errors.New("prefix IAID is required")
	}
	if requestAddress && ianaIAID == nil {
		return netif.DHCPv6PDConfig{}, errors.New("address IAID is required")
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(duid, ":", ""))
	if err != nil {
		slog.Warn("addresses: invalid configured DHCPv6 DUID", "connection_id", connection.ID, "err", err)
		return netif.DHCPv6PDConfig{}, fmt.Errorf("decode DUID: %w", err)
	}
	config := netif.DHCPv6PDConfig{
		Iface: iface, DUID: decoded, IAID: 0, IANAIAID: 0,
		RequestAddress: requestAddress, RequestPrefix: requestPrefix,
		Hint: hint, Clock: clock.Real{}, WaitForRA: withoutRA == "no", CachedLease: nil,
	}
	if iaid != nil {
		config.IAID = *iaid
	}
	if ianaIAID != nil {
		config.IANAIAID = *ianaIAID
	}
	return config, nil
}

func (module *Module) watchDHCPv6(ctx context.Context, id, iface string, session *dhcpv6Session) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-session.addrErrors:
			module.Log.WarnContext(ctx, "addresses: DHCPv6 address observer stopped", "connection_id", id, "err", err)
			return
		case update, ok := <-session.addrUpdates:
			if !ok {
				return
			}
			module.declineFailedDHCPv6Address(ctx, id, session, update)
		case _, ok := <-session.client.Events:
			if !ok {
				return
			}
			module.sessionMu.Lock()
			current := module.dhcpv6Sessions[id]
			valid := current == session && current.generation == session.generation
			if valid {
				module.publishDHCPv6Lease(iface, session.client.LastLease())
			}
			module.sessionMu.Unlock()
			if valid && module.Env.RequestReconcile != nil {
				module.Env.RequestReconcile("dhcpv6 assignment changed")
			}
		}
	}
}

func (module *Module) declineFailedDHCPv6Address(ctx context.Context, id string, session *dhcpv6Session, update netlink.AddrUpdate) {
	if update.LinkIndex != session.ready.IfIndex || update.Flags&unix.IFA_F_DADFAILED == 0 {
		return
	}
	prefix, err := netip.ParsePrefix(update.LinkAddress.String())
	if err != nil || !prefix.IsValid() || prefix.Bits() != 128 {
		return
	}
	lease := session.client.LastLease()
	for _, assigned := range lease.Addresses {
		if assigned.Address != prefix.Addr() || !module.clock.Now().Before(assigned.ValidUntil) {
			continue
		}
		if module.queueDHCPv6Decline(id, session, assigned.Address) {
			module.Log.WarnContext(ctx, "addresses: DHCPv6 address failed duplicate address detection", "connection_id", id, "address", assigned.Address)
		}
		return
	}
}

func (module *Module) publishDHCPv6Lease(iface string, lease netif.DHCPv6PDLease) {
	if len(lease.Prefixes) == 0 && len(lease.Addresses) == 0 {
		module.Env.Delegations.Delete(iface)
		return
	}
	module.Env.Delegations.Set(iface, lease)
}

func (module *Module) delegation(iface string) netip.Prefix {
	lease, ok := module.Env.Delegations.Get(iface)
	if !ok {
		return netip.Prefix{}
	}
	now := module.clock.Now()
	for _, value := range lease.Prefixes {
		if now.Before(value.ValidUntil) {
			return value.Prefix
		}
	}
	return netip.Prefix{}
}

func (module *Module) delegationAssignments(connection interfaceintent.Connection) []interfaceintent.Assignment {
	lease, ok := module.Env.Delegations.Get(connection.Name)
	if !ok {
		return nil
	}
	now := module.clock.Now()
	assignments := make([]interfaceintent.Assignment, 0, len(lease.Prefixes))
	for _, prefix := range lease.Prefixes {
		if !now.Before(prefix.ValidUntil) {
			continue
		}
		iaid := lease.IAID
		assignment := interfaceintent.Assignment{
			ConnectionID: connection.ID, Family: "ipv6", Kind: interfaceintent.AssignmentDHCPv6IAPD,
			Source: "dhcpv6", Purpose: "", Value: prefix.Prefix, Route: nil, ClientID: "", DUID: net.HardwareAddr(lease.DUID).String(),
			IAID: &iaid, AcquiredAt: lease.AcquiredAt, RenewAt: &lease.RenewAt,
			RebindAt: &lease.RebindAt, PreferredUntil: &prefix.PreferredUntil,
			ValidUntil: &prefix.ValidUntil, Valid: true,
		}
		assignments = append(assignments, assignment)
	}
	return assignments
}

func (module *Module) localDHCPv6Assignments(connection interfaceintent.Connection) ([]interfaceintent.Address, []interfaceintent.Assignment) {
	lease, ok := module.Env.Delegations.Get(connection.Name)
	if !ok {
		return nil, nil
	}
	now := module.clock.Now()
	addresses := make([]interfaceintent.Address, 0, len(lease.Addresses))
	assignments := make([]interfaceintent.Assignment, 0, len(lease.Addresses))
	for _, leased := range lease.Addresses {
		if !now.Before(leased.ValidUntil) {
			continue
		}
		prefix := netip.PrefixFrom(leased.Address, 128)
		addresses = append(addresses, interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeLocal})
		iaid := lease.IANAIAID
		assignments = append(assignments, interfaceintent.Assignment{
			ConnectionID: connection.ID, Family: "ipv6", Kind: interfaceintent.AssignmentDHCPv6IANA,
			Source: "dhcpv6", Purpose: interfaceintent.PurposeLocal, Value: prefix, Route: nil, ClientID: "",
			DUID: net.HardwareAddr(lease.DUID).String(), IAID: &iaid,
			AcquiredAt: lease.AcquiredAt, RenewAt: &lease.IANARenewAt,
			RebindAt: &lease.IANARebindAt, PreferredUntil: &leased.PreferredUntil,
			ValidUntil: &leased.ValidUntil, Valid: true,
		})
	}
	return addresses, assignments
}

func (module *Module) dhcpv6State(connection interfaceintent.Connection) (string, string, bool) {
	if connection.IPv6 == nil || connection.IPv6.DHCP == nil || !*connection.IPv6.DHCP {
		return "static", "valid", true
	}
	wantsAddress := false
	wantsPrefix := connection.IPv6.Delegation != nil
	if client := connection.IPv6.DHCPv6; client != nil {
		if client.RequestAddress != nil {
			wantsAddress = *client.RequestAddress
		}
		if client.RequestPrefix != nil {
			wantsPrefix = *client.RequestPrefix
		}
	}
	lease, present := module.Env.Delegations.Get(connection.Name)
	if !present {
		return "acquiring", "pending", false
	}
	now := module.clock.Now()
	hasAddress := false
	for _, address := range lease.Addresses {
		if now.Before(address.ValidUntil) {
			hasAddress = true
			break
		}
	}
	hasPrefix := false
	for _, prefix := range lease.Prefixes {
		if now.Before(prefix.ValidUntil) {
			hasPrefix = true
			break
		}
	}
	if !hasAddress && !hasPrefix {
		return "expired", "expired", false
	}
	if wantsAddress && !hasAddress || wantsPrefix && !hasPrefix {
		return "partial", "partial", true
	}
	return "bound", "valid", true
}

func (module *Module) familyDelegations(connection interfaceintent.Connection, family string) []interfaceintent.Assignment {
	if family != "ipv6" {
		return nil
	}
	return module.delegationAssignments(connection)
}
