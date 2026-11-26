package netif

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/interfaceintent"
	internalclock "goodkind.io/mwan/gateway/internal/clock"
)

const familyIPv4 = "ipv4"

// OwnedStaticRouteProtocol identifies routes installed by the address module.
const OwnedStaticRouteProtocol = 186

// OwnedRoute is one main-table route assigned to an owned connection.
type OwnedRoute struct {
	Destination netip.Prefix
	Gateway     netip.Addr
	Metric      uint32
}

// OwnedRoutesFromIntent omits table IDs because owned routes use the main table.
func OwnedRoutesFromIntent(routes []interfaceintent.RouteIntent) []OwnedRoute {
	owned := make([]OwnedRoute, 0, len(routes)+1)
	for _, route := range routes {
		owned = append(
			owned,
			OwnedRoute{
				Destination: route.Destination,
				Gateway:     route.Gateway,
				Metric:      route.Metric,
			},
		)
	}

	return owned
}

// OwnedAddressLifetime supplies the deadlines for one acquired address.
type OwnedAddressLifetime struct {
	PreferredUntil time.Time
	ValidUntil     time.Time
}

// RecordedRetention selects journaled objects kept during lease validation.
type RecordedRetention struct {
	All      bool
	Prefixes map[netip.Prefix]bool
}

type ownedStaticObject struct {
	Scope        string `json:"scope,omitempty"`
	ConnectionID string `json:"connection_id"`
	Family       string `json:"family"`
	LinkName     string `json:"link_name"`
	LinkIndex    int    `json:"link_index"`
	LinkIdentity string `json:"link_identity"`
	Prefix       string `json:"prefix,omitempty"`
	Destination  string `json:"destination,omitempty"`
	Gateway      string `json:"gateway,omitempty"`
	Metric       int    `json:"metric,omitempty"`
}

type ownedStaticJournal struct {
	BootID         string                 `json:"boot_id"`
	Objects        []ownedStaticObject    `json:"objects"`
	Promotion      []ownedStaticPromotion `json:"promotion,omitempty"`
	EndpointRoutes []ownedEndpointRoute   `json:"endpoint_routes,omitempty"`
}

type ownedStaticPromotion struct {
	ConnectionID string `json:"connection_id"`
	LinkName     string `json:"link_name"`
	LinkIndex    int    `json:"link_index"`
	LinkIdentity string `json:"link_identity"`
	Previous     string `json:"previous"`
}

// OwnedStaticPruneError reports a failed removal for one connection family.
type OwnedStaticPruneError struct {
	ConnectionID string
	LinkName     string
	Family       string
	Err          error
}

// Error returns the removal failure.
func (e *OwnedStaticPruneError) Error() string { return e.Err.Error() }

// Unwrap returns the underlying kernel or journal error.
func (e *OwnedStaticPruneError) Unwrap() error { return e.Err }

// OwnedStaticReconciler persists each exact address or route before kernel writes.
type OwnedStaticReconciler struct {
	mu      sync.Mutex
	path    string
	journal ownedStaticJournal
	clock   internalclock.Clock
}

// NewOwnedStaticReconciler reads a durable static ownership journal.
func NewOwnedStaticReconciler(path string) (*OwnedStaticReconciler, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("static ownership state path must be absolute")
	}

	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		slog.Warn("static ownership boot ID read failed", "err", err)
		return nil, fmt.Errorf("read boot ID: %w", err)
	}

	r := &OwnedStaticReconciler{
		mu:   sync.Mutex{},
		path: path,
		journal: ownedStaticJournal{
			BootID:         string(bootID),
			Objects:        nil,
			Promotion:      nil,
			EndpointRoutes: nil,
		},
		clock: internalclock.Real{},
	}
	if _, err := readOwnedJournal(path, &r.journal); err != nil {
		slog.Warn("static ownership journal read failed", "path", path, "err", err)
		return nil, fmt.Errorf("read static ownership journal: %w", err)
	}

	if r.journal.BootID != string(bootID) {
		r.journal = ownedStaticJournal{
			BootID:         string(bootID),
			Objects:        nil,
			Promotion:      nil,
			EndpointRoutes: nil,
		}
	}

	if err := validateScopedAddressRecords(r.journal.Objects); err != nil {
		return nil, err
	}

	return r, nil
}

