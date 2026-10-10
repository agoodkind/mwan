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

func tunnelMTU(connection interfaceintent.Connection, underlay netlink.Link) (int, error) {
	limit := underlay.Attrs().MTU - interfaceintent.Tunnel6in4Overhead
	if limit < interfaceintent.IPv6MinimumMTU {
		return 0, fmt.Errorf("tunnel %s underlay %s MTU %d is below the required minimum %d",
			connection.Name, underlay.Attrs().Name, underlay.Attrs().MTU,
			interfaceintent.IPv6MinimumMTU+interfaceintent.Tunnel6in4Overhead)
	}
	if connection.Link.MTU == nil {
		return limit, nil
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

func recordTunnelIdentity(record *virtualRecord, tunnel *interfaceintent.Tunnel, underlay netlink.Link) {
	if tunnel == nil || underlay == nil {
		return
	}
	local := netip.IPv4Unspecified()
	if tunnel.Local.IsValid() {
		local = tunnel.Local
	}
	record.TunnelProtocol, record.ParentIndex = string(tunnel.Protocol), underlay.Attrs().Index
	record.TunnelRemote, record.TunnelLocal = tunnel.Remote.String(), local.String()
}

// validateTemporaryTunnel accepts a device with the reserved temporary name only when the underlay and
// endpoints match the reservation. The link manager creates the temporary device with those values and
// tags the device after creation.
func validateTemporaryTunnel(link netlink.Link, connection interfaceintent.Connection, record virtualRecord) error {
	if err := validateTunnelLink(link, connection); err != nil {
		return err
	}
	observed, ok := link.(*netlink.Sittun)
	if !ok || observed.ParentIndex != record.ParentIndex ||
		tunnelEndpoint(observed.Remote).String() != record.TunnelRemote ||
		tunnelEndpoint(observed.Local).String() != record.TunnelLocal {
		return fmt.Errorf("quarantined temporary link %s: wrong tunnel identity", link.Attrs().Name)
	}
	return nil
}
