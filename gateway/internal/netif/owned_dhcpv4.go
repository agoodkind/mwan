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
	"slices"
	"sync"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/installfile"
)

// OwnedDHCPv4RouteProtocol identifies routes installed from a DHCPv4 lease.
const OwnedDHCPv4RouteProtocol = 187

type ownedDHCPv4Object struct {
	ConsumerID     string `json:"consumer_id"`
	ConnectionID   string `json:"connection_id"`
	LinkName       string `json:"link_name"`
	LinkIndex      int    `json:"link_index"`
	LinkIdentity   string `json:"link_identity"`
	TableID        int    `json:"table_id"`
	Prefix         string `json:"prefix,omitempty"`
	Destination    string `json:"destination,omitempty"`
	Gateway        string `json:"gateway,omitempty"`
	Metric         int    `json:"metric,omitempty"`
	LegacyProtocol int    `json:"legacy_protocol,omitempty"`
}

type ownedDHCPv4Journal struct {
	BootID        string                 `json:"boot_id"`
	Objects       []ownedDHCPv4Object    `json:"objects"`
	Promotion     []ownedDHCPv4Promotion `json:"promotion,omitempty"`
	LegacyChecked []string               `json:"legacy_checked,omitempty"`
}

type ownedDHCPv4Promotion struct {
	ConsumerID   string `json:"consumer_id"`
	ConnectionID string `json:"connection_id"`
	LinkName     string `json:"link_name"`
	LinkIndex    int    `json:"link_index"`
	LinkIdentity string `json:"link_identity"`
	Previous     string `json:"previous"`
}

// OwnedDHCPv4Reconciler records exact DHCPv4 objects before kernel writes.
type OwnedDHCPv4Reconciler struct {
	mu      sync.Mutex
	path    string
	journal ownedDHCPv4Journal
}

// NewOwnedDHCPv4Reconciler reads a durable DHCPv4 ownership journal.
func NewOwnedDHCPv4Reconciler(path string) (*OwnedDHCPv4Reconciler, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("DHCPv4 ownership path must be absolute")
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		slog.Warn("DHCPv4 boot ID read failed", "err", err)
		return nil, fmt.Errorf("read boot ID: %w", err)
	}
	r := &OwnedDHCPv4Reconciler{
		mu: sync.Mutex{}, path: path,
		journal: ownedDHCPv4Journal{BootID: string(bootID), Objects: nil, Promotion: nil, LegacyChecked: nil},
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		slog.Warn("DHCPv4 ownership journal read failed", "path", path, "err", err)
		return nil, fmt.Errorf("read DHCPv4 ownership journal: %w", err)
	}
	if err := json.Unmarshal(data, &r.journal); err != nil {
		slog.Warn("DHCPv4 ownership journal decode failed", "path", path, "err", err)
		return nil, fmt.Errorf("decode DHCPv4 ownership journal: %w", err)
	}
	if r.journal.BootID != string(bootID) {
		r.journal = ownedDHCPv4Journal{BootID: string(bootID), Objects: nil, Promotion: nil, LegacyChecked: nil}
	}
	return r, nil
}