func validateScopedAddressRecords(objects []ownedStaticObject) error {
	for _, value := range objects {
		if value.Scope == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(value.Prefix)
		if value.Scope != nptEdgeScope || err != nil || value.Family != "ipv6" ||
			!prefix.Addr().Is6() ||
			prefix.Bits() != 128 ||
			value.ConnectionID == "" ||
			value.LinkName == "" ||
			value.LinkIndex < 1 ||
			value.LinkIdentity == "" ||
			value.Gateway != "" ||
			value.Destination != "" {
			return fmt.Errorf("invalid scoped address ownership record for %s", value.ConnectionID)
		}
	}

	return nil
}

func (r *OwnedStaticReconciler) save() error {
	const journal = "static ownership journal"
	if err := writeOwnedJournal(r.path, journal, r.journal); err != nil {
		return err
	}

	return syncOwnedDirectory(r.path, "static ownership", journal)
}

func staticFamily(family string) int {
	if family == familyIPv4 {
		return unix.AF_INET
	}

	return unix.AF_INET6
}

func staticObjectKey(value ownedStaticObject) string {
	if value.Scope != "" {
		return value.Scope + "/" + value.ConnectionID + "/" + value.Family + "/addr/" + value.Prefix
	}

	if value.Prefix != "" {
		return value.ConnectionID + "/" + value.Family + "/addr/" + value.Prefix
	}

	if value.Destination != "" {
		return fmt.Sprintf(
			"%s/%s/route/%s/%s/%d",
			value.ConnectionID,
			value.Family,
			value.Destination,
			value.Gateway,
			value.Metric,
		)
	}

	return fmt.Sprintf(
		"%s/%s/route/%s/%d",
		value.ConnectionID,
		value.Family,
		value.Gateway,
		value.Metric,
	)
}

func (r *OwnedStaticReconciler) recorded(value ownedStaticObject) bool {
	for _, old := range r.journal.Objects {
		if staticObjectKey(old) == staticObjectKey(value) && old.LinkIndex == value.LinkIndex &&
			old.LinkIdentity == value.LinkIdentity {
			return true
		}
	}

	return false
}

func (r *OwnedStaticReconciler) reserve(value ownedStaticObject) error {
	r.journal.Objects = append(r.journal.Objects, value)
	if err := r.save(); err != nil {
		r.journal.Objects = r.journal.Objects[:len(r.journal.Objects)-1]
		return err
	}

	return nil
}

func (r *OwnedStaticReconciler) forget(value ownedStaticObject) error {
	for i, old := range r.journal.Objects {
		if staticObjectKey(old) != staticObjectKey(value) {
			continue
		}

		previous := append([]ownedStaticObject(nil), r.journal.Objects...)
		r.journal.Objects = append(r.journal.Objects[:i], r.journal.Objects[i+1:]...)
		if err := r.save(); err != nil {
			r.journal.Objects = previous
			return err
		}

		return nil
	}

	return nil
}

// ReconcileFamily installs desired objects before pruning recorded obsolete objects.
func (r *OwnedStaticReconciler) ReconcileFamily(
	ctx context.Context,
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	ready OwnedLinkResult,
) error {
	routes := OwnedRoutesFromIntent(settings.Routes)
	if settings.Gateway.IsValid() {
		metric := uint32(0)
		if settings.RouteMetric != nil {
			metric = *settings.RouteMetric
		}

		routes = append(
			routes,
			OwnedRoute{
				Destination: defaultPrefix(family),
				Gateway:     settings.Gateway,
				Metric:      metric,
			},
		)
	}

	return r.ReconcileFamilyRoutes(ctx, connection, family, settings, routes, ready)
}

func defaultPrefix(family string) netip.Prefix {
	if family == familyIPv4 {
		return netip.MustParsePrefix("0.0.0.0/0")
	}

	return netip.MustParsePrefix("::/0")
}

