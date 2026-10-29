package netif

import (
	"fmt"
	"log/slog"

	"github.com/vishvananda/netlink"
)

func modifyTunnel(requested *netlink.Sittun) error {
	if err := netlink.LinkModify(requested); err != nil {
		slog.Warn("owned tunnel update failed", "link", requested.Name, "index", requested.Index, "err", err)
		return fmt.Errorf("update tunnel %s: %w", requested.Name, err)
	}
	return nil
}
