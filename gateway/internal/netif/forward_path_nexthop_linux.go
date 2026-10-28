//go:build linux

package netif

import (
	"context"
	"fmt"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// The kernel limits group members to leaf nexthops. A repeated group ID
// invalidates the observation rather than creating a recursive lookup.
func inspectForwardingNexthop(ctx context.Context, id uint32, selected netlink.Route) (bool, bool, error) {
	nexthop, err := queryPacketNexthop(ctx, id)
	if err != nil {
		return false, false, err
	}
	if nexthop.blackhole || nexthop.encapsulated || nexthop.flags&(unix.RTNH_F_DEAD|unix.RTNH_F_LINKDOWN) != 0 {
		return false, false, nil
	}
	if len(nexthop.group) == 0 {
		matches := nexthop.output == selected.LinkIndex && nexthop.gateway.Equal(selected.Gw)
		return false, matches, nil
	}
	if nexthop.groupType != packetNexthopGroupMPath && nexthop.groupType != packetNexthopGroupResilient {
		return false, false, nil
	}
	matched := false
	for _, member := range nexthop.group {
		if member == id {
			return false, false, fmt.Errorf("forwarding nexthop group %d references itself", id)
		}
		leaf, err := queryPacketNexthop(ctx, member)
		if err != nil {
			return false, false, &packetQueryError{operation: packetQueryNexthop, objectID: id, stage: fmt.Sprintf("inspect group member %d", member), cause: err}
		}
		if len(leaf.group) != 0 {
			return false, false, fmt.Errorf("forwarding nexthop group %d contains another group", id)
		}
		if leaf.output != selected.LinkIndex || !leaf.gateway.Equal(selected.Gw) {
			continue
		}
		if leaf.blackhole || leaf.encapsulated || leaf.flags&(unix.RTNH_F_DEAD|unix.RTNH_F_LINKDOWN) != 0 {
			return false, false, nil
		}
		matched = true
	}
	return len(nexthop.group) > 1, matched, nil
}