// ReconcileFamilyRoutes installs assigned main-table routes and owned addresses.
func (r *OwnedStaticReconciler) ReconcileFamilyRoutes(
	ctx context.Context,
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	routes []OwnedRoute,
	ready OwnedLinkResult,
) error {
	return r.ReconcileFamilyRoutesWithLifetimes(
		ctx,
		connection,
		family,
		settings,
		routes,
		ready,
		nil,
	)
}

// ReconcileFamilyRoutesWithLifetimes also applies deadlines to acquired addresses.
func (r *OwnedStaticReconciler) ReconcileFamilyRoutesWithLifetimes(
	ctx context.Context,
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	routes []OwnedRoute,
	ready OwnedLinkResult,
	lifetimes map[netip.Prefix]OwnedAddressLifetime,
) error {
	return r.ReconcileFamilyRoutesWithLifetimesRetaining(
		ctx,
		connection,
		family,
		settings,
		routes,
		ready,
		lifetimes,
		RecordedRetention{All: false, Prefixes: nil},
	)
}

// ReconcileFamilyRoutesWithLifetimesRetaining applies desired objects and retains selected recorded objects on the current link.
func (r *OwnedStaticReconciler) ReconcileFamilyRoutesWithLifetimesRetaining(
	ctx context.Context,
	connection interfaceintent.Connection,
	family string,
	settings interfaceintent.Family,
	routes []OwnedRoute,
	ready OwnedLinkResult,
	lifetimes map[netip.Prefix]OwnedAddressLifetime,
	retention RecordedRetention,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("static reconciliation canceled: %w", err)
	}

	if ready.Status != OwnedLinkReady || ready.ConnectionID != connection.ID.String() ||
		ready.IfIndex == 0 {
		return fmt.Errorf("connection %s link is not ready", connection.ID)
	}

	link, err := netlink.LinkByIndex(ready.IfIndex)
	if err != nil {
		slog.Warn("ready link verification failed", "connection_id", connection.ID, "err", err)
		return fmt.Errorf("verify ready link: %w", err)
	}

	if link.Attrs().Name != ready.ActualName || ready.Name != connection.Name {
		return fmt.Errorf("connection %s link identity changed", connection.ID)
	}

	seenRoutes := make(map[string]netip.Addr, len(routes))

	routes = normalizeOwnedRoutes(routes, family)
	for _, assigned := range routes {
		if !assigned.Destination.IsValid() || !assigned.Gateway.IsValid() ||
			assigned.Destination.Addr().Is4() != assigned.Gateway.Is4() ||
			assigned.Destination.Addr().Is4() != (family == familyIPv4) {
			return fmt.Errorf(
				"invalid owned %s route %s via %s",
				family,
				assigned.Destination,
				assigned.Gateway,
			)
		}

		slot := fmt.Sprintf("%s/%d", assigned.Destination.Masked(), assigned.Metric)
		if previous, exists := seenRoutes[slot]; exists && previous != assigned.Gateway {
			return fmt.Errorf("conflicting owned routes for %s", slot)
		}

		seenRoutes[slot] = assigned.Gateway
	}

	base := ownedStaticObject{
		Scope:        "",
		ConnectionID: connection.ID.String(),
		Family:       family,
		LinkName:     ready.ActualName,
		LinkIndex:    ready.IfIndex,
		LinkIdentity: LinkOwnershipIdentity(link),
		Prefix:       "",
		Destination:  "",
		Gateway:      "",
		Metric:       0,
	}
	if family == familyIPv4 && len(settings.Addresses) != 0 {
		if err := r.ensurePromotion(base); err != nil {
			return err
		}
	}

	desired := make(map[string]bool)

	for _, address := range settings.Addresses {
		value := base
		value.Prefix = address.Prefix.String()
		if err := r.ensureAddress(link, value, lifetimes[address.Prefix]); err != nil {
			return err
		}

		desired[staticObjectKey(value)] = true
	}

	orderedRoutes := append([]OwnedRoute(nil), routes...)
	// An off-subnet gateway can require a DHCP on-link route before the default.
	sort.SliceStable(
		orderedRoutes,
		func(i, j int) bool {
			leftOnLink := orderedRoutes[i].Gateway.IsUnspecified()
			rightOnLink := orderedRoutes[j].Gateway.IsUnspecified()
			if leftOnLink != rightOnLink {
				return leftOnLink
			}

			return orderedRoutes[i].Destination.Bits() > orderedRoutes[j].Destination.Bits()
		},
	)

	for _, assigned := range orderedRoutes {
		value := base
		value.Gateway = assigned.Gateway.String()
		value.Metric = int(assigned.Metric)
		if assigned.Destination.Bits() != 0 {
			value.Destination = assigned.Destination.Masked().String()
		}

		if err := r.ensureRoute(value); err != nil {
			return err
		}

		desired[staticObjectKey(value)] = true
	}

	return r.completeFamily(base, len(settings.Addresses) == 0, desired, retention)
}

