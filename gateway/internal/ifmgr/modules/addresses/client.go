package addresses

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

func dhcpv4LinkIdentity(
	connection interfaceintent.Connection,
	ready netif.OwnedLinkResult,
	connectionID string,
	enabled bool,
) string {
	identity := ""
	if enabled && ready.Status == netif.OwnedLinkReady && ready.ConnectionID == connectionID &&
		ready.Name == connection.Name && ready.IfIndex != 0 {
		link, err := netlink.LinkByIndex(ready.IfIndex)
		if err == nil && link.Attrs().Name == ready.ActualName {
			attributes := link.Attrs()
			identity = fmt.Sprintf(
				"%s/%s/%s/%d",
				link.Type(),
				attributes.HardwareAddr,
				attributes.Alias,
				attributes.ParentIndex,
			)
		}
	}

	return identity
}

func dhcpv4LeaseOptions(connection interfaceintent.Connection) (string, bool) {
	clientID := ""
	useRoutes := true
	if connection.IPv4.DHCPv4 != nil {
		clientID = connection.IPv4.DHCPv4.ClientID
		if connection.IPv4.DHCPv4.UseRoutes != nil {
			useRoutes = *connection.IPv4.DHCPv4.UseRoutes
		}
	}

	return clientID, useRoutes
}

type dhcpSession struct {
	client           *netif.DHCPClient
	cancel           context.CancelFunc
	ready            netif.OwnedLinkResult
	identity         string
	generation       uint64
	recoveryPending  bool
	recoveryUntil    time.Time
	recoveryRejected bool
}

type dhcpv6Session struct {
	client           *netif.DHCPv6PDClient
	cancel           context.CancelFunc
	ready            netif.OwnedLinkResult
	identity         string
	generation       uint64
	recoveryPending  bool
	recoveryUntil    time.Time
	cachedLease      *netif.DHCPv6PDLease
	recoveryRejected bool
	addrUpdates      chan netlink.AddrUpdate
	addrErrors       chan error
	addrDone         chan struct{}
}

func (module *Module) resolveDHCPv4Recovery(id string, session *dhcpSession) bool {
	module.sessionMu.Lock()
	defer module.sessionMu.Unlock()
	current := module.sessions[id]
	if current != session || current.generation != session.generation || !session.recoveryPending {
		return false
	}

	session.recoveryPending = false

	return true
}

func (module *Module) recordLeasePersistence(id, family, result, reason string) {
	if module.Env.LiveState != nil {
		module.Env.LiveState.SetLeasePersistence(id, family, result, reason)
	}
}

func (module *Module) requestLeaseReconcile(reason string) {
	if module.Env.RequestReconcile != nil {
		module.Env.RequestReconcile(reason)
	}
}

func (module *Module) queueDHCPv6Decline(
	id string,
	expected *dhcpv6Session,
	address netip.Addr,
) bool {
	module.sessionMu.Lock()
	session := module.dhcpv6Sessions[id]
	module.sessionMu.Unlock()
	if session == nil || expected != nil && session != expected {
		return false
	}

	return session.client.DeclineAddress(address)
}

func (module *Module) startDHCPv4Watcher(
	clientContext context.Context,
	log *slog.Logger,
	connectionID string,
	session *dhcpSession,
) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(
					clientContext,
					"addresses: DHCP event watcher panicked",
					"connection_id",
					connectionID,
					"err",
					recovered,
				)
			}
		}()
		module.watchDHCP(clientContext, connectionID, session)
	}()
}
