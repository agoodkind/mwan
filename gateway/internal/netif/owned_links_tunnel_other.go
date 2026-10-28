//go:build !linux

package netif

import "github.com/vishvananda/netlink"

func modifyTunnel(*netlink.Sittun) error {
	return netlink.ErrNotImplemented
}
