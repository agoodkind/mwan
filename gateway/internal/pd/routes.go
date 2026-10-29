package pd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func (s *DefaultSource) prefixFromKernelRoutes(
	ctx context.Context,
	iface string,
) (netip.Prefix, bool, error) {
	log := s.log
	if log == nil {
		log = slog.Default()
	}

	link, err := netlink.LinkByName(iface)
	if err != nil {
		log.WarnContext(ctx, "pd: netlink LinkByName failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("LinkByName(%s): %w", iface, err)
	}
	filter := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Protocol:  unix.RTPROT_DHCP,
	}
	mask := netlink.RT_FILTER_OIF | netlink.RT_FILTER_PROTOCOL
	routes, err := netlink.RouteListFiltered(unix.AF_INET6, filter, mask)
	if err != nil {
		log.WarnContext(ctx, "pd: netlink RouteListFiltered failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("RouteListFiltered(%s): %w", iface, err)
	}

	for _, route := range routes {
		prefix, ok := prefixFromIPNet(route.Dst)
		if ok {
			return prefix, true, nil
		}
	}
	return netip.Prefix{}, false, nil
}

func prefixFromIPNet(ipNet *net.IPNet) (netip.Prefix, bool) {
	if ipNet == nil {
		return netip.Prefix{}, false
	}
	addrBytes := ipNet.IP.To16()
	if addrBytes == nil || ipNet.IP.To4() != nil {
		return netip.Prefix{}, false
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 128 || ones < 0 {
		return netip.Prefix{}, false
	}
	var addr16 [16]byte
	copy(addr16[:], addrBytes)
	return netip.PrefixFrom(netip.AddrFrom16(addr16), ones).Masked(), true
}
