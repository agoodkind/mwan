package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

const (
	sitLinkType = "sit"
	// The netlink library always sends the path MTU discovery attribute.
	// The kernel clears the outer Don't Fragment bit when the attribute is zero.
	sitPathMTUDiscovery = 1
	// The netlink library always sends the protocol attribute.
	// A zero protocol makes the device also accept IPv4 packets inside IPv4 packets.
	sitInnerProtocol = unix.IPPROTO_IPV6
)

// errTunnelEndpointsHeld reports a tunnel deletion caused by another owned tunnel using the deleted tunnel's
// configured endpoints.
var errTunnelEndpointsHeld = errors.New("another owned tunnel has the configured endpoints")

func tunnelKernelType(protocol interfaceintent.TunnelProtocol) string {
	if protocol == interfaceintent.TunnelProtocol6in4 {
		return sitLinkType
	}
	return ""
}

func (record virtualRecord) kernelType() string {
	if record.Kind == string(interfaceintent.KindTunnel) {
		return tunnelKernelType(interfaceintent.TunnelProtocol(record.TunnelProtocol))
	}
	return record.Kind
}

func isOwnedVirtualType(linkType string) bool {
	return linkType == "vlan" || linkType == "bridge" || linkType == sitLinkType
}

// effectiveTunnelTTL returns the hop limit for every create and update request because the kernel applies
// its own default when the netlink library omits a zero hop limit.
func effectiveTunnelTTL(tunnel *interfaceintent.Tunnel) uint8 {
	if tunnel.TTL == nil {
		return interfaceintent.DefaultTunnelTTL
	}
	return *tunnel.TTL
}

// tunnelMTU returns the configured MTU, or the underlay MTU less the outer header when no MTU is configured.
func tunnelMTU(connection interfaceintent.Connection, underlay netlink.Link) (int, error) {
	limit := underlay.Attrs().MTU - interfaceintent.Tunnel6in4Overhead
	if connection.Link.MTU == nil {
		return max(limit, interfaceintent.IPv6MinimumMTU), nil
	}
	configured := int(*connection.Link.MTU)
	if configured > limit {
		return 0, fmt.Errorf("tunnel %s MTU %d exceeds underlay %s MTU %d less the %d-byte outer header",
			connection.Name, configured, underlay.Attrs().Name, underlay.Attrs().MTU, interfaceintent.Tunnel6in4Overhead)
	}
	return configured, nil
}

func validateTunnelLink(link netlink.Link, connection interfaceintent.Connection) error {
	tunnel := connection.Link.Tunnel
	if tunnel == nil {
		return fmt.Errorf("connection %s has no tunnel intent", connection.ID)
	}
	kernelType := tunnelKernelType(tunnel.Protocol)
	if kernelType == "" {
		return fmt.Errorf("connection %s has unsupported tunnel protocol %q", connection.ID, tunnel.Protocol)
	}
	if link.Type() != kernelType {
		return fmt.Errorf("%s is not a %s device", link.Attrs().Name, kernelType)
	}
	return nil
}

func (r *OwnedLinkReconciler) tunnelUnderlay(ctx context.Context, connection interfaceintent.Connection, byName map[string]interfaceintent.Connection, resolved map[string]OwnedLinkResult, visiting map[string]bool, result *OwnedLinkResult) netlink.Link {
	tunnel := connection.Link.Tunnel
	if tunnel == nil || tunnelKernelType(tunnel.Protocol) == "" || !tunnel.Remote.Is4() ||
		tunnel.Local.IsValid() && !tunnel.Local.Is4() {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf("connection %s has invalid tunnel intent", connection.ID)
		return nil
	}
	result.Dependency = tunnel.Underlay
	underlay, dependency := r.dependency(ctx, result.Dependency, byName, resolved, visiting)
	if underlay != nil {
		return underlay
	}
	result.Operation = "wait-for-underlay"
	result.Err = fmt.Errorf("tunnel underlay %s is not present", result.Dependency)
	if dependency.Err != nil {
		result.Err = fmt.Errorf("tunnel underlay %s is not present: %w", result.Dependency, dependency.Err)
	}
	if dependency.Status == OwnedLinkFailed {
		result.Status, result.Operation = OwnedLinkFailed, "resolve-underlay"
	}
	return nil
}