func normalizeOwnedRoutes(routes []OwnedRoute, family string) []OwnedRoute {
	normalized := append([]OwnedRoute(nil), routes...)
	for index := range normalized {
		if !normalized[index].Gateway.IsValid() {
			normalized[index].Gateway = netip.IPv6Unspecified()
			if family == familyIPv4 {
				normalized[index].Gateway = netip.IPv4Unspecified()
			}
		}

		// Linux substitutes 1024 for an IPv6 route metric of zero.
		if family == "ipv6" && normalized[index].Metric == 0 {
			normalized[index].Metric = 1024
		}
	}

	return normalized
}

func (r *OwnedStaticReconciler) completeFamily(
	base ownedStaticObject,
	noAddresses bool,
	desired map[string]bool,
	retention RecordedRetention,
) error {
	if err := r.pruneFamily(base, desired, retention); err != nil {
		return err
	}

	if base.Family == familyIPv4 && noAddresses && !retention.All {
		return r.restorePromotion(base.ConnectionID)
	}

	return nil
}

func promotionPath(name string) string {
	return filepath.Join("/proc/sys/net/ipv4/conf", name, "promote_secondaries")
}

func readPromotion(name string) (string, error) {
	data, err := os.ReadFile(promotionPath(name))
	if err != nil {
		slog.Warn("promote_secondaries read failed", "link", name, "err", err)
		return "", fmt.Errorf("read promote_secondaries for %s: %w", name, err)
	}

	value := string(data)
	if value != "0\n" && value != "1\n" {
		slog.Warn("promote_secondaries value invalid", "link", name, "value", value)
		return "", fmt.Errorf("unexpected promote_secondaries value %q for %s", value, name)
	}

	return value, nil
}

func writePromotion(name, value string) error {
	if err := os.WriteFile(promotionPath(name), []byte(value), 0o600); err != nil {
		slog.Warn("promote_secondaries write failed", "link", name, "value", value, "err", err)
		return fmt.Errorf("write promote_secondaries for %s: %w", name, err)
	}

	actual, err := readPromotion(name)
	if err != nil {
		return err
	}

	if actual != value {
		slog.Warn(
			"promote_secondaries verification failed",
			"link",
			name,
			"wanted",
			value,
			"actual",
			actual,
		)

		return fmt.Errorf("promote_secondaries for %s is %q after writing %q", name, actual, value)
	}

	return nil
}

