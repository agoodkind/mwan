package netif

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

const vxlanLinkType = "vxlan"

func validateVXLANIntent(connection interfaceintent.Connection) error {
	settings := connection.Link.Tunnel.VXLAN
	if settings == nil || settings.VNI > interfaceintent.MaximumVXLANVNI || settings.SourcePort == 0 || settings.DestinationPort == 0 {
		return fmt.Errorf("connection %s requires a 24-bit VXLAN VNI and nonzero UDP ports", connection.ID)
	}
	if connection.Link.BridgeMaster != "" {
		return fmt.Errorf("connection %s requires a routed VXLAN interface without a bridge", connection.ID)
	}
	if connection.Link.MTU != nil && *connection.Link.MTU < interfaceintent.IPv6MinimumMTU {
		return fmt.Errorf("connection %s VXLAN MTU is below %d", connection.ID, interfaceintent.IPv6MinimumMTU)
	}
	return nil
}

func requestedVXLAN(name string, index int, tunnel *interfaceintent.Tunnel, underlay netlink.Link) (*netlink.Vxlan, error) {
	underlayIndex := underlay.Attrs().Index
	if underlayIndex <= 0 || uint64(underlayIndex) > math.MaxUint32 {
		return nil, fmt.Errorf("VXLAN underlay %s has invalid index %d", underlay.Attrs().Name, underlayIndex)
	}
	settings := tunnel.VXLAN
	if settings == nil {
		return nil, fmt.Errorf("VXLAN tunnel %s has no envelope settings", name)
	}
	attributes := netlink.NewLinkAttrs()
	attributes.Name, attributes.Index = name, index
	requested := &netlink.Vxlan{
		LinkAttrs: attributes, VxlanId: int(settings.VNI), VtepDevIndex: underlayIndex,
		SrcAddr: nil, Group: net.IP(tunnel.Remote.AsSlice()), TTL: int(effectiveTunnelTTL(tunnel)), TOS: int(settings.TOS),
		Learning: false, Proxy: false, RSC: false, L2miss: false, L3miss: false,
		UDPCSum: true, UDP6ZeroCSumTx: false, UDP6ZeroCSumRx: false,
		NoAge: false, GBP: false, FlowBased: false, Age: 0, Limit: 0,
		Port: int(settings.DestinationPort), PortLow: int(settings.SourcePort), PortHigh: int(settings.SourcePort),
	}
	if tunnel.Local.IsValid() {
		requested.SrcAddr = net.IP(tunnel.Local.AsSlice())
	}
	return requested, nil
}

func matchesVXLANSettings(observed *netlink.Vxlan, settings *interfaceintent.VXLAN, parent int, local, remote netip.Addr, ttl uint8) bool {
	if settings == nil {
		return false
	}
	return observed.VxlanId == int(settings.VNI) && observed.VtepDevIndex == parent && observed.MasterIndex == 0 &&
		tunnelEndpoint(observed.SrcAddr) == local && tunnelEndpoint(observed.Group) == remote &&
		observed.Port == int(settings.DestinationPort) && observed.PortLow == int(settings.SourcePort) && observed.PortHigh == int(settings.SourcePort) &&
		observed.TTL == int(ttl) && observed.TOS == int(settings.TOS) &&
		!observed.Learning && !observed.Proxy && !observed.RSC && !observed.L2miss && !observed.L3miss &&
		observed.UDPCSum && !observed.UDP6ZeroCSumTx && !observed.UDP6ZeroCSumRx && !observed.GBP && !observed.FlowBased
}

func matchesVXLANRecord(link netlink.Link, record virtualRecord) bool {
	observed, ok := link.(*netlink.Vxlan)
	if !ok {
		return false
	}
	local, localErr := netip.ParseAddr(record.TunnelLocal)
	remote, remoteErr := netip.ParseAddr(record.TunnelRemote)
	return localErr == nil && remoteErr == nil && matchesVXLANSettings(observed, record.VXLAN, record.ParentIndex, local, remote, record.TunnelTTL)
}

