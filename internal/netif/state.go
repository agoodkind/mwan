package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// AddrSpec is one IP address (CIDR notation) we want present on an iface.
// Family is derived from the CIDR ("inet" if it parses as IPv4, "inet6"
// otherwise; the caller can override).
type AddrSpec struct {
	CIDR   string
	Family string // "inet" or "inet6"; if empty, derived from CIDR
}

func (a AddrSpec) family() string {
	if a.Family != "" {
		return a.Family
	}
	if strings.Contains(a.CIDR, ":") {
		return "inet6"
	}
	return "inet"
}

// CurrentAddr is one address present on the interface, as observed via
// netlink AddrList. Mirrored from netlink.Addr but exposed as our own
// type so callers do not need to import vishvananda/netlink.
type CurrentAddr struct {
	CIDR   string
	Family string // "inet" or "inet6"
	// Flags is the raw IFA_F_* bitmask from netlink (linux/if_addr.h).
	// Useful flags include IFA_F_PERMANENT (0x80) which is set when the
	// address was added administratively rather than via SLAAC autoconf.
	Flags int
}

// IFA_F_* flag constants from linux/if_addr.h, exposed so callers can
// classify addresses (SLAAC vs manually-added vs deprecated etc.) without
// importing the kernel headers or vishvananda/netlink. Matches the values
// returned in CurrentAddr.Flags.
const (
	IFAFTemporary      = 0x01
	IFAFNoDAD          = 0x02
	IFAFOptimistic     = 0x04
	IFAFDADFailed      = 0x08
	IFAFHomeAddress    = 0x10
	IFAFDeprecated     = 0x20
	IFAFTentative      = 0x40
	IFAFPermanent      = 0x80
	IFAFManageTempAddr = 0x100
	IFAFNoPrefixRoute  = 0x200
	IFAFMcAutoJoin     = 0x400
	IFAFStablePrivacy  = 0x800
)

// ReconcileAddrs ensures every spec in desired is present on iface.
// Foreign addresses are NOT removed; the daemon only adds what it owns.
// (Removal of unmanaged addresses is out of scope; trust the operator.)
//
// Implementation uses vishvananda/netlink directly. The runner argument
// is retained for interface-level back-compat with callers that still
// pass it in step 3; it is not consulted for this operation.
func ReconcileAddrs(
	ctx context.Context, log *slog.Logger,
	iface string, desired []AddrSpec,
) error {
	log = log.With("component", "addrs", "iface", iface, "op", "reconcile")
	log.DebugContext(ctx, "addrs: reconcile entry", "desired_count", len(desired))

	link, err := linkByName(log, iface)
	if err != nil {
		log.WarnContext(ctx, "addrs: linkByName failed", "err", err)
		return err
	}

	current, err := listAddrsNetlink(log, link)
	if err != nil {
		log.WarnContext(ctx, "addrs: listAddrsNetlink failed", "err", err)
		return fmt.Errorf("list addrs: %w", err)
	}
	log.DebugContext(ctx, "addrs: current snapshot",
		"count", len(current), "addrs", current)

	have := map[string]bool{}
	for _, c := range current {
		have[normalizeCIDR(c.CIDR)] = true
	}

	for _, w := range desired {
		key := normalizeCIDR(w.CIDR)
		if have[key] {
			log.DebugContext(ctx, "addrs: already present", "addr", w.CIDR)
			continue
		}
		log.DebugContext(ctx, "addrs: adding", "addr", w.CIDR, "family", w.family())
		if err := addAddrNetlink(ctx, log, link, w); err != nil {
			log.WarnContext(ctx, "addrs: addAddrNetlink failed", "addr", w.CIDR, "err", err)
			return fmt.Errorf("add addr %s: %w", w.CIDR, err)
		}
	}
	log.DebugContext(ctx, "addrs: reconcile complete", "applied", len(desired))
	return nil
}

