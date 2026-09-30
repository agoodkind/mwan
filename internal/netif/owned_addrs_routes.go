package netif

import (
	"context"
	"encoding/json"
	"errors"
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
	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/interfaceintent"
)

// OwnedStaticRouteProtocol identifies routes installed by the address module.
const OwnedStaticRouteProtocol = 186

// OwnedRoute is one main-table route assigned to an owned connection.
type OwnedRoute struct {
	Destination netip.Prefix
	Gateway     netip.Addr
	Metric      uint32
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
	BootID    string                 `json:"boot_id"`
	Objects   []ownedStaticObject    `json:"objects"`
	Promotion []ownedStaticPromotion `json:"promotion,omitempty"`
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
	r := &OwnedStaticReconciler{mu: sync.Mutex{}, path: path, journal: ownedStaticJournal{BootID: string(bootID), Objects: nil, Promotion: nil}, clock: internalclock.Real{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		slog.Warn("static ownership journal read failed", "path", path, "err", err)
		return nil, fmt.Errorf("read static ownership journal: %w", err)
	}
	if err := json.Unmarshal(data, &r.journal); err != nil {
		slog.Warn("static ownership journal decode failed", "path", path, "err", err)
		return nil, fmt.Errorf("decode static ownership journal: %w", err)
	}
	if r.journal.BootID != string(bootID) {
		r.journal = ownedStaticJournal{BootID: string(bootID), Objects: nil, Promotion: nil}
	}
	return r, nil
}

func (r *OwnedStaticReconciler) save() error {
	data, err := json.Marshal(r.journal)
	if err != nil {
		slog.Warn("static ownership journal encode failed", "path", r.path, "err", err)
		return fmt.Errorf("encode static ownership journal: %w", err)
	}
	if _, err := installfile.Write(r.path, append(data, '\n'), 0o600); err != nil {
		slog.Warn("static ownership journal write failed", "path", r.path, "err", err)
		return fmt.Errorf("write static ownership journal: %w", err)
	}
	directory, err := os.Open(filepath.Dir(r.path))
	if err != nil {
		slog.Warn("static ownership journal directory open failed", "path", r.path, "err", err)
		return fmt.Errorf("open static ownership directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		slog.Warn("static ownership journal directory sync failed", "path", r.path, "err", err)
		return fmt.Errorf("sync static ownership directory: %w", err)
	}
	return nil
}

func staticLinkIdentity(link netlink.Link) string {
	if link.Attrs().Alias != "" {
		return "alias:" + link.Attrs().Alias
	}
	if len(link.Attrs().PermHWAddr) != 0 {
		return "mac:" + link.Attrs().PermHWAddr.String()
	}
	return "mac:" + link.Attrs().HardwareAddr.String()
}

func staticFamily(family string) int {
	if family == "ipv4" {
		return unix.AF_INET
	}
	return unix.AF_INET6
}

func staticObjectKey(value ownedStaticObject) string {
	if value.Prefix != "" {
		return value.ConnectionID + "/" + value.Family + "/addr/" + value.Prefix
	}
	if value.Destination != "" {
		return fmt.Sprintf("%s/%s/route/%s/%s/%d", value.ConnectionID, value.Family, value.Destination, value.Gateway, value.Metric)
	}
	return fmt.Sprintf("%s/%s/route/%s/%d", value.ConnectionID, value.Family, value.Gateway, value.Metric)
}

func (r *OwnedStaticReconciler) recorded(value ownedStaticObject) bool {
	for _, old := range r.journal.Objects {
		if staticObjectKey(old) == staticObjectKey(value) && old.LinkIndex == value.LinkIndex && old.LinkIdentity == value.LinkIdentity {
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
func (r *OwnedStaticReconciler) ReconcileFamily(ctx context.Context, connection interfaceintent.Connection, family string, settings interfaceintent.Family, ready OwnedLinkResult) error {
	routes := make([]OwnedRoute, 0, 1)
	if settings.Gateway.IsValid() {
		metric := uint32(0)
		if settings.RouteMetric != nil {
			metric = *settings.RouteMetric
		}
		routes = append(routes, OwnedRoute{Destination: defaultPrefix(family), Gateway: settings.Gateway, Metric: metric})
	}
	return r.ReconcileFamilyRoutes(ctx, connection, family, settings, routes, ready)
}

func defaultPrefix(family string) netip.Prefix {
	if family == "ipv4" {
		return netip.MustParsePrefix("0.0.0.0/0")
	}
	return netip.MustParsePrefix("::/0")
}

// ReconcileFamilyRoutes installs assigned main-table routes and owned addresses.
func (r *OwnedStaticReconciler) ReconcileFamilyRoutes(ctx context.Context, connection interfaceintent.Connection, family string, settings interfaceintent.Family, routes []OwnedRoute, ready OwnedLinkResult) error {
	return r.ReconcileFamilyRoutesWithLifetimes(ctx, connection, family, settings, routes, ready, nil)
}

// ReconcileFamilyRoutesWithLifetimes also applies deadlines to acquired addresses.
func (r *OwnedStaticReconciler) ReconcileFamilyRoutesWithLifetimes(ctx context.Context, connection interfaceintent.Connection, family string, settings interfaceintent.Family, routes []OwnedRoute, ready OwnedLinkResult, lifetimes map[netip.Prefix]OwnedAddressLifetime) error {
	return r.ReconcileFamilyRoutesWithLifetimesRetaining(ctx, connection, family, settings, routes, ready, lifetimes, RecordedRetention{All: false, Prefixes: nil})
}

// ReconcileFamilyRoutesWithLifetimesRetaining applies desired objects and retains selected recorded objects on the current link.
func (r *OwnedStaticReconciler) ReconcileFamilyRoutesWithLifetimesRetaining(ctx context.Context, connection interfaceintent.Connection, family string, settings interfaceintent.Family, routes []OwnedRoute, ready OwnedLinkResult, lifetimes map[netip.Prefix]OwnedAddressLifetime, retention RecordedRetention) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("static reconciliation canceled: %w", err)
	}
	if ready.Status != OwnedLinkReady || ready.ConnectionID != connection.ID.String() || ready.IfIndex == 0 {
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
	for _, assigned := range routes {
		if !assigned.Destination.IsValid() || !assigned.Gateway.IsValid() ||
			assigned.Destination.Addr().Is4() != assigned.Gateway.Is4() ||
			assigned.Destination.Addr().Is4() != (family == "ipv4") {
			return fmt.Errorf("invalid owned %s route %s via %s", family, assigned.Destination, assigned.Gateway)
		}
		slot := fmt.Sprintf("%s/%d", assigned.Destination.Masked(), assigned.Metric)
		if previous, exists := seenRoutes[slot]; exists && previous != assigned.Gateway {
			return fmt.Errorf("conflicting owned routes for %s", slot)
		}
		seenRoutes[slot] = assigned.Gateway
	}
	base := ownedStaticObject{ConnectionID: connection.ID.String(), Family: family, LinkName: ready.ActualName, LinkIndex: ready.IfIndex, LinkIdentity: staticLinkIdentity(link), Prefix: "", Destination: "", Gateway: "", Metric: 0}
	if family == "ipv4" && len(settings.Addresses) != 0 {
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
	sort.SliceStable(orderedRoutes, func(i, j int) bool {
		leftOnLink := orderedRoutes[i].Gateway.IsUnspecified()
		rightOnLink := orderedRoutes[j].Gateway.IsUnspecified()
		if leftOnLink != rightOnLink {
			return leftOnLink
		}
		return orderedRoutes[i].Destination.Bits() > orderedRoutes[j].Destination.Bits()
	})
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

func (r *OwnedStaticReconciler) completeFamily(base ownedStaticObject, noAddresses bool, desired map[string]bool, retention RecordedRetention) error {
	if err := r.pruneFamily(base, desired, retention); err != nil {
		return err
	}
	if base.Family == "ipv4" && noAddresses && !retention.All {
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
		slog.Warn("promote_secondaries verification failed", "link", name, "wanted", value, "actual", actual)
		return fmt.Errorf("promote_secondaries for %s is %q after writing %q", name, actual, value)
	}
	return nil
}

func (r *OwnedStaticReconciler) ensurePromotion(value ownedStaticObject) error {
	for i, record := range r.journal.Promotion {
		if record.ConnectionID != value.ConnectionID {
			continue
		}
		if record.LinkName != value.LinkName || record.LinkIndex != value.LinkIndex || record.LinkIdentity != value.LinkIdentity {
			old, err := netlink.LinkByIndex(record.LinkIndex)
			if err != nil && !IsLinkNotFound(err) {
				slog.Warn("recorded promotion link lookup failed", "connection_id", value.ConnectionID, "err", err)
				return fmt.Errorf("verify recorded promotion link: %w", err)
			}
			if err == nil && staticLinkIdentity(old) == record.LinkIdentity {
				return fmt.Errorf("promote_secondaries link identity changed for %s", value.ConnectionID)
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
	r.journal.Promotion = append(r.journal.Promotion, ownedStaticPromotion{ConnectionID: value.ConnectionID, LinkName: value.LinkName, LinkIndex: value.LinkIndex, LinkIdentity: value.LinkIdentity, Previous: previous})
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
		if staticLinkIdentity(link) != record.LinkIdentity {
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

func (r *OwnedStaticReconciler) ensureAddress(link netlink.Link, value ownedStaticObject, lifetime OwnedAddressLifetime) error {
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
		if found && (lifetimeDifference(installed.ValidLft, valid) > 2 || lifetimeDifference(installed.PreferedLft, preferred) > 2) {
			replace = true
		}
	}
	if found && !replace {
		return nil
	}
	return r.writeOwnedAddress(link, value, lifetime, replace)
}

func (r *OwnedStaticReconciler) findOwnedAddress(link netlink.Link, value ownedStaticObject, prefix netip.Prefix) (bool, bool, netlink.Addr, error) {
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
				return false, false, netlink.Addr{}, fmt.Errorf("foreign address uses %s with another prefix", prefix.Addr())
			}
			replace = true
			continue
		}
		found = true
		installed = address
	}
	return found, replace, installed, nil
}

func (r *OwnedStaticReconciler) writeOwnedAddress(link netlink.Link, value ownedStaticObject, lifetime OwnedAddressLifetime, replace bool) error {
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
		address.PreferedLft = min(address.ValidLft, leaseLifetimeSeconds(lifetime.PreferredUntil, now))
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

func sameStaticRoute(route netlink.Route, value ownedStaticObject) bool {
	return routeMatchesDestination(route, value.Destination) && route.Table == unix.RT_TABLE_MAIN && route.Priority == value.Metric &&
		route.Protocol == OwnedStaticRouteProtocol && route.LinkIndex == value.LinkIndex && route.Gw.Equal(ownedRouteGateway(value.Gateway))
}

func ownedRouteGateway(gateway string) net.IP {
	if gateway == "0.0.0.0" {
		return nil
	}
	return net.ParseIP(gateway)
}

func routeMatchesDestination(route netlink.Route, destination string) bool {
	if destination == "" {
		if route.Dst == nil {
			return true
		}
		ones, _ := route.Dst.Mask.Size()
		return ones == 0
	}
	return route.Dst != nil && route.Dst.String() == destination
}

func (r *OwnedStaticReconciler) recordedRoute(route netlink.Route, value ownedStaticObject) bool {
	for _, old := range r.journal.Objects {
		if old.ConnectionID != value.ConnectionID || old.Family != value.Family || old.Gateway == "" ||
			old.LinkIndex != value.LinkIndex || old.LinkIdentity != value.LinkIdentity {
			continue
		}
		if sameStaticRoute(route, old) {
			return true
		}
	}
	return false
}

func (r *OwnedStaticReconciler) ensureRoute(value ownedStaticObject) error {
	routes, err := netlink.RouteListFiltered(staticFamily(value.Family), &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("list main routes: %w", err)
	}
	found := false
	replace := false
	for _, route := range routes {
		if !routeMatchesDestination(route, value.Destination) || route.Priority != value.Metric {
			continue
		}
		if !r.recordedRoute(route, value) {
			if value.Destination == "" {
				return fmt.Errorf("default route metric %d conflicts with an unowned route", value.Metric)
			}
			return fmt.Errorf("route %s metric %d conflicts with an unowned route", value.Destination, value.Metric)
		}
		if sameStaticRoute(route, value) {
			found = true
		} else {
			replace = true
		}
	}
	if found {
		return nil
	}
	if !r.recorded(value) {
		if err := r.reserve(value); err != nil {
			return err
		}
	}
	route := &netlink.Route{LinkIndex: value.LinkIndex, Table: unix.RT_TABLE_MAIN, Family: staticFamily(value.Family), Gw: ownedRouteGateway(value.Gateway), Priority: value.Metric, Protocol: OwnedStaticRouteProtocol}
	if value.Gateway == "0.0.0.0" {
		route.Scope = netlink.SCOPE_LINK
	}
	if value.Destination != "" {
		_, destination, parseErr := net.ParseCIDR(value.Destination)
		if parseErr != nil {
			return fmt.Errorf("parse owned route destination %s: %w", value.Destination, parseErr)
		}
		route.Dst = destination
	}
	var writeErr error
	if replace {
		writeErr = netlink.RouteReplace(route)
	} else {
		writeErr = netlink.RouteAdd(route)
	}
	if writeErr != nil {
		slog.Warn("owned route write failed", "destination", value.Destination, "gateway", value.Gateway, "metric", value.Metric, "err", writeErr)
		return fmt.Errorf("write owned route: %w", writeErr)
	}
	return nil
}

func (r *OwnedStaticReconciler) pruneFamily(base ownedStaticObject, desired map[string]bool, retention RecordedRetention) error {
	old := append([]ownedStaticObject(nil), r.journal.Objects...)
	for _, value := range old {
		if value.ConnectionID != base.ConnectionID || value.Family != base.Family {
			continue
		}
		matchesLink := value.LinkIndex == base.LinkIndex && value.LinkIdentity == base.LinkIdentity && value.LinkName == base.LinkName
		if matchesLink && (desired[staticObjectKey(value)] || retention.All || retainedPrefix(value, retention.Prefixes)) {
			continue
		}
		if err := r.remove(value); err != nil {
			return err
		}
	}
	return nil
}

func retainedPrefix(value ownedStaticObject, prefixes map[netip.Prefix]bool) bool {
	if value.Prefix == "" || len(prefixes) == 0 {
		return false
	}
	prefix, err := netip.ParsePrefix(value.Prefix)
	return err == nil && prefixes[prefix]
}

func (r *OwnedStaticReconciler) remove(value ownedStaticObject) error {
	link, err := netlink.LinkByIndex(value.LinkIndex)
	if IsLinkNotFound(err) {
		return r.forget(value)
	}
	if err != nil {
		slog.Warn("recorded link lookup failed", "connection_id", value.ConnectionID, "err", err)
		return fmt.Errorf("find recorded link: %w", err)
	}
	if staticLinkIdentity(link) != value.LinkIdentity {
		return r.forget(value)
	}
	if link.Attrs().Name != value.LinkName {
		return fmt.Errorf("recorded link name changed for %s", value.ConnectionID)
	}
	if value.Prefix != "" {
		if err := removeStaticAddress(link, value); err != nil {
			return err
		}
	} else if err := removeStaticRoute(value); err != nil {
		return err
	}
	return r.forget(value)
}

func removeStaticAddress(link netlink.Link, value ownedStaticObject) error {
	prefix, err := netip.ParsePrefix(value.Prefix)
	if err != nil {
		slog.Warn("recorded prefix parse failed", "prefix", value.Prefix, "err", err)
		return fmt.Errorf("parse recorded prefix: %w", err)
	}
	addresses, err := netlink.AddrList(link, staticFamily(value.Family))
	if err != nil {
		slog.Warn("recorded address listing failed", "connection_id", value.ConnectionID, "err", err)
		return fmt.Errorf("list recorded addresses: %w", err)
	}
	for _, address := range addresses {
		if !addressMatches(address, prefix) {
			continue
		}
		if err := netlink.AddrDel(link, &address); err != nil {
			slog.Warn("recorded address delete failed", "prefix", value.Prefix, "err", err)
			return fmt.Errorf("delete recorded address: %w", err)
		}
	}
	return nil
}

func removeStaticRoute(value ownedStaticObject) error {
	routes, err := netlink.RouteListFiltered(staticFamily(value.Family), &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		slog.Warn("recorded route listing failed", "connection_id", value.ConnectionID, "err", err)
		return fmt.Errorf("list recorded routes: %w", err)
	}
	for _, route := range routes {
		if !sameStaticRoute(route, value) {
			continue
		}
		if err := netlink.RouteDel(&route); err != nil {
			slog.Warn("recorded route delete failed", "gateway", value.Gateway, "metric", value.Metric, "err", err)
			return fmt.Errorf("delete recorded route: %w", err)
		}
	}
	return nil
}

// PruneRemoved removes only recorded objects for deleted connections or families.
func (r *OwnedStaticReconciler) PruneRemoved(desired map[string]map[string]bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := append([]ownedStaticObject(nil), r.journal.Objects...)
	for _, value := range old {
		families := desired[value.ConnectionID]
		if families[value.Family] {
			continue
		}
		if err := r.remove(value); err != nil {
			return &OwnedStaticPruneError{ConnectionID: value.ConnectionID, LinkName: value.LinkName, Family: value.Family, Err: err}
		}
	}
	for len(r.journal.Promotion) != 0 {
		found := false
		for _, record := range r.journal.Promotion {
			if desired[record.ConnectionID]["ipv4"] {
				continue
			}
			if err := r.restorePromotion(record.ConnectionID); err != nil {
				return &OwnedStaticPruneError{ConnectionID: record.ConnectionID, LinkName: record.LinkName, Family: "ipv4", Err: err}
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