func (r *OwnedLinkReconciler) matchesVXLANUnderlay(record virtualRecord) bool {
	underlay, err := netlink.LinkByIndex(record.ParentIndex)
	return err == nil && r.matchesParentRecord(record, underlay)
}

func (r *OwnedLinkReconciler) vxlanReceiverHolder(requested *netlink.Vxlan, links []netlink.Link) (string, bool) {
	for _, link := range links {
		observed, ok := link.(*netlink.Vxlan)
		if !ok || observed.Index == requested.Index || observed.VxlanId != requested.VxlanId || observed.Port != requested.Port ||
			observed.FlowBased || observed.GBP || observed.UDP6ZeroCSumRx ||
			observed.Group.To4() == nil && observed.Group != nil || observed.SrcAddr.To4() == nil && observed.SrcAddr != nil {
			continue
		}
		_, record, recorded := r.recordForAlias(observed.Alias)
		return observed.Name, recorded && r.matchesRecordedVirtual(link, record)
	}
	return "", false
}

func (r *OwnedLinkReconciler) validateVXLANReceiverAvailability(connection interfaceintent.Connection, found netlink.Link, underlay netlink.Link, links []netlink.Link) error {
	if connection.Link.Tunnel == nil || connection.Link.Tunnel.Protocol != interfaceintent.TunnelProtocolVXLAN {
		return nil
	}
	index := 0
	if found != nil {
		index = found.Attrs().Index
	}
	requested, err := requestedVXLAN(connection.Name, index, connection.Link.Tunnel, underlay)
	if err != nil {
		return err
	}
	if holder, owned := r.vxlanReceiverHolder(requested, links); holder != "" && (found == nil || !owned) {
		slog.Warn("VXLAN receiver is occupied", "link", connection.Name, "holder", holder, "owned", owned)
		return fmt.Errorf("VXLAN receiver VNI %d UDP port %d is occupied by %s: %w", requested.VxlanId, requested.Port, holder, unix.EEXIST)
	}
	return nil
}

func (r *OwnedLinkReconciler) convergeVXLAN(connection interfaceintent.Connection, link netlink.Link, underlay netlink.Link) (netlink.Link, bool, error) {
	tunnel := connection.Link.Tunnel
	observed, ok := link.(*netlink.Vxlan)
	if !ok {
		return nil, false, fmt.Errorf("%s is not a VXLAN device", link.Attrs().Name)
	}
	local := netip.IPv4Unspecified()
	if tunnel.Local.IsValid() {
		local = tunnel.Local
	}
	if matchesVXLANSettings(observed, tunnel.VXLAN, underlay.Attrs().Index, local, tunnel.Remote, effectiveTunnelTTL(tunnel)) {
		return link, false, nil
	}
	requested, err := requestedVXLAN(link.Attrs().Name, link.Attrs().Index, tunnel, underlay)
	if err != nil {
		return nil, false, err
	}
	links, err := netlink.LinkList()
	if err != nil {
		slog.Warn("VXLAN receiver inspection failed", "link", connection.Name, "err", err)
		return nil, false, fmt.Errorf("inspect VXLAN receiver conflicts: %w", err)
	}
	if holder, owned := r.vxlanReceiverHolder(requested, links); holder != "" {
		slog.Warn("VXLAN receiver reconfiguration conflicts", "link", connection.Name, "holder", holder, "owned", owned)
		if !owned {
			return nil, false, fmt.Errorf("VXLAN receiver VNI %d UDP port %d is occupied by unowned %s: %w", requested.VxlanId, requested.Port, holder, unix.EEXIST)
		}
		if err := r.deleteTunnel(connection, link); err != nil {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%w: %s; the next pass recreates %s", errTunnelEndpointsHeld, holder, connection.Name)
	}
	return r.replaceTunnel(connection, link, underlay, fmt.Errorf("VXLAN envelope or underlay changed"))
}
