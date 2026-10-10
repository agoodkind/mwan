package netif

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

func tunnelEndpoint(address net.IP) netip.Addr {
	parsed, ok := netip.AddrFromSlice(address.To4())
	if !ok {
		return netip.IPv4Unspecified()
	}
	return parsed
}

func tunnelNeedsUpdate(observed *netlink.Sittun, tunnel *interfaceintent.Tunnel, underlay netlink.Link) bool {
	local := netip.IPv4Unspecified()
	if tunnel.Local.IsValid() {
		local = tunnel.Local
	}
	return observed.Attrs().ParentIndex != underlay.Attrs().Index ||
		tunnelEndpoint(observed.Remote) != tunnel.Remote || tunnelEndpoint(observed.Local) != local ||
		observed.Ttl != effectiveTunnelTTL(tunnel) || observed.PMtuDisc != sitPathMTUDiscovery
}

func (r *OwnedLinkReconciler) convergeTunnel(connection interfaceintent.Connection, link netlink.Link, underlay netlink.Link) (netlink.Link, bool, error) {
	id := connection.ID.String()
	tunnel := connection.Link.Tunnel
	observed, ok := link.(*netlink.Sittun)
	if !ok {
		return nil, false, fmt.Errorf("%s is not a %s device", link.Attrs().Name, sitLinkType)
	}
	if observed.Proto != sitInnerProtocol {
		// The kernel does not change the inner protocol of an existing sit device.
		mismatch := fmt.Errorf("tunnel %s has inner protocol %d, want %d", link.Attrs().Name, observed.Proto, sitInnerProtocol)
		return r.replaceTunnel(connection, link, underlay, mismatch)
	}
	if !tunnelNeedsUpdate(observed, tunnel, underlay) {
		return link, false, nil
	}
	requested, err := requestedTunnel(link.Attrs().Name, link.Attrs().Index, tunnel, underlay)
	if err != nil {
		return nil, false, err
	}
	if err := modifyTunnel(requested); err != nil {
		return r.resolveTunnelUpdateError(connection, link, underlay, requested, err)
	}
	updated, err := netlink.LinkByIndex(link.Attrs().Index)
	if err != nil {
		slog.Warn("owned tunnel refresh failed", "connection_id", id, "link", link.Attrs().Name, "err", err)
		return nil, false, fmt.Errorf("refresh updated tunnel %s: %w", link.Attrs().Name, err)
	}
	return updated, false, nil
}

// resolveTunnelUpdateError deletes the device only when the kernel cannot update the device in place
// (EOPNOTSUPP) or another owned tunnel causes EEXIST during an endpoint exchange between two owned tunnels.
// The kernel rejects both endpoint updates while both devices exist.
func (r *OwnedLinkReconciler) resolveTunnelUpdateError(connection interfaceintent.Connection, link netlink.Link, underlay netlink.Link, requested *netlink.Sittun, updateErr error) (netlink.Link, bool, error) {
	if errors.Is(updateErr, unix.EOPNOTSUPP) {
		return r.replaceTunnel(connection, link, underlay, updateErr)
	}
	if !errors.Is(updateErr, unix.EEXIST) {
		return nil, false, updateErr
	}
	holder, owned, err := r.tunnelEndpointHolder(requested)
	if err != nil {
		return nil, false, errors.Join(updateErr, err)
	}
	if !owned {
		return nil, false, updateErr
	}
	slog.Warn("owned tunnel deletion for an endpoint exchange", "connection_id", connection.ID.String(),
		"link", link.Attrs().Name, "holder", holder)
	if err := r.deleteTunnel(connection, link); err != nil {
		return nil, false, errors.Join(updateErr, err)
	}
	return nil, false, fmt.Errorf("%w: %s; the next pass recreates %s", errTunnelEndpointsHeld, holder, connection.Name)
}

// tunnelEndpointHolder identifies the other device with the requested underlay and endpoints.
// tunnelEndpointHolder reports whether the journal records that device.
func (r *OwnedLinkReconciler) tunnelEndpointHolder(requested *netlink.Sittun) (string, bool, error) {
	links, err := netlink.LinkList()
	if err != nil {
		slog.Warn("tunnel endpoint holder listing failed", "link", requested.Name, "err", err)
		return "", false, fmt.Errorf("list tunnel endpoint holders: %w", err)
	}
	for _, link := range links {
		candidate, ok := link.(*netlink.Sittun)
		if !ok || candidate.Index == requested.Index || candidate.ParentIndex != int(requested.Link) ||
			tunnelEndpoint(candidate.Remote) != tunnelEndpoint(requested.Remote) ||
			tunnelEndpoint(candidate.Local) != tunnelEndpoint(requested.Local) {
			continue
		}
		_, record, recorded := r.recordForAlias(candidate.Alias)
		return candidate.Name, recorded && r.matchesRecordedVirtual(link, record), nil
	}
	return "", false, nil
}

func (r *OwnedLinkReconciler) deleteTunnel(connection interfaceintent.Connection, link netlink.Link) error {
	id := connection.ID.String()
	if err := netlink.LinkDel(link); err != nil {
		slog.Warn("owned tunnel deletion failed", "connection_id", id, "link", link.Attrs().Name, "err", err)
		return fmt.Errorf("delete owned tunnel %s: %w", link.Attrs().Name, err)
	}
	delete(r.state.Virtuals, id)
	return r.save()
}

func (r *OwnedLinkReconciler) replaceTunnel(connection interfaceintent.Connection, link netlink.Link, underlay netlink.Link, updateErr error) (netlink.Link, bool, error) {
	slog.Warn("owned tunnel replacement", "connection_id", connection.ID.String(),
		"link", link.Attrs().Name, "err", updateErr)
	if err := r.deleteTunnel(connection, link); err != nil {
		return nil, false, errors.Join(updateErr, err)
	}
	replacement, _, err := r.virtualLink(connection, nil, underlay)
	if err != nil {
		return nil, false, errors.Join(updateErr, err)
	}
	return replacement, true, nil
}