func (r *OwnedDHCPv4Reconciler) save() error {
	data, err := json.Marshal(r.journal)
	if err != nil {
		slog.Warn("DHCPv4 ownership journal encode failed", "path", r.path, "err", err)
		return fmt.Errorf("encode DHCPv4 ownership journal: %w", err)
	}
	if _, err := installfile.Write(r.path, append(data, '\n'), 0o600); err != nil {
		slog.Warn("DHCPv4 ownership journal write failed", "path", r.path, "err", err)
		return fmt.Errorf("write DHCPv4 ownership journal: %w", err)
	}
	directory, err := os.Open(filepath.Dir(r.path))
	if err != nil {
		slog.Warn("DHCPv4 ownership directory open failed", "path", r.path, "err", err)
		return fmt.Errorf("open DHCPv4 ownership directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		slog.Warn("DHCPv4 ownership directory sync failed", "path", r.path, "err", err)
		return fmt.Errorf("sync DHCPv4 ownership directory: %w", err)
	}
	return nil
}

func sameDHCPv4Owner(a, b ownedDHCPv4Object) bool {
	return a.ConsumerID == b.ConsumerID && a.ConnectionID == b.ConnectionID
}

func sameDHCPv4Link(a, b ownedDHCPv4Object) bool {
	return a.LinkIndex == b.LinkIndex && a.LinkIdentity == b.LinkIdentity && a.LinkName == b.LinkName
}

func sameDHCPv4Object(a, b ownedDHCPv4Object) bool {
	return sameDHCPv4Owner(a, b) && sameDHCPv4Link(a, b) && a.TableID == b.TableID &&
		a.Prefix == b.Prefix && a.Destination == b.Destination && a.Gateway == b.Gateway &&
		a.Metric == b.Metric && a.LegacyProtocol == b.LegacyProtocol
}

func dhcpv4Owner(consumerID, connectionID string, tableID int) ownedDHCPv4Object {
	var value ownedDHCPv4Object
	value.ConsumerID = consumerID
	value.ConnectionID = connectionID
	value.TableID = tableID
	return value
}

func (r *OwnedDHCPv4Reconciler) recorded(value ownedDHCPv4Object) bool {
	for _, old := range r.journal.Objects {
		if sameDHCPv4Object(old, value) {
			return true
		}
	}
	return false
}

func (r *OwnedDHCPv4Reconciler) reserve(value ownedDHCPv4Object) error {
	if r.recorded(value) {
		return nil
	}
	r.journal.Objects = append(r.journal.Objects, value)
	if err := r.save(); err != nil {
		r.journal.Objects = r.journal.Objects[:len(r.journal.Objects)-1]
		return err
	}
	return nil
}

func (r *OwnedDHCPv4Reconciler) forget(value ownedDHCPv4Object) error {
	for i, old := range r.journal.Objects {
		if !sameDHCPv4Object(old, value) {
			continue
		}
		previous := slices.Clone(r.journal.Objects)
		r.journal.Objects = append(r.journal.Objects[:i], r.journal.Objects[i+1:]...)
		if err := r.save(); err != nil {
			r.journal.Objects = previous
			return err
		}
		return nil
	}
	return nil
}

// Reconcile installs a bound lease and withdraws recorded objects for nil or expired leases.
// Renewing and rebinding preserve the current kernel assignment.
func (r *OwnedDHCPv4Reconciler) Reconcile(ctx context.Context, consumerID, connectionID, iface string, tableID, metric int, lease *LeaseInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		slog.Warn("DHCPv4 ownership reconcile canceled", "err", err)
		return fmt.Errorf("reconcile DHCPv4 ownership: %w", err)
	}
	if consumerID == "" || connectionID == "" || iface == "" || tableID <= 0 || metric < 0 {
		return errors.New("invalid DHCPv4 ownership input")
	}
	if lease != nil && (lease.State == LeaseRenewing || lease.State == LeaseRebinding) {
		return nil
	}
	if lease != nil && lease.State != LeaseBound && lease.State != LeaseExpired {
		return fmt.Errorf("unsupported DHCPv4 reconciliation state %s", lease.State)
	}
	base := dhcpv4Owner(consumerID, connectionID, tableID)
	var desired []ownedDHCPv4Object
	if lease != nil && lease.State == LeaseBound {
		var err error
		base, desired, err = r.installBound(iface, metric, base, lease)
		if err != nil {
			return err
		}
	}
	for _, old := range slices.Clone(r.journal.Objects) {
		if !sameDHCPv4Owner(old, base) {
			continue
		}
		if slices.ContainsFunc(desired, func(value ownedDHCPv4Object) bool { return sameDHCPv4Object(old, value) }) {
			continue
		}
		if err := r.remove(old); err != nil {
			return err
		}
	}
	if lease == nil || lease.State == LeaseExpired {
		return r.restorePromotion(base)
	}
	return nil
}

