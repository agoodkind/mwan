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
	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

func (module *Module) currentDHCPv6Client(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection) {
	id := connection.ID.String()
	delegation := (*interfaceintent.Delegation)(nil)
	if connection.IPv6 != nil && connection.IPv6.DHCP != nil && *connection.IPv6.DHCP {
		delegation = connection.IPv6.Delegation
	}
	ready, ok := module.Env.OwnedLinks.Get(id)
	identity := ""
	if delegation != nil && ok && ready.Status == netif.OwnedLinkReady && ready.ConnectionID == id &&
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
	clientContext, cancel := context.WithCancel(ctx)
	client, err := netif.StartDHCPv6PDClient(clientContext, log, clientConfig)
	if err != nil {
		cancel()
		module.Env.Delegations.Delete(connection.Name)
		module.sessionMu.Unlock()
		log.ErrorContext(ctx, "addresses: DHCPv6 client failed", "connection_id", id, "err", err)
		return
	}
	module.nextGeneration++
	session := &dhcpv6Session{client: client, cancel: cancel, ready: ready, identity: identity, generation: module.nextGeneration}
	module.dhcpv6Sessions[id] = session
	module.Env.Delegations.Delete(connection.Name)
	module.sessionMu.Unlock()
	module.startDHCPv6Watcher(clientContext, log, id, connection.Name, session)
}

func (module *Module) startDHCPv6Watcher(ctx context.Context, log *slog.Logger, id, iface string, session *dhcpv6Session) {
	go func() {
		defer func() {
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
		module.watchDHCPv6(ctx, id, session)
	}()
}

func dhcpv6PDConfig(connection interfaceintent.Connection, iface string) (netif.DHCPv6PDConfig, error) {
	delegation := connection.IPv6.Delegation
	duid := delegation.DUID
	iaid := delegation.IAID
	withoutRA := delegation.WithoutRA
	if client := connection.IPv6.DHCPv6; client != nil {
		if client.WithoutRA != "" {
			withoutRA = client.WithoutRA
		}
		if client.DUID != "" {
			duid = client.DUID
		}
		if client.IAPDIAID != nil {
			iaid = client.IAPDIAID
		}
	}
	if iaid == nil {
		return netif.DHCPv6PDConfig{}, errors.New("prefix IAID is required")
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(duid, ":", ""))
	if err != nil {
		slog.Warn("addresses: invalid configured DHCPv6 DUID", "connection_id", connection.ID, "err", err)
		return netif.DHCPv6PDConfig{}, fmt.Errorf("decode DUID: %w", err)
	}
	return netif.DHCPv6PDConfig{
		Iface:          iface,
		DUID:           decoded,
		IAID:           *iaid,
		IANAIAID:       0,
		RequestAddress: false,
		RequestPrefix:  true,
		Hint:           delegation.Hint,
		Clock:          clock.Real{},
		WaitForRA:      withoutRA == "no",
	}, nil
}

func (module *Module) watchDHCPv6(ctx context.Context, id string, session *dhcpv6Session) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-session.client.Events:
			if !ok {
				return
			}
			module.sessionMu.Lock()
			current := module.dhcpv6Sessions[id]
			valid := current == session && current.generation == session.generation
			module.sessionMu.Unlock()
			if valid && module.Env.RequestReconcile != nil {
				module.Env.RequestReconcile("dhcpv6 delegation changed")
			}
		}
	}
}

func (module *Module) publishDHCPv6Lease(iface string, lease netif.DHCPv6PDLease) {
	if len(lease.Prefixes) == 0 {
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

func (module *Module) familyDelegations(connection interfaceintent.Connection, family string) []interfaceintent.Assignment {
	if family != "ipv6" {
		return nil
	}
	return module.delegationAssignments(connection)
}