// ListAddrs returns every address (v4 and v6) currently assigned to iface.
// Wraps RTM_GETADDR via netlink. Returns ENODEV-style errors as nil link
// not found, matching ReconcileAddrs' shape.
func ListAddrs(ctx context.Context, log *slog.Logger, iface string) ([]CurrentAddr, error) {
	log = log.With("component", "addrs", "op", "list", "iface", iface)
	link, err := linkByName(log, iface)
	if err != nil {
		log.WarnContext(ctx, "addrs: linkByName failed", "err", err)
		return nil, fmt.Errorf("link %s: %w", iface, err)
	}
	return listAddrsNetlink(log, link)
}

// listAddrsNetlink returns parsed addresses on link, both families. Uses
// netlink.AddrList (RTM_GETADDR) directly. Logged at DEBUG with counts.
func listAddrsNetlink(log *slog.Logger, link netlink.Link) ([]CurrentAddr, error) {
	startTime := realClock{}.Now()
	addrs, err := netlink.AddrList(link, unix.AF_UNSPEC)
	dur := realClock{}.Now().Sub(startTime)
	log.Debug(
		"addrs: AddrList",
		"link", link.Attrs().Name,
		"count", len(addrs),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.Warn("addrs: AddrList failed", "link", link.Attrs().Name, "err", err)
		return nil, fmt.Errorf("AddrList(%s): %w", link.Attrs().Name, err)
	}
	out := make([]CurrentAddr, 0, len(addrs))
	for _, a := range addrs {
		if a.IPNet == nil {
			continue
		}
		fam := "inet"
		if a.IP.To4() == nil {
			fam = "inet6"
		}
		out = append(out, CurrentAddr{CIDR: a.IPNet.String(), Family: fam, Flags: a.Flags})
	}
	return out, nil
}

// normalizeCIDR lower-cases the address half so comparisons are stable
// across IPv6 representations the kernel may return.
func normalizeCIDR(cidr string) string {
	return strings.ToLower(cidr)
}

// addAddrNetlink calls netlink.AddrReplace with a parsed CIDR. Returns nil
// if the address already exists at that prefix; a no-op AddrReplace returns
// nil from the kernel. Logged at DEBUG with parsed Addr struct.
func addAddrNetlink(
	ctx context.Context, log *slog.Logger, link netlink.Link, a AddrSpec,
) error {
	addr, err := netlink.ParseAddr(a.CIDR)
	if err != nil {
		log.WarnContext(ctx, "addrs: ParseAddr failed", "cidr", a.CIDR, "err", err)
		return fmt.Errorf("parse %q: %w", a.CIDR, err)
	}
	start := realClock{}.Now()
	err = netlink.AddrReplace(link, addr)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx, "addrs: AddrReplace",
		"link", link.Attrs().Name,
		"cidr", addr.IPNet.String(),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.WarnContext(ctx, "addrs: AddrReplace failed",
			"link", link.Attrs().Name, "cidr", addr.IPNet.String(), "err", err)
		return fmt.Errorf("AddrReplace(%s,%s): %w", link.Attrs().Name, addr.IPNet.String(), err)
	}
	return nil
}

// RouteSpec describes one route the daemon wants to keep present in a
// particular table. Currently only "default" is needed but the type is
// generic for future use.
type RouteSpec struct {
	Family   string // "inet" or "inet6"
	Dest     string // e.g. "default" or "::/0"
	Via      string // gateway address (link-local OK for inet6)
	Dev      string // outgoing interface
	TableID  int    // routing table ID (e.g. 500 for "oob")
	Metric   int    // optional; 0 omits the metric
	Protocol int    // optional route protocol; 0 leaves the kernel default
}

// CurrentRoute is one observed route entry. Mirrored from netlink.Route
// for callers who do not want to import vishvananda/netlink.
type CurrentRoute struct {
	Dest   string
	Via    string
	Dev    string
	Metric int
}