func (r *OwnedDHCPv4Reconciler) installBound(
	iface string, metric int, base ownedDHCPv4Object, lease *LeaseInfo,
) (ownedDHCPv4Object, []ownedDHCPv4Object, error) {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		slog.Warn("DHCPv4 link lookup failed", "iface", iface, "err", err)
		return base, nil, fmt.Errorf("find DHCPv4 link: %w", err)
	}
	base.LinkName = iface
	base.LinkIndex = link.Attrs().Index
	base.LinkIdentity = LinkOwnershipIdentity(link)
	desired, err := dhcpv4Desired(base, metric, lease)
	if err != nil {
		return base, nil, err
	}
	if err := r.adoptLegacy(link, base, desired, metric); err != nil {
		return base, nil, err
	}
	if err := r.checkConflicts(link, desired); err != nil {
		return base, nil, err
	}
	if err := r.ensurePromotion(base); err != nil {
		return base, nil, err
	}
	for _, value := range desired {
		if value.Prefix == "" {
			continue
		}
		if err := r.ensureAddress(link, value); err != nil {
			return base, nil, err
		}
	}
	for _, value := range desired {
		if value.Prefix != "" {
			continue
		}
		if err := r.ensureRoute(value); err != nil {
			return base, nil, err
		}
	}
	return base, desired, nil
}

func (r *OwnedDHCPv4Reconciler) adoptLegacy(
	link netlink.Link, base ownedDHCPv4Object, desired []ownedDHCPv4Object, metric int,
) error {
	if base.ConsumerID != "oobv4" && base.ConsumerID != "mainv4" {
		return nil
	}
	if slices.Contains(r.journal.LegacyChecked, base.ConsumerID) {
		return nil
	}
	addressValue := desired[0]
	adoptAddress, err := r.legacyAddress(link, addressValue)
	if err != nil {
		return err
	}
	legacyRoute, err := r.legacyDefault(base, metric)
	if err != nil {
		return err
	}
	if adoptAddress {
		if err := r.reserve(addressValue); err != nil {
			return err
		}
	}
	if legacyRoute != nil {
		if err := r.reserve(*legacyRoute); err != nil {
			return err
		}
	}
	r.journal.LegacyChecked = append(r.journal.LegacyChecked, base.ConsumerID)
	if err := r.save(); err != nil {
		r.journal.LegacyChecked = r.journal.LegacyChecked[:len(r.journal.LegacyChecked)-1]
		return err
	}
	return nil
}

func (r *OwnedDHCPv4Reconciler) legacyAddress(link netlink.Link, addressValue ownedDHCPv4Object) (bool, error) {
	prefix, err := netip.ParsePrefix(addressValue.Prefix)
	if err != nil {
		slog.Warn("DHCPv4 legacy address parse failed", "prefix", addressValue.Prefix, "err", err)
		return false, fmt.Errorf("parse DHCPv4 legacy address %s: %w", addressValue.Prefix, err)
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		slog.Warn("DHCPv4 legacy address listing failed", "link", link.Attrs().Name, "err", err)
		return false, fmt.Errorf("list DHCPv4 legacy addresses: %w", err)
	}
	adoptAddress := false
	for _, address := range addresses {
		if !address.IP.Equal(net.IP(prefix.Addr().AsSlice())) {
			// The old role retained an expired address, but did not record its identity.
			if address.Scope == int(netlink.SCOPE_UNIVERSE) && !address.IP.IsLinkLocalUnicast() {
				return false, fmt.Errorf("unidentified IPv4 address %s on %s during DHCPv4 migration", address.IPNet, link.Attrs().Name)
			}
			continue
		}
		if !addressMatches(address, prefix) {
			return false, fmt.Errorf("foreign address uses %s", prefix.Addr())
		}
		adoptAddress = !r.recorded(addressValue)
	}
	return adoptAddress, nil
}

func (r *OwnedDHCPv4Reconciler) legacyDefault(base ownedDHCPv4Object, metric int) (*ownedDHCPv4Object, error) {
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: base.TableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		slog.Warn("DHCPv4 legacy route listing failed", "table", base.TableID, "err", err)
		return nil, fmt.Errorf("list DHCPv4 legacy routes: %w", err)
	}
	var legacyRoute *ownedDHCPv4Object
	for _, route := range routes {
		if route.Priority != metric || !dhcpv4DefaultRoute(route) {
			continue
		}
		if r.recordedRoute(route, base) {
			continue
		}
		if legacyRoute != nil {
			return nil, fmt.Errorf("multiple DHCPv4 defaults in table %d metric %d", base.TableID, metric)
		}
		gateway := route.Gw.To4()
		if route.Protocol != unix.RTPROT_BOOT || route.LinkIndex != base.LinkIndex || gateway == nil || len(route.MultiPath) != 0 {
			return nil, fmt.Errorf("foreign default in table %d metric %d", base.TableID, metric)
		}
		value := base
		value.Destination = "0.0.0.0/0"
		value.Gateway = gateway.String()
		value.Metric = metric
		value.LegacyProtocol = int(route.Protocol)
		legacyRoute = &value
	}
	return legacyRoute, nil
}

