//go:build linux

package netif

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/agoodkind/netlink"
	"golang.org/x/sys/unix"
)

// The kernel limits group members to leaf nexthops. A repeated group ID
// invalidates the observation rather than creating a recursive lookup.
func inspectForwardingNexthop(ctx context.Context, log *slog.Logger, handle *netlink.Handle, id uint32, selected netlink.Route) (bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return false, false, fmt.Errorf("start forwarding nexthop inspection: %w", err)
	}
	nexthop, err := handle.NexthopGet(id)
	if err != nil {
		log.WarnContext(ctx, "forwarding nexthop read failed", "nexthop_id", id, "err", err)
		return false, false, fmt.Errorf("inspect forwarding nexthop %d: %w", id, err)
	}
	if nexthop.Blackhole || nexthop.EncapType != 0 || nexthop.Flags&(unix.RTNH_F_DEAD|unix.RTNH_F_LINKDOWN) != 0 {
		return false, false, nil
	}
	if len(nexthop.Group) == 0 {
		matches := int(nexthop.OIF) == selected.LinkIndex && nexthop.Gateway.Equal(selected.Gw)
		return false, matches, nil
	}
	if nexthop.GroupType != netlink.NEXTHOP_GRP_TYPE_MPATH && nexthop.GroupType != netlink.NEXTHOP_GRP_TYPE_RES {
		return false, false, nil
	}
	matched := false
	for _, member := range nexthop.Group {
		if err := ctx.Err(); err != nil {
			return false, false, fmt.Errorf("inspect forwarding nexthop group members: %w", err)
		}
		if member.ID == id {
			return false, false, fmt.Errorf("forwarding nexthop group %d references itself", id)
		}
		leaf, err := handle.NexthopGet(member.ID)
		if err != nil {
			log.WarnContext(ctx, "forwarding nexthop group member read failed", "group_id", id, "member_id", member.ID, "err", err)
			return false, false, fmt.Errorf("inspect forwarding nexthop group %d member %d: %w", id, member.ID, err)
		}
		if len(leaf.Group) != 0 {
			return false, false, fmt.Errorf("forwarding nexthop group %d contains another group", id)
		}
		if int(leaf.OIF) != selected.LinkIndex || !leaf.Gateway.Equal(selected.Gw) {
			continue
		}
		if leaf.Blackhole || leaf.EncapType != 0 || leaf.Flags&(unix.RTNH_F_DEAD|unix.RTNH_F_LINKDOWN) != 0 {
			return false, false, nil
		}
		matched = true
	}
	return len(nexthop.Group) > 1, matched, nil
}