// ReconcileTableDefault ensures the table contains exactly the desired
// default route. If the current default's gateway differs, it's replaced.
// If no default exists, it's added. Other entries in the table are not
// touched. Pass want.Via == "" to clear the default route from the table.
func ReconcileTableDefault(
	ctx context.Context, log *slog.Logger,
	want RouteSpec,
) error {
	log = log.With("component", "route",
		"family", want.Family, "table_id", want.TableID, "op", "reconcile")
	log.DebugContext(ctx, "route: reconcile entry", "want", want)

	cur, err := getTableDefaultNetlink(log, want.Family, want.TableID)
	if err != nil {
		log.WarnContext(ctx, "route: getTableDefaultNetlink failed", "err", err)
		return fmt.Errorf("read table default: %w", err)
	}
	log.DebugContext(ctx, "route: current default in table",
		"current", cur, "want", want)

	if want.Via == "" {
		// Caller wants no default. Delete if present.
		if cur == nil {
			log.DebugContext(ctx, "route: no default present and none wanted")
			return nil
		}
		log.DebugContext(ctx, "route: removing default from table",
			"old_via", cur.Via, "old_dev", cur.Dev)
		err = delTableDefaultNetlink(ctx, log, want.Family, want.TableID)
		if err != nil {
			log.WarnContext(ctx, "route: delTableDefaultNetlink failed", "err", err)
		}
		return err
	}

	if cur != nil && cur.Via == want.Via && cur.Dev == want.Dev {
		log.DebugContext(ctx, "route: default already correct")
		return nil
	}

	log.DebugContext(
		ctx, "route: replacing default in table",
		"new_via", want.Via, "new_dev", want.Dev,
		"old", cur,
	)
	err = replaceTableDefaultNetlink(ctx, log, want)
	if err != nil {
		log.WarnContext(ctx, "route: replaceTableDefaultNetlink failed", "err", err)
	}
	return err
}

// ReconcileTableRoute ensures the table contains the desired non-default
// prefix route. Other entries in the table are not touched.
func ReconcileTableRoute(
	ctx context.Context, log *slog.Logger,
	want RouteSpec,
) error {
	log = log.With("component", "route",
		"family", want.Family, "table_id", want.TableID, "op", "reconcile-prefix")
	log.DebugContext(ctx, "route: reconcile prefix entry", "want", want)
	return replaceTableRouteNetlink(ctx, log, want)
}

// DeleteTableRoute removes the desired non-default prefix route from its table.
func DeleteTableRoute(
	ctx context.Context, log *slog.Logger,
	want RouteSpec,
) error {
	log = log.With("component", "route",
		"family", want.Family, "table_id", want.TableID, "op", "delete-prefix")
	log.DebugContext(ctx, "route: delete prefix entry", "want", want)
	err := delTableRouteNetlink(ctx, log, want)
	if err != nil {
		log.WarnContext(ctx, "route: delTableRouteNetlink failed", "err", err)
	}
	return err
}

// linkByName wraps netlink.LinkByName with debug logging. Centralised so
// every call records the interface lookup.
func linkByName(log *slog.Logger, iface string) (netlink.Link, error) {
	start := realClock{}.Now()
	link, err := netlink.LinkByName(iface)
	dur := realClock{}.Now().Sub(start)
	log.Debug(
		"link: LinkByName",
		"iface", iface,
		"index", linkIndex(link),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.Warn("link: LinkByName failed", "iface", iface, "err", err)
		return nil, fmt.Errorf("link %q: %w", iface, err)
	}
	return link, nil
}

// IsLinkNotFound reports whether err records that the named interface does not
// exist. It matches the netlink error type rather than its text, so callers can
// tell a vanished link apart from a failed read without importing netlink.
func IsLinkNotFound(err error) bool {
	var notFound netlink.LinkNotFoundError
	return errors.As(err, &notFound)
}

func linkIndex(link netlink.Link) int {
	if link == nil {
		return 0
	}
	return link.Attrs().Index
}

// familyToNetlink converts our string family ("inet"/"inet6") into the
// AF_INET / AF_INET6 constant netlink expects.
func familyToNetlink(family string) int {
	if family == "inet" {
		return unix.AF_INET
	}
	return unix.AF_INET6
}