func dhcpv4DefaultRoute(route netlink.Route) bool {
	if route.Dst == nil {
		return true
	}
	bits, size := route.Dst.Mask.Size()
	return size == 32 && bits == 0
}

// PruneConsumer withdraws every recorded assignment for one consumer.
func (r *OwnedDHCPv4Reconciler) PruneConsumer(ctx context.Context, consumerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		slog.Warn("DHCPv4 ownership prune canceled", "err", err)
		return fmt.Errorf("prune DHCPv4 ownership: %w", err)
	}
	if consumerID == "" {
		return errors.New("missing DHCPv4 consumer ID")
	}
	for _, old := range slices.Clone(r.journal.Objects) {
		if old.ConsumerID != consumerID {
			continue
		}
		if err := r.remove(old); err != nil {
			return err
		}
	}
	for _, promotion := range slices.Clone(r.journal.Promotion) {
		if promotion.ConsumerID != consumerID {
			continue
		}
		if err := r.restorePromotion(dhcpv4Owner(promotion.ConsumerID, promotion.ConnectionID, 0)); err != nil {
			return err
		}
	}
	return nil
}

func (r *OwnedDHCPv4Reconciler) checkConflicts(link netlink.Link, desired []ownedDHCPv4Object) error {
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		slog.Warn("DHCPv4 conflict address listing failed", "link", link.Attrs().Name, "err", err)
		return fmt.Errorf("list DHCPv4 addresses: %w", err)
	}
	for _, value := range desired {
		if value.Prefix == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value.Prefix)
		if err != nil {
			return fmt.Errorf("parse DHCPv4 desired prefix %s: %w", value.Prefix, err)
		}
		for _, address := range addresses {
			if !address.IP.Equal(net.IP(prefix.Addr().AsSlice())) {
				continue
			}
			old := value
			if !addressMatches(address, prefix) {
				old.Prefix = address.IPNet.String()
			}
			if !r.recorded(old) {
				return fmt.Errorf("foreign address uses %s", prefix.Addr())
			}
		}
	}
	for _, value := range desired {
		if value.Prefix != "" {
			continue
		}
		routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: value.TableID}, netlink.RT_FILTER_TABLE)
		if err != nil {
			return fmt.Errorf("list DHCPv4 routes: %w", err)
		}
		for _, route := range routes {
			if route.Priority == value.Metric && dhcpv4RouteMatches(route, value) && !r.recordedRoute(route, value) {
				return fmt.Errorf("foreign route conflicts with %s metric %d in table %d", value.Destination, value.Metric, value.TableID)
			}
		}
	}
	return nil
}

func (r *OwnedDHCPv4Reconciler) ensurePromotion(base ownedDHCPv4Object) error {
	for i, record := range r.journal.Promotion {
		if record.ConsumerID != base.ConsumerID || record.ConnectionID != base.ConnectionID {
			continue
		}
		if record.LinkIndex == base.LinkIndex && record.LinkIdentity == base.LinkIdentity && record.LinkName == base.LinkName {
			return writePromotion(base.LinkName, "1\n")
		}
		old, err := netlink.LinkByIndex(record.LinkIndex)
		if err != nil && !IsLinkNotFound(err) {
			slog.Warn("old DHCPv4 link verification failed", "index", record.LinkIndex, "err", err)
			return fmt.Errorf("verify old DHCPv4 link: %w", err)
		}
		if err == nil && LinkMatchesIdentity(old, record.LinkIndex, record.LinkIdentity) {
			return errors.New("DHCPv4 ownership moved while old link exists")
		}
		if err := r.forgetPromotion(i); err != nil {
			return err
		}
		break
	}
	previous, err := readPromotion(base.LinkName)
	if err != nil {
		return err
	}
	r.journal.Promotion = append(r.journal.Promotion, ownedDHCPv4Promotion{
		ConsumerID: base.ConsumerID, ConnectionID: base.ConnectionID, LinkName: base.LinkName,
		LinkIndex: base.LinkIndex, LinkIdentity: base.LinkIdentity, Previous: previous,
	})
	if err := r.save(); err != nil {
		r.journal.Promotion = r.journal.Promotion[:len(r.journal.Promotion)-1]
		return err
	}
	return writePromotion(base.LinkName, "1\n")
}

