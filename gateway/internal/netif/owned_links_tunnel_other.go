//go:build !linux

package netif

import "github.com/agoodkind/netlink"

func modifyTunnel(*netlink.Sittun) error {
	return netlink.ErrNotImplemented
}
