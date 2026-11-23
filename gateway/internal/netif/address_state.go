package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// AddrSpec identifies an address in CIDR notation.
type AddrSpec struct {
	CIDR   string
	Family string
}

// CurrentAddr is one address present on the interface, as observed via
// netlink AddrList. Mirrored from netlink.Addr but exposed as our own
// type so callers do not need to import vishvananda/netlink.
type CurrentAddr struct {
	CIDR   string
	Family string // "inet" or "inet6"
	// Flags is the raw IFA_F_* bitmask from netlink (linux/if_addr.h).
	Flags int
	Scope int
	// Lifetimes are seconds remaining at ObservedAt. A negative value means unknown.
	PreferredLifetime int
	ValidLifetime     int
	ObservedAt        time.Time
	// Origin is unknown unless a separate acquisition source establishes it.
	Origin string
}

// IFA_F_* flag constants from linux/if_addr.h. These flags describe
// address state but do not establish acquisition provenance.
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

// ListAddrs returns IPv4 and IPv6 addresses currently assigned to iface.
// It returns a wrapped error if the link lookup or netlink query fails.
//
// Example:
//
//	addresses, err := ListAddrs(ctx, log, "wan0")
func ListAddrs(ctx context.Context, log *slog.Logger, iface string) ([]CurrentAddr, error) {
	log = log.With("component", "addrs", "op", "list", "iface", iface)
	link, err := netlink.LinkByName(iface)
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
		"link",
		link.Attrs().Name,
		"count",
		len(addrs),
		"duration_ms",
		dur.Milliseconds(),
		"err",
		err,
	)
	if err != nil {
		log.Warn("addrs: AddrList failed", "link", link.Attrs().Name, "err", err)

		return nil, fmt.Errorf("AddrList(%s): %w", link.Attrs().Name, err)
	}

	out := make([]CurrentAddr, 0, len(addrs))
	for _, address := range addrs {
		if address.IPNet == nil {
			continue
		}

		family := tunnelEndpointRouteFamily
		if address.IP.To4() == nil {
			family = string(httpFamilyV6)
		}

		out = append(
			out,
			CurrentAddr{
				CIDR:              address.IPNet.String(),
				Family:            family,
				Flags:             address.Flags,
				Scope:             address.Scope,
				PreferredLifetime: address.PreferedLft,
				ValidLifetime:     address.ValidLft,
				ObservedAt:        startTime,
				Origin:            HealthStateUnknown,
			},
		)
	}

	return out, nil
}

// RouteSpec defines a route the daemon must keep present in a routing table.
type RouteSpec struct {
	Family   string // Address family: "inet" or "inet6".
	Dest     string // Destination prefix, such as "default" or "::/0".
	Via      string // Next-hop address. IPv6 routes can use link-local addresses.
	Dev      string // Interface name for the route.
	TableID  int    // Routing table ID.
	Metric   int    // A zero metric omits the route metric.
	Protocol int    // A zero value uses the kernel's default protocol.
}

// CurrentRoute records the route fields returned by Linux netlink.
type CurrentRoute struct {
	Family   string
	Dest     string
	Via      string
	Dev      string
	TableID  int
	Protocol int
	Metric   int
	Scope    int
	Type     int
	NextHops []RouteNextHop
}

// RouteNextHop records one kernel next hop in a multipath route.
type RouteNextHop struct {
	Via       string
	Dev       string
	LinkIndex int
	Weight    int
}

// ReconcileTableDefault sets the table's default route to want.
// It leaves other routes unchanged and clears the default when Via is empty.
//
// Example:
//
//	ReconcileTableDefault(ctx, log, RouteSpec{Family: "inet", TableID: 100, Via: "192.0.2.1"})
func ReconcileTableDefault(
	ctx context.Context,
	log *slog.Logger,
	want RouteSpec,
) error {
	log = log.With(
		"component",
		"route",
		"family",
		want.Family,
		"table_id",
		want.TableID,
		"op",
		"reconcile",
	)
	log.DebugContext(ctx, "route: reconcile entry", "want", want)

	cur, err := getTableDefaultNetlink(log, want.Family, want.TableID)
	if err != nil {
		log.WarnContext(ctx, "route: getTableDefaultNetlink failed", "err", err)

		return fmt.Errorf("read table default: %w", err)
	}

	log.DebugContext(ctx, "route: current default in table", "current", cur, "want", want)

	if want.Via == "" {
		// Caller wants no default. Delete if present.
		if cur == nil {
			log.DebugContext(ctx, "route: no default present and none wanted")

			return nil
		}

		log.DebugContext(
			ctx,
			"route: removing default from table",
			"old_via",
			cur.Via,
			"old_dev",
			cur.Dev,
		)
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
		ctx,
		"route: replacing default in table",
		"new_via",
		want.Via,
		"new_dev",
		want.Dev,
		"old",
		cur,
	)
	err = replaceTableDefaultNetlink(ctx, log, want)
	if err != nil {
		log.WarnContext(ctx, "route: replaceTableDefaultNetlink failed", "err", err)
	}

	return err
}

// ReconcileTableRoute installs the requested non-default prefix route.
// It leaves other table entries unchanged.
//
// Example:
//
//	ReconcileTableRoute(ctx, log, RouteSpec{Family: "inet", TableID: 100, Dest: "192.0.2.0/24"})
func ReconcileTableRoute(
	ctx context.Context,
	log *slog.Logger,
	want RouteSpec,
) error {
	log = log.With(
		"component",
		"route",
		"family",
		want.Family,
		"table_id",
		want.TableID,
		"op",
		"reconcile-prefix",
	)
	log.DebugContext(ctx, "route: reconcile prefix entry", "want", want)

	return replaceTableRouteNetlink(ctx, log, want)
}

// DeleteTableRoute removes the requested non-default prefix route.
// It returns an error when the kernel rejects the deletion.
//
// Example:
//
//	DeleteTableRoute(ctx, log, RouteSpec{Family: "inet", TableID: 100, Dest: "192.0.2.0/24"})
func DeleteTableRoute(
	ctx context.Context,
	log *slog.Logger,
	want RouteSpec,
) error {
	log = log.With(
		"component",
		"route",
		"family",
		want.Family,
		"table_id",
		want.TableID,
		"op",
		"delete-prefix",
	)
	log.DebugContext(ctx, "route: delete prefix entry", "want", want)
	err := delTableRouteNetlink(ctx, log, want)
	if err != nil {
		log.WarnContext(ctx, "route: delTableRouteNetlink failed", "err", err)
	}

	return err
}

// IsLinkNotFound reports whether a link lookup failed because the interface
// does not exist. It checks the netlink error type so callers can distinguish
// a vanished interface from another lookup failure.
//
// Example:
//
//	if IsLinkNotFound(err) {
//		return nil
//	}
func IsLinkNotFound(err error) bool {
	var notFound netlink.LinkNotFoundError

	return errors.As(err, &notFound)
}

// familyToNetlink converts our string family ("inet"/"inet6") into the
// AF_INET / AF_INET6 constant netlink expects.
func familyToNetlink(family string) int {
	if family == tunnelEndpointRouteFamily {
		return unix.AF_INET
	}

	return unix.AF_INET6
}