func (r *OwnedDHCPv4Reconciler) restorePromotion(base ownedDHCPv4Object) error {
	for i, record := range r.journal.Promotion {
		if record.ConsumerID != base.ConsumerID || record.ConnectionID != base.ConnectionID {
			continue
		}
		link, err := netlink.LinkByIndex(record.LinkIndex)
		if IsLinkNotFound(err) {
			return r.forgetPromotion(i)
		}
		if err != nil {
			slog.Warn("DHCPv4 promotion link lookup failed", "index", record.LinkIndex, "err", err)
			return fmt.Errorf("find DHCPv4 promotion link: %w", err)
		}
		if !LinkMatchesIdentity(link, record.LinkIndex, record.LinkIdentity) {
			return r.forgetPromotion(i)
		}
		if link.Attrs().Name != record.LinkName {
			return errors.New("recorded DHCPv4 promotion link name changed")
		}
		current, err := readPromotion(record.LinkName)
		if err != nil {
			return err
		}
		if current != "1\n" && current != record.Previous {
			return errors.New("DHCPv4 promotion changed outside MWAN")
		}
		if current != record.Previous {
			if err := writePromotion(record.LinkName, record.Previous); err != nil {
				return err
			}
		}
		return r.forgetPromotion(i)
	}
	return nil
}

func (r *OwnedDHCPv4Reconciler) forgetPromotion(index int) error {
	previous := slices.Clone(r.journal.Promotion)
	r.journal.Promotion = append(r.journal.Promotion[:index], r.journal.Promotion[index+1:]...)
	if err := r.save(); err != nil {
		r.journal.Promotion = previous
		return err
	}
	return nil
}

func dhcpv4Desired(base ownedDHCPv4Object, metric int, lease *LeaseInfo) ([]ownedDHCPv4Object, error) {
	address, ok := netip.AddrFromSlice(lease.IP.To4())
	if !ok || lease.PrefixLen < 0 || lease.PrefixLen > 32 {
		return nil, errors.New("invalid DHCPv4 lease address")
	}
	value := base
	value.Prefix = netip.PrefixFrom(address, lease.PrefixLen).String()
	desired := []ownedDHCPv4Object{value}
	connected := netip.PrefixFrom(address, lease.PrefixLen).Masked()
	routes := lease.Routes
	if len(routes) == 0 && lease.Gateway != nil {
		routes = []LeaseRoute{{Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, Gateway: lease.Gateway}}
	}
	for _, route := range routes {
		value, skip, err := dhcpv4RouteObject(base, metric, connected, route)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		for _, previous := range desired {
			if previous.Prefix == "" && previous.Destination == value.Destination && previous.Metric == value.Metric && previous.Gateway != value.Gateway {
				return nil, fmt.Errorf("conflicting DHCPv4 gateways for %s", value.Destination)
			}
		}
		if !slices.ContainsFunc(desired, func(old ownedDHCPv4Object) bool { return sameDHCPv4Object(old, value) }) {
			desired = append(desired, value)
		}
	}
	slices.SortStableFunc(desired, func(a, b ownedDHCPv4Object) int {
		if a.Prefix != "" && b.Prefix != "" {
			return 0
		}
		if a.Prefix != "" {
			return -1
		}
		if b.Prefix != "" {
			return 1
		}
		if a.Gateway == "0.0.0.0" && b.Gateway != "0.0.0.0" {
			return -1
		}
		if b.Gateway == "0.0.0.0" && a.Gateway != "0.0.0.0" {
			return 1
		}
		aDestination := netip.MustParsePrefix(a.Destination)
		bDestination := netip.MustParsePrefix(b.Destination)
		return bDestination.Bits() - aDestination.Bits()
	})
	return desired, nil
}