func (r *OwnedStaticReconciler) ensurePromotion(value ownedStaticObject) error {
	for i, record := range r.journal.Promotion {
		if record.ConnectionID != value.ConnectionID {
			continue
		}

		if record.LinkName != value.LinkName || record.LinkIndex != value.LinkIndex ||
			record.LinkIdentity != value.LinkIdentity {
			old, err := netlink.LinkByIndex(record.LinkIndex)
			if err != nil && !IsLinkNotFound(err) {
				slog.Warn(
					"recorded promotion link lookup failed",
					"connection_id",
					value.ConnectionID,
					"err",
					err,
				)

				return fmt.Errorf("verify recorded promotion link: %w", err)
			}

			if err == nil && LinkMatchesIdentity(old, record.LinkIndex, record.LinkIdentity) {
				return fmt.Errorf(
					"promote_secondaries link identity changed for %s",
					value.ConnectionID,
				)
			}

			if err := r.forgetPromotion(i); err != nil {
				return err
			}

			break
		}

		return writePromotion(value.LinkName, "1\n")
	}

	previous, err := readPromotion(value.LinkName)
	if err != nil {
		return err
	}

	r.journal.Promotion = append(
		r.journal.Promotion,
		ownedStaticPromotion{
			ConnectionID: value.ConnectionID,
			LinkName:     value.LinkName,
			LinkIndex:    value.LinkIndex,
			LinkIdentity: value.LinkIdentity,
			Previous:     previous,
		},
	)
	if err := r.save(); err != nil {
		r.journal.Promotion = r.journal.Promotion[:len(r.journal.Promotion)-1]
		return err
	}

	return writePromotion(value.LinkName, "1\n")
}

func (r *OwnedStaticReconciler) restorePromotion(id string) error {
	for i, record := range r.journal.Promotion {
		if record.ConnectionID != id {
			continue
		}

		link, err := netlink.LinkByIndex(record.LinkIndex)
		if IsLinkNotFound(err) {
			return r.forgetPromotion(i)
		}

		if err != nil {
			slog.Warn("promotion link lookup failed", "connection_id", id, "err", err)
			return fmt.Errorf("find promotion link: %w", err)
		}

		if !LinkMatchesIdentity(link, record.LinkIndex, record.LinkIdentity) {
			return r.forgetPromotion(i)
		}

		if link.Attrs().Name != record.LinkName {
			return fmt.Errorf("promotion link name changed for %s", id)
		}

		current, err := readPromotion(record.LinkName)
		if err != nil {
			return err
		}

		if current != "1\n" && current != record.Previous {
			return fmt.Errorf("promote_secondaries changed outside MWAN for %s", id)
		}

		if current == "1\n" && record.Previous != current {
			if err := writePromotion(record.LinkName, record.Previous); err != nil {
				return err
			}
		}

		return r.forgetPromotion(i)
	}

	return nil
}

func (r *OwnedStaticReconciler) forgetPromotion(index int) error {
	previous := append([]ownedStaticPromotion(nil), r.journal.Promotion...)
	r.journal.Promotion = append(r.journal.Promotion[:index], r.journal.Promotion[index+1:]...)
	if err := r.save(); err != nil {
		r.journal.Promotion = previous
		return err
	}

	return nil
}

func addressMatches(address netlink.Addr, prefix netip.Prefix) bool {
	if address.IPNet == nil {
		return false
	}

	bits, _ := address.Mask.Size()

	return address.IP.Equal(net.IP(prefix.Addr().AsSlice())) && bits == prefix.Bits()
}

func (r *OwnedStaticReconciler) ensureAddress(
	link netlink.Link,
	value ownedStaticObject,
	lifetime OwnedAddressLifetime,
) error {
	prefix, err := netip.ParsePrefix(value.Prefix)
	if err != nil {
		slog.Warn("owned address prefix parse failed", "prefix", value.Prefix, "err", err)
		return fmt.Errorf("parse owned prefix: %w", err)
	}

	found, replace, installed, err := r.findOwnedAddress(link, value, prefix)
	if err != nil {
		return err
	}

	now := r.clock.Now()
	if !lifetime.ValidUntil.IsZero() && !now.Before(lifetime.ValidUntil) {
		return fmt.Errorf("owned address %s lease expired", value.Prefix)
	}

	if found && !r.recorded(value) {
		return fmt.Errorf("address %s already exists without ownership record", value.Prefix)
	}

	if !found && !r.recorded(value) {
		if err := r.reserve(value); err != nil {
			return err
		}
	}

	if !lifetime.ValidUntil.IsZero() {
		valid := leaseLifetimeSeconds(lifetime.ValidUntil, now)
		preferred := min(valid, leaseLifetimeSeconds(lifetime.PreferredUntil, now))
		if found &&
			(lifetimeDifference(
				installed.ValidLft,
				valid,
			) > 2 || lifetimeDifference(installed.PreferedLft, preferred) > 2) {
			replace = true
		}
	}

	if found && !replace {
		return nil
	}

	return r.writeOwnedAddress(link, value, lifetime, replace)
}

