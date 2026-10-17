package netif

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/interfaceintent"
)

// OwnedLinkPreparer applies per-link settings to a down owned link before the
// link reconciler sets the link up. The kernel reads some settings only at the
// moment the link starts IPv6 address configuration.
type OwnedLinkPreparer func(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, link OwnedLinkResult) error

func applyLinkSettings(ctx context.Context, pass *ownedLinkPass, link netlink.Link, connection interfaceintent.Connection, result OwnedLinkResult) error {
	settings := connection.Link
	if settings.MTU != nil && link.Attrs().MTU != int(*settings.MTU) {
		if err := netlink.LinkSetMTU(link, int(*settings.MTU)); err != nil {
			slog.Warn("link MTU update failed", "link", link.Attrs().Name, "mtu", *settings.MTU, "err", err)
			return fmt.Errorf("set MTU on %s: %w", link.Attrs().Name, err)
		}
	}
	if settings.HardwareAddress != "" {
		address, err := net.ParseMAC(settings.HardwareAddress)
		if err != nil {
			slog.Warn("configured link MAC is invalid", "link", link.Attrs().Name, "err", err)
			return fmt.Errorf("parse configured MAC: %w", err)
		}
		if !strings.EqualFold(link.Attrs().HardwareAddr.String(), address.String()) {
			if err := netlink.LinkSetHardwareAddr(link, address); err != nil {
				slog.Warn("link MAC update failed", "link", link.Attrs().Name, "err", err)
				return fmt.Errorf("set MAC on %s: %w", link.Attrs().Name, err)
			}
		}
	}
	return applyLinkEnabled(ctx, pass, link, connection, result)
}

func applyLinkEnabled(ctx context.Context, pass *ownedLinkPass, link netlink.Link, connection interfaceintent.Connection, result OwnedLinkResult) error {
	desired := connection.Enabled == nil || *connection.Enabled
	if desired == (link.Attrs().Flags&net.FlagUp != 0) {
		return nil
	}
	if desired {
		prepareLink(ctx, pass, connection, result)
		if err := netlink.LinkSetUp(link); err != nil {
			slog.Warn("link enable failed", "link", link.Attrs().Name, "err", err)
			return fmt.Errorf("raise link %s: %w", link.Attrs().Name, err)
		}
		return nil
	}
	if err := netlink.LinkSetDown(link); err != nil {
		slog.Warn("link disable failed", "link", link.Attrs().Name, "err", err)
		return fmt.Errorf("lower link %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// prepareLink runs while the link is still down. The kernel sends Router
// Solicitations only when the link-local address finishes duplicate address
// detection after the link comes up, and only when accept_ra already permits
// advertisements at that moment. A preparation failure leaves the link
// unprepared, and the link still goes up. The later kernel policy pass on the
// ready link reports the same error.
func prepareLink(ctx context.Context, pass *ownedLinkPass, connection interfaceintent.Connection, result OwnedLinkResult) {
	if pass.prepare == nil || result.ActualName != connection.Name {
		return
	}
	if err := pass.prepare(ctx, pass.log, connection, result); err != nil {
		pass.log.WarnContext(ctx, "owned link preparation failed", "link", result.ActualName, "connection_id", result.ConnectionID, "err", err)
	}
}