func dhcpv4RouteObject(base ownedDHCPv4Object, metric int, connected netip.Prefix, route LeaseRoute) (ownedDHCPv4Object, bool, error) {
	if route.Destination == nil || route.Gateway.To4() == nil {
		return base, false, errors.New("invalid DHCPv4 lease route")
	}
	bits, size := route.Destination.Mask.Size()
	if size != 32 || route.Destination.IP.To4() == nil {
		return base, false, errors.New("invalid DHCPv4 route destination")
	}
	gateway, _ := netip.AddrFromSlice(route.Gateway.To4())
	if gateway.IsMulticast() {
		return base, false, errors.New("invalid DHCPv4 route gateway")
	}
	destination, _ := netip.AddrFromSlice(route.Destination.IP.To4())
	prefix := netip.PrefixFrom(destination, bits).Masked()
	if prefix == connected {
		return base, true, nil
	}
	value := base
	value.Destination = prefix.String()
	value.Gateway = gateway.String()
	value.Metric = metric
	return value, false, nil
}

func (r *OwnedDHCPv4Reconciler) ensureAddress(link netlink.Link, value ownedDHCPv4Object) error {
	prefix, err := netip.ParsePrefix(value.Prefix)
	if err != nil {
		return fmt.Errorf("parse DHCPv4 address prefix %s: %w", value.Prefix, err)
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		slog.Warn("DHCPv4 link address listing failed", "link", link.Attrs().Name, "err", err)
		return fmt.Errorf("list DHCPv4 addresses: %w", err)
	}
	found := false
	replace := false
	for _, address := range addresses {
		if !address.IP.Equal(net.IP(prefix.Addr().AsSlice())) {
			continue
		}
		if addressMatches(address, prefix) {
			found = true
			continue
		}
		old := value
		old.Prefix = address.IPNet.String()
		if !r.recorded(old) {
			return fmt.Errorf("foreign address uses %s", prefix.Addr())
		}
		replace = true
	}
	if found && !r.recorded(value) {
		return fmt.Errorf("address %s exists without DHCP ownership", value.Prefix)
	}
	if found {
		return nil
	}
	if err := r.reserve(value); err != nil {
		return err
	}
	address, err := netlink.ParseAddr(value.Prefix)
	if err != nil {
		return fmt.Errorf("parse DHCPv4 address %s: %w", value.Prefix, err)
	}
	if replace {
		err = netlink.AddrReplace(link, address)
	} else {
		err = netlink.AddrAdd(link, address)
	}
	if err != nil {
		return fmt.Errorf("write DHCPv4 address %s: %w", value.Prefix, err)
	}
	return nil
}

func dhcpv4RouteMatches(route netlink.Route, value ownedDHCPv4Object) bool {
	destination, err := netip.ParsePrefix(value.Destination)
	if err != nil {
		return false
	}
	if route.Dst == nil {
		return destination.Bits() == 0
	}
	return route.Dst.String() == destination.String()
}

func (r *OwnedDHCPv4Reconciler) recordedRoute(route netlink.Route, value ownedDHCPv4Object) bool {
	for _, old := range r.journal.Objects {
		if old.Prefix != "" || !sameDHCPv4Owner(old, value) || !sameDHCPv4Link(old, value) || old.TableID != value.TableID ||
			old.Metric != value.Metric || !dhcpv4RouteMatches(route, old) {
			continue
		}
		if route.Protocol == dhcpv4RouteProtocol(old) && route.LinkIndex == old.LinkIndex &&
			route.Gw.Equal(dhcpv4Gateway(old.Gateway)) {
			return true
		}
	}
	return false
}

func dhcpv4Gateway(gateway string) net.IP {
	if gateway == "0.0.0.0" {
		return nil
	}
	return net.ParseIP(gateway)
}

func dhcpv4RouteProtocol(value ownedDHCPv4Object) netlink.RouteProtocol {
	if value.LegacyProtocol != 0 {
		return netlink.RouteProtocol(value.LegacyProtocol)
	}
	return OwnedDHCPv4RouteProtocol
}