func (r *OwnedStaticReconciler) findOwnedAddress(
	link netlink.Link,
	value ownedStaticObject,
	prefix netip.Prefix,
) (bool, bool, netlink.Addr, error) {
	addresses, err := netlink.AddrList(link, staticFamily(value.Family))
	if err != nil {
		slog.Warn("owned address list failed", "link", link.Attrs().Name, "err", err)
		return false, false, netlink.Addr{}, fmt.Errorf("list link addresses: %w", err)
	}

	found := false
	replace := false
	var installed netlink.Addr

	for _, address := range addresses {
		if !address.IP.Equal(net.IP(prefix.Addr().AsSlice())) {
			continue
		}

		if !addressMatches(address, prefix) {
			old := value
			old.Prefix = address.IPNet.String()
			if !r.recorded(old) {
				return false, false, netlink.Addr{}, fmt.Errorf(
					"foreign address uses %s with another prefix",
					prefix.Addr(),
				)
			}

			replace = true
			continue
		}

		found = true
		installed = address
	}

	return found, replace, installed, nil
}

func (r *OwnedStaticReconciler) writeOwnedAddress(
	link netlink.Link,
	value ownedStaticObject,
	lifetime OwnedAddressLifetime,
	replace bool,
) error {
	address, err := netlink.ParseAddr(value.Prefix)
	if err != nil {
		return fmt.Errorf("parse netlink address: %w", err)
	}

	if !lifetime.ValidUntil.IsZero() {
		now := r.clock.Now()
		if !now.Before(lifetime.ValidUntil) {
			return fmt.Errorf("owned address %s lease expired", value.Prefix)
		}

		address.ValidLft = leaseLifetimeSeconds(lifetime.ValidUntil, now)
		address.PreferedLft = min(
			address.ValidLft,
			leaseLifetimeSeconds(lifetime.PreferredUntil, now),
		)
	}

	if replace {
		err = netlink.AddrReplace(link, address)
	} else {
		err = netlink.AddrAdd(link, address)
	}

	if err != nil {
		slog.Warn("owned static address write failed", "prefix", value.Prefix, "err", err)
		return fmt.Errorf("write owned address %s: %w", value.Prefix, err)
	}

	return nil
}

func leaseLifetimeSeconds(deadline, now time.Time) int {
	if !deadline.After(now) {
		return 0
	}

	return max(1, int(deadline.Sub(now).Seconds()))
}

func lifetimeDifference(left, right int) int {
	if left > right {
		return left - right
	}

	return right - left
}

// PruneRemoved removes owned routes for deleted connections or families.
func (r *OwnedStaticReconciler) PruneRemoved(desired map[string]map[string]bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	old := append([]ownedStaticObject(nil), r.journal.Objects...)
	for _, value := range old {
		if value.Scope != "" {
			continue
		}

		families := desired[value.ConnectionID]
		if families[value.Family] {
			continue
		}

		if err := r.remove(value); err != nil {
			return &OwnedStaticPruneError{
				ConnectionID: value.ConnectionID,
				LinkName:     value.LinkName,
				Family:       value.Family,
				Err:          err,
			}
		}
	}

	for len(r.journal.Promotion) != 0 {
		found := false

		for _, record := range r.journal.Promotion {
			if desired[record.ConnectionID][familyIPv4] {
				continue
			}

			if err := r.restorePromotion(record.ConnectionID); err != nil {
				return &OwnedStaticPruneError{
					ConnectionID: record.ConnectionID,
					LinkName:     record.LinkName,
					Family:       familyIPv4,
					Err:          err,
				}
			}

			found = true
			break
		}

		if !found {
			break
		}
	}

	return nil
}