func (r *OwnedLinkReconciler) requiredLinks(ctx context.Context, connection interfaceintent.Connection, byName map[string]interfaceintent.Connection, resolved map[string]OwnedLinkResult, visiting map[string]bool, result *OwnedLinkResult) (netlink.Link, netlink.Link, bool) {
	if connection.Link.Kind == interfaceintent.KindVLAN {
		parent := r.vlanParent(ctx, connection, byName, resolved, visiting, result)
		return parent, nil, parent != nil
	}
	if connection.Link.Kind == interfaceintent.KindTunnel {
		underlay := r.tunnelUnderlay(ctx, connection, byName, resolved, visiting, result)
		return nil, underlay, underlay != nil
	}
	return nil, nil, true
}

func requestedVirtualLink(connection interfaceintent.Connection, name string, parent netlink.Link, underlay netlink.Link) (netlink.Link, error) {
	if connection.Link.Kind == interfaceintent.KindTunnel {
		return requestedTunnel(name, 0, connection.Link.Tunnel, underlay)
	}
	attrs := netlink.LinkAttrs{Name: name}
	if connection.Link.Kind == interfaceintent.KindVLAN {
		attrs.ParentIndex = parent.Attrs().Index
		return &netlink.Vlan{LinkAttrs: attrs, VlanId: int(connection.Link.VLAN.ID), VlanProtocol: netlink.VLAN_PROTOCOL_8021Q}, nil
	}
	return &netlink.Bridge{LinkAttrs: attrs}, nil
}

func requestedTunnel(name string, index int, tunnel *interfaceintent.Tunnel, underlay netlink.Link) (*netlink.Sittun, error) {
	underlayIndex := underlay.Attrs().Index
	if underlayIndex <= 0 || uint64(underlayIndex) > math.MaxUint32 {
		return nil, fmt.Errorf("tunnel underlay %s has invalid index %d", underlay.Attrs().Name, underlayIndex)
	}
	// NewLinkAttrs disables the transmit queue length attribute because a zero LinkAttrs value sets that
	// attribute to zero.
	attrs := netlink.NewLinkAttrs()
	attrs.Name, attrs.Index = name, index
	requested := &netlink.Sittun{
		LinkAttrs: attrs, Link: uint32(underlayIndex),
		Ttl: effectiveTunnelTTL(tunnel), PMtuDisc: sitPathMTUDiscovery, Proto: sitInnerProtocol,
		Remote: net.IP(tunnel.Remote.AsSlice()),
	}
	if tunnel.Local.IsValid() {
		requested.Local = net.IP(tunnel.Local.AsSlice())
	}
	return requested, nil
}

func (r *OwnedLinkReconciler) convergedVirtualLink(connection interfaceintent.Connection, parent netlink.Link, underlay netlink.Link) (netlink.Link, bool, error) {
	if connection.Link.Kind != interfaceintent.KindTunnel {
		return r.virtualLink(connection, parent, underlay)
	}
	// The MTU check prevents device creation and updates when the MTU exceeds the underlay limit.
	// The MTU check runs before every kernel change.
	mtu, err := tunnelMTU(connection, underlay)
	if err != nil {
		return nil, false, err
	}
	link, created, err := r.virtualLink(connection, parent, underlay)
	if err != nil || link == nil {
		return link, created, err
	}
	converged, replaced, err := r.convergeTunnel(connection, link, underlay)
	if err != nil {
		return nil, false, err
	}
	if converged.Attrs().MTU == mtu {
		return converged, created || replaced, nil
	}
	if err := netlink.LinkSetMTU(converged, mtu); err != nil {
		slog.Warn("owned tunnel MTU update failed", "link", converged.Attrs().Name, "mtu", mtu, "err", err)
		return nil, false, fmt.Errorf("set MTU on tunnel %s: %w", converged.Attrs().Name, err)
	}
	converged.Attrs().MTU = mtu
	return converged, created || replaced, nil
}

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