func (r *OwnedDHCPv4Reconciler) ensureRoute(value ownedDHCPv4Object) error {
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: value.TableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("list DHCPv4 routes: %w", err)
	}
	found := false
	replace := false
	for _, route := range routes {
		if route.Priority != value.Metric || !dhcpv4RouteMatches(route, value) {
			continue
		}
		if !r.recordedRoute(route, value) {
			return fmt.Errorf("foreign route conflicts with %s metric %d in table %d", value.Destination, value.Metric, value.TableID)
		}
		if route.Protocol == OwnedDHCPv4RouteProtocol && route.Gw.Equal(dhcpv4Gateway(value.Gateway)) {
			found = true
		} else {
			replace = true
		}
	}
	if found {
		return nil
	}
	if err := r.reserve(value); err != nil {
		return err
	}
	destination, err := netip.ParsePrefix(value.Destination)
	if err != nil {
		slog.Warn("DHCPv4 route destination parse failed", "destination", value.Destination, "err", err)
		return fmt.Errorf("parse DHCPv4 route destination %s: %w", value.Destination, err)
	}
	route := &netlink.Route{
		LinkIndex: value.LinkIndex, Table: value.TableID, Family: unix.AF_INET,
		Priority: value.Metric, Protocol: OwnedDHCPv4RouteProtocol, Gw: dhcpv4Gateway(value.Gateway),
	}
	if route.Gw == nil {
		route.Scope = netlink.SCOPE_LINK
	}
	if destination.Bits() != 0 {
		_, route.Dst, _ = net.ParseCIDR(value.Destination)
	}
	if replace {
		err = netlink.RouteReplace(route)
	} else {
		err = netlink.RouteAdd(route)
	}
	if err != nil {
		return fmt.Errorf("write DHCPv4 route %s: %w", value.Destination, err)
	}
	return nil
}

func (r *OwnedDHCPv4Reconciler) remove(value ownedDHCPv4Object) error {
	link, err := netlink.LinkByIndex(value.LinkIndex)
	if IsLinkNotFound(err) {
		return r.forget(value)
	}
	if err != nil {
		slog.Warn("DHCPv4 link lookup for removal failed", "index", value.LinkIndex, "err", err)
		return fmt.Errorf("find recorded DHCPv4 link: %w", err)
	}
	if !LinkMatchesIdentity(link, value.LinkIndex, value.LinkIdentity) {
		return r.forget(value)
	}
	if link.Attrs().Name != value.LinkName {
		return errors.New("recorded DHCPv4 link name changed")
	}
	if value.Prefix != "" {
		if err := removeDHCPv4Address(link, value); err != nil {
			return err
		}
	} else if err := removeDHCPv4Route(value); err != nil {
		return err
	}
	return r.forget(value)
}

func removeDHCPv4Address(link netlink.Link, value ownedDHCPv4Object) error {
	prefix, err := netip.ParsePrefix(value.Prefix)
	if err != nil {
		return fmt.Errorf("parse recorded DHCPv4 address %s: %w", value.Prefix, err)
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		return fmt.Errorf("list recorded DHCPv4 addresses: %w", err)
	}
	for _, address := range addresses {
		if !addressMatches(address, prefix) {
			continue
		}
		if err := netlink.AddrDel(link, &address); err != nil {
			slog.Warn("DHCPv4 address removal failed", "prefix", value.Prefix, "err", err)
			return fmt.Errorf("remove DHCPv4 address %s: %w", value.Prefix, err)
		}
	}
	return nil
}

func removeDHCPv4Route(value ownedDHCPv4Object) error {
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: value.TableID}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("list recorded DHCPv4 routes: %w", err)
	}
	for _, route := range routes {
		if route.Priority != value.Metric || !dhcpv4RouteMatches(route, value) ||
			route.Protocol != dhcpv4RouteProtocol(value) || route.LinkIndex != value.LinkIndex ||
			!route.Gw.Equal(dhcpv4Gateway(value.Gateway)) {
			continue
		}
		if err := netlink.RouteDel(&route); err != nil {
			slog.Warn("DHCPv4 route removal failed", "destination", value.Destination, "err", err)
			return fmt.Errorf("remove DHCPv4 route %s: %w", value.Destination, err)
		}
	}
	return nil
}
