// Package wanconfig projects the configuration the gateway daemon loaded onto
// the model it describes itself with, so the management surface serves what
// the running process holds rather than a file on disk. The projection turns
// a Gateway value into the path-value items the publishing binding writes.
//
// The package carries the platform constraint of the gateway it describes:
// only the linux daemon loads this configuration and only its sysrepo
// binding publishes it, so the freebsd router build never reaches this code.
package wanconfig

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/interfaceintent"
)

// Item is one path-value pair in the published tree. It mirrors the
// publishing binding's item so the projection can be built and tested on
// every platform; the linux publisher converts it on the way out.
type Item struct {
	Path  string
	Value string
}

// Member configures one connection with a network interface, steering tier,
// health policy, and optional translation policy.
type Member struct {
	// Name is the connection ID used by probe policies and translation instances.
	Name         string
	ProviderName string
	// Iface is the interface entry key.
	Iface string
	// Tier is the steering tier the configuration assigns; lower is preferred.
	Tier uint8
	// Weight is the member's share of its tier, at least one. The schema bounds
	// it there, so a zero would be refused by the datastore rather than
	// published.
	Weight uint16
	// ProbePolicy is the name of the health policy that decides the member's
	// state, or empty when the daemon runs no probe for it.
	ProbePolicy     string
	TranslationV4   *config.IPv4Translation
	TranslationV6   *config.IPv6Translation
	TranslationIDV4 uint32
	TranslationIDV6 uint32
	// TableID, FwMark, FwMarkPrio, and FromPrio are the provider's routing
	// numbers: the table holding its default route, the firewall mark that
	// selects that table, and the priorities of the two policy rules. The
	// model ranges the table and the mark from 1, so a zero is refused rather
	// than published.
	TableID    uint32
	FwMark     uint32
	FwMarkPrio uint32
	FromPrio   uint32
	// V4Source is the provider's static IPv4 link address exactly as loaded,
	// an address or a prefix, or empty when the provider carries none.
	V4Source string
	// ForcedDSCP is the DSCP value that forces a new flow onto the provider,
	// or zero when it carries none. The model ranges it from 1, so zero is
	// free to mean absent.
	ForcedDSCP uint8
	// Health is the provider's probe as loaded, or nil when the provider
	// carries no health container.
	Health *ProbeSettings
	// LinkFiles says who writes the member's unit files, as the link-files
	// leaf spells it: rendered or hand-authored. Empty publishes nothing,
	// which is what a caller holding no network configuration passes.
	LinkFiles string
}

// ProbeSettings is one provider's health probe as the loaded configuration
// holds it. A disabled probe keeps whichever settings its file carries, so
// each count and the interval is a pointer: nil is a leaf the file left out,
// and publishes nothing, while zero is a value the model accepts.
type ProbeSettings struct {
	Enabled              bool
	PingCount            *uint8
	SuccessThreshold     *uint8
	FailureThreshold     *uint8
	RecoveryThreshold    *uint8
	CheckIntervalSeconds *uint32
	TargetsV4            []netip.Addr
	TargetsV6            []netip.Addr
	HTTPURLs             []string
}

// GroupSettings are the network values that belong to the member group as a
// whole rather than to one provider.
type GroupSettings struct {
	// ReservedTables are the routing tables no provider may take.
	ReservedTables []uint32
	// InternalPrefix is the internal half of every member's translation.
	InternalPrefix netip.Prefix
	// OpnsenseEdgeV6 and MwanbrEdgeV6 are the router's and the gateway's
	// addresses on the internal link.
	OpnsenseEdgeV6 netip.Addr
	MwanbrEdgeV6   netip.Addr
	// InternalNetV4 is the internal IPv4 network routed into every member's
	// table.
	InternalNetV4 netip.Prefix
	// ProbeTimeoutMillis is how long one probe attempt may take. The model
	// ranges it from 1, so zero publishes nothing.
	ProbeTimeoutMillis uint32
}

// WatchdogSettings is the rollback watchdog's policy as the publishing
// daemon's configuration carries it: the thresholds that decide when a
// deploy is judged and rolled back, and the targets its probes exercise.
// Present gates publishing, so a host whose configuration carries no
// watchdog section publishes nothing under the container.
type WatchdogSettings struct {
	Present                      bool
	DeployWindowMinutes          uint16
	ConnectivityTimeoutSeconds   uint16
	CheckIntervalHealthySeconds  uint16
	CheckIntervalDegradedSeconds uint16
	PostRollbackGraceSeconds     uint16
	AlertCooldownSeconds         uint16
	DeployGracePeriodSeconds     uint16
	MaxRollbackAttempts          uint8
	SnapshotHealthyThreshold     uint16
	MaxKnownGoodSnapshots        uint8
	// PingTargets are the addresses the watchdog pings, both families.
	PingTargets []netip.Addr
}

// OOBSettings is the out-of-band access policy as the publishing daemon's
// configuration carries it. Each family publishes only when its module
// config is present in the daemon's role.
type OOBSettings struct {
	V6Present         bool
	V6Iface           string
	V6Addr            netip.Addr
	V6TableID         uint32
	ManageSLAACRule   bool
	SLAACRulePriority uint32
	V4Present         bool
	V4Iface           string
	V4TableID         uint32
}

// TapSettings is the tunnel tap as the publishing daemon's configuration
// carries it: the unit whose journal is tailed and the patterns whose
// entries are downgraded.
type TapSettings struct {
	Present           bool
	Unit              string
	DowngradePatterns []string
}

// DaemonSettings are the daemon settings no published model covers,
// published under the local module's daemon container.
type DaemonSettings struct {
	Watchdog WatchdogSettings
	OOB      OOBSettings
	Tap      TapSettings
}

// Gateway is the loaded configuration the surface publishes.
type Gateway struct {
	Firewall           firewall.Config
	PinnedConnectionID string
	ConnectionIDs      map[string]connectionid.ID
	Connections        []interfaceintent.Connection
	// InternalIface is the link toward the router behind the gateway. It is
	// published as an interface entry with both families enabled, because
	// the daemon routes both families over it.
	InternalIface string
	// HashMode is how a new connection is assigned to a member of the active
	// tier. It is published because the daemon acts on it, so a reader of the
	// tree sees the rule the kernel is running under. Empty publishes nothing,
	// which is what a role that runs no steering module carries.
	HashMode string
	// Group carries the rest of the steering group's network values. A value
	// left zero publishes nothing, which is what a caller holding no network
	// configuration passes.
	Group GroupSettings
	// Members are the steering members in a stable order.
	Members []Member
	// Daemon carries the settings published under the local module's
	// daemon container.
	Daemon DaemonSettings
}

// OwnedPaths are the subtrees the daemon owns in the configuration
// datastore. A publish replaces them wholesale, so an entry a retired
// configuration wrote cannot survive a restart.
var OwnedPaths = []string{
	interfacesPath,
	natPath,
	daemonPath,
}

const (
	interfacesPath    = "/ietf-interfaces:interfaces"
	steeringGroupPath = interfacesPath + "/goodkind-mwan-steering:steering-group"
	natPath           = "/ietf-nat:nat"
	daemonPath        = "/goodkind-mwan-steering:daemon"

	// natPolicyID is the single policy each instance carries.
	natPolicyID = 1

	boolTrue = "true"

	// maxDSCP is the largest value a six-bit DSCP field holds.
	maxDSCP = 63

	// maxVLANID is the largest tag the model's vlan/id leaf accepts.
	maxVLANID = 4094

	// The two values the link-files leaf takes.
	linkFilesRendered     = "rendered"
	linkFilesHandAuthored = "hand-authored"

	// itemsPerMember and itemsForGroup size the item slice for a member with
	// a full probe and a group carrying every value; they are capacity hints,
	// not limits.
	itemsPerMember = 28
	itemsForGroup  = 16
	// itemsPerLink sizes the items a rendered link with both families and a
	// delegation publishes.
	itemsPerLink = 24
)

// ErrInvalidGateway means the Gateway cannot be published as given. The
// message names the member and field.
var ErrInvalidGateway = errors.New("wanconfig: invalid gateway")

// ConfigItems returns the items that describe g in the model, in a stable
// order, or an error when g cannot be published faithfully: a member or the
// internal link with no name, a name that cannot be quoted into a path, a
// duplicate link, a translation pair with only one prefix, or a value outside
// the range or address family its leaf accepts.
func ConfigItems(g Gateway) ([]Item, error) {
	if err := validate(g); err != nil {
		return nil, err
	}

	items := make([]Item, 0, itemsPerMember*len(g.Members)+itemsForGroup)
	for _, connection := range g.Connections {
		items = append(items, connectionItems(connection, g.ConnectionIDs)...)
	}
	for _, member := range g.Members {
		items = append(items, steeringItems(member)...)
		items = append(items, wanItems(member)...)
	}
	items = append(items, steeringGroupItems(g)...)
	items = append(items, firewallItems(g)...)
	for _, member := range g.Members {
		items = append(items, natInstanceItems(member)...)
	}
	items = append(items, daemonItems(g.Daemon)...)
	return items, nil
}

// daemonItems describes the daemon settings the loaded configuration
// carries. A section that is not present publishes nothing, so the tree
// never invents values another host owns.
func daemonItems(daemon DaemonSettings) []Item {
	items := make([]Item, 0, 16)
	items = append(items, watchdogItems(daemon.Watchdog)...)
	items = append(items, oobItems(daemon.OOB)...)
	items = append(items, tapItems(daemon.Tap)...)
	return items
}

func watchdogItems(watchdog WatchdogSettings) []Item {
	if !watchdog.Present {
		return nil
	}
	base := daemonPath + "/watchdog"
	items := []Item{
		{Path: base + "/deploy-window-minutes", Value: uintValue(uint64(watchdog.DeployWindowMinutes))},
		{Path: base + "/connectivity-timeout-seconds", Value: uintValue(uint64(watchdog.ConnectivityTimeoutSeconds))},
		{Path: base + "/check-interval-healthy-seconds", Value: uintValue(uint64(watchdog.CheckIntervalHealthySeconds))},
		{Path: base + "/check-interval-degraded-seconds", Value: uintValue(uint64(watchdog.CheckIntervalDegradedSeconds))},
		{Path: base + "/post-rollback-grace-seconds", Value: uintValue(uint64(watchdog.PostRollbackGraceSeconds))},
		{Path: base + "/alert-cooldown-seconds", Value: uintValue(uint64(watchdog.AlertCooldownSeconds))},
		{Path: base + "/deploy-grace-period-seconds", Value: uintValue(uint64(watchdog.DeployGracePeriodSeconds))},
		{Path: base + "/max-rollback-attempts", Value: uintValue(uint64(watchdog.MaxRollbackAttempts))},
		{Path: base + "/snapshot-healthy-threshold", Value: uintValue(uint64(watchdog.SnapshotHealthyThreshold))},
		{Path: base + "/max-known-good-snapshots", Value: uintValue(uint64(watchdog.MaxKnownGoodSnapshots))},
	}
	// Leaf-list entries are addressed by their value, not a predicate, so
	// the datastore keys each instance by the value itself. The leaf is
	// typed, so a zero address publishes nothing rather than a value the
	// schema rejects.
	for _, target := range watchdog.PingTargets {
		if !target.IsValid() {
			continue
		}
		items = append(items, Item{
			Path:  base + "/probe-targets/ping",
			Value: target.String(),
		})
	}
	return items
}

func oobItems(oob OOBSettings) []Item {
	items := make([]Item, 0, 7)
	if oob.V6Present {
		base := daemonPath + "/oob/ipv6"
		items = append(
			items,
			Item{Path: base + "/interface", Value: oob.V6Iface},
			Item{Path: base + "/table-id", Value: uintValue(uint64(oob.V6TableID))},
			Item{Path: base + "/manage-slaac-rule", Value: boolValue(oob.ManageSLAACRule)},
			Item{Path: base + "/slaac-rule-priority", Value: uintValue(uint64(oob.SLAACRulePriority))},
		)
		// The address leaf is typed; a configuration that carries none
		// publishes none rather than an unparsable value.
		if oob.V6Addr.IsValid() {
			items = append(items, Item{Path: base + "/address", Value: oob.V6Addr.String()})
		}
	}
	if oob.V4Present {
		base := daemonPath + "/oob/ipv4"
		items = append(
			items,
			Item{Path: base + "/interface", Value: oob.V4Iface},
			Item{Path: base + "/table-id", Value: uintValue(uint64(oob.V4TableID))},
		)
	}
	return items
}

func tapItems(tap TapSettings) []Item {
	if !tap.Present {
		return nil
	}
	base := daemonPath + "/tap"
	items := []Item{{Path: base + "/unit", Value: tap.Unit}}
	// Patterns carry regex text a path predicate cannot quote, so each
	// leaf-list entry is addressed by the bare path with its value.
	for _, pattern := range tap.DowngradePatterns {
		items = append(items, Item{
			Path:  base + "/downgrade-pattern",
			Value: pattern,
		})
	}
	return items
}

// uintValue renders one unsigned leaf value.
func uintValue(value uint64) string {
	return strconv.FormatUint(value, 10)
}

// boolValue renders one boolean leaf value.
func boolValue(value bool) string {
	if value {
		return boolTrue
	}
	return "false"
}

// interfaceItems describes one link: the entry exists, its type is
// unspecified, it is enabled, and both address families are enabled.
func connectionItems(connection interfaceintent.Connection, explicit map[string]connectionid.ID) []Item {
	base := interfacePath(connection.Name)
	items := []Item{{Path: base + "/type", Value: connection.Type}}
	if connection.Enabled == nil {
		items = append(items, Item{Path: base + "/enabled", Value: boolTrue})
	} else {
		items = append(items, Item{Path: base + "/enabled", Value: boolValue(*connection.Enabled)})
	}
	if id, set := explicit[connection.Name]; set {
		items = append(items, Item{Path: base + "/goodkind-mwan-steering:connection-id", Value: id.String()})
	}
	items = append(items, Item{Path: base + "/goodkind-mwan-steering:owner", Value: string(connection.Owner)})
	if connection.LeaseStore != "" {
		items = append(items, Item{Path: base + "/goodkind-mwan-steering:lease-store", Value: connection.LeaseStore})
	}
	items = append(items, linkItems(connection)...)
	if connection.Link == nil {
		if connection.IPv4 != nil {
			items = append(items, familyItems(base+"/ietf-ip:ipv4", connection.IPv4.Family)...)
		}
		if connection.IPv6 != nil {
			items = append(items, familyItems(base+"/ietf-ip:ipv6", connection.IPv6.Family)...)
		}
	}
	items = append(items, extendedConnectionItems(connection)...)
	return items
}

func extendedConnectionItems(connection interfaceintent.Connection) []Item {
	base := interfacePath(connection.Name)
	var items []Item
	if connection.IPv4 != nil {
		items = append(items, extendedIPv4Items(base+"/ietf-ip:ipv4/goodkind-mwan-steering:", connection.IPv4)...)
	}
	if connection.IPv6 != nil {
		items = append(items, extendedIPv6Items(base+"/ietf-ip:ipv6/goodkind-mwan-steering:", connection.IPv6)...)
	}
	return items
}

func extendedIPv4Items(base string, ipv4 *interfaceintent.IPv4) []Item {
	items := resolverItems(base+"resolver", ipv4.DNS, ipv4.SearchDomains)
	return append(items, dhcpv4Items(base+"dhcpv4", ipv4.DHCPv4)...)
}

func dhcpv4Items(base string, client *interfaceintent.DHCPv4) []Item {
	if client == nil {
		return nil
	}
	items := []Item{{Path: base, Value: ""}}
	if client.ClientID != "" {
		items = append(items, Item{Path: base + "/client-id", Value: client.ClientID})
	}
	if client.UseDNS != nil {
		items = append(items, Item{Path: base + "/use-dns", Value: boolValue(*client.UseDNS)})
	}
	if client.UseRoutes != nil {
		items = append(items, Item{Path: base + "/use-routes", Value: boolValue(*client.UseRoutes)})
	}
	return items
}

func extendedIPv6Items(base string, ipv6 *interfaceintent.IPv6) []Item {
	items := resolverItems(base+"resolver", ipv6.DNS, ipv6.SearchDomains)
	for _, setting := range []struct {
		name  string
		value *bool
	}{
		{name: "autoconf", value: ipv6.AutoConf},
		{name: "accept-ra-default-route", value: ipv6.AcceptRADefaultRoute},
		{name: "use-ra-dns", value: ipv6.UseRADNS},
	} {
		if setting.value != nil {
			items = append(items, Item{Path: base + setting.name, Value: boolValue(*setting.value)})
		}
	}
	items = append(items, dhcpv6Items(base+"dhcpv6-client", ipv6.DHCPv6)...)
	for _, address := range ipv6.ForwardingAddresses {
		path := base + "forwarding-address[address='" + address.Address.String() + "']"
		items = append(items, Item{Path: path, Value: ""})
		if address.Delivery != "" {
			items = append(items, Item{Path: path + "/delivery", Value: string(address.Delivery)})
		}
	}
	return items
}

func dhcpv6Items(base string, client *interfaceintent.DHCPv6) []Item {
	if client == nil {
		return nil
	}
	items := []Item{{Path: base, Value: ""}}
	for _, setting := range []struct{ name, value string }{
		{name: "duid-type", value: client.DUIDType},
		{name: "duid", value: client.DUID},
		{name: "without-ra", value: client.WithoutRA},
	} {
		if setting.value != "" {
			items = append(items, Item{Path: base + "/" + setting.name, Value: setting.value})
		}
	}
	for _, setting := range []struct {
		name  string
		value *uint32
	}{
		{name: "address-iaid", value: client.IANAIAID},
		{name: "prefix-iaid", value: client.IAPDIAID},
	} {
		if setting.value != nil {
			items = append(items, Item{Path: base + "/" + setting.name, Value: uintValue(uint64(*setting.value))})
		}
	}
	for _, setting := range []struct {
		name  string
		value *bool
	}{
		{name: "request-address", value: client.RequestAddress},
		{name: "request-prefix", value: client.RequestPrefix},
		{name: "use-dns", value: client.UseDNS},
	} {
		if setting.value != nil {
			items = append(items, Item{Path: base + "/" + setting.name, Value: boolValue(*setting.value)})
		}
	}
	return items
}

func resolverItems(base string, servers []netip.Addr, domains []string) []Item {
	var items []Item
	for _, server := range servers {
		items = append(items, Item{Path: base + "/dns", Value: server.String()})
	}
	for _, domain := range domains {
		items = append(items, Item{Path: base + "/search", Value: domain})
	}
	return items
}

// steeringItems marks the member's link as a steering member with its tier and
// weight and, when the daemon probes it, the probe policy that decides its
// state.
func steeringItems(member Member) []Item {
	base := interfacePath(member.Iface) + "/goodkind-mwan-steering:steering"
	items := []Item{
		{Path: base + "/tier", Value: strconv.FormatUint(uint64(member.Tier), 10)},
		{Path: base + "/weight", Value: strconv.FormatUint(uint64(member.Weight), 10)},
	}
	if member.ProbePolicy != "" {
		items = append(items, Item{Path: base + "/probe-policy", Value: member.ProbePolicy})
	}
	return items
}

// wanItems publishes the provider display name and its configured routing,
// translation, source, and health values.
func wanItems(member Member) []Item {
	base := interfacePath(member.Iface) + "/goodkind-mwan-steering:wan"
	providerName := member.ProviderName
	if providerName == "" {
		providerName = member.Name
	}
	items := []Item{
		{Path: base + "/name", Value: providerName},
		{Path: base + "/table-id", Value: uintValue(uint64(member.TableID))},
		{Path: base + "/fw-mark", Value: uintValue(uint64(member.FwMark))},
		{Path: base + "/fw-mark-prio", Value: uintValue(uint64(member.FwMarkPrio))},
		{Path: base + "/from-prio", Value: uintValue(uint64(member.FromPrio))},
	}
	if member.V4Source != "" {
		items = append(items, Item{Path: base + "/v4-source", Value: member.V4Source})
	}
	if member.ForcedDSCP != 0 {
		items = append(items, Item{Path: base + "/forced-dscp", Value: uintValue(uint64(member.ForcedDSCP))})
	}
	items = append(items, translationItems(member)...)
	items = append(items, probeItems(base+"/health", member.Health)...)
	return items
}

// probeItems describes one provider's probe: the enabled flag whenever the
// container exists, and each setting the loaded probe holds. Leaf-list entries
// are addressed by the bare path with their value, like the watchdog targets.
func probeItems(base string, probe *ProbeSettings) []Item {
	if probe == nil {
		return nil
	}
	items := []Item{{Path: base + "/enabled", Value: boolValue(probe.Enabled)}}
	counts := []struct {
		leaf  string
		value *uint8
	}{
		{leaf: "ping-count", value: probe.PingCount},
		{leaf: "success-threshold", value: probe.SuccessThreshold},
		{leaf: "failure-threshold", value: probe.FailureThreshold},
		{leaf: "recovery-threshold", value: probe.RecoveryThreshold},
	}
	for _, count := range counts {
		if count.value == nil {
			continue
		}
		items = append(items, Item{Path: base + "/" + count.leaf, Value: uintValue(uint64(*count.value))})
	}
	if probe.CheckIntervalSeconds != nil {
		items = append(items, Item{
			Path:  base + "/check-interval",
			Value: uintValue(uint64(*probe.CheckIntervalSeconds)),
		})
	}
	for _, target := range probe.TargetsV4 {
		items = append(items, Item{Path: base + "/targets-v4", Value: target.String()})
	}
	for _, target := range probe.TargetsV6 {
		items = append(items, Item{Path: base + "/targets-v6", Value: target.String()})
	}
	for _, url := range probe.HTTPURLs {
		items = append(items, Item{Path: base + "/http-urls", Value: url})
	}
	return items
}

// linkItems describes who writes the member's unit files and, for a link the
// daemon renders, the link identity, both family containers, and the
// free-form sections exactly as the daemon loaded them. A member holding no
// link-files value publishes none of it.
func linkItems(connection interfaceintent.Connection) []Item {
	base := interfacePath(connection.Name)
	items := make([]Item, 0, itemsPerLink)
	if connection.Owner == interfaceintent.OwnerNetworkd {
		linkFiles := linkFilesHandAuthored
		if connection.Link != nil {
			linkFiles = linkFilesRendered
		}
		items = append(items, Item{Path: base + "/goodkind-mwan-steering:link-files", Value: linkFiles})
	}
	if connection.Link == nil {
		return items
	}
	linkSpec := connection.Link
	link := base + "/goodkind-mwan-steering:link"
	if linkSpec.Match.Driver != "" {
		items = append(items, Item{Path: link + "/match/driver", Value: linkSpec.Match.Driver})
	}
	if linkSpec.Match.HardwareAddress != "" {
		items = append(items, Item{Path: link + "/match/hardware-address", Value: linkSpec.Match.HardwareAddress})
	}
	if linkSpec.HardwareAddress != "" {
		items = append(items, Item{Path: link + "/hardware-address", Value: linkSpec.HardwareAddress})
	}
	if linkSpec.MTU != nil {
		items = append(items, Item{Path: link + "/mtu", Value: uintValue(uint64(*linkSpec.MTU))})
	}
	if linkSpec.BridgeMaster != "" {
		items = append(items, Item{Path: link + "/bridge-master", Value: linkSpec.BridgeMaster})
	}
	if linkSpec.VLAN != nil {
		items = append(items,
			Item{Path: link + "/vlan/parent", Value: linkSpec.VLAN.Parent},
			Item{Path: link + "/vlan/id", Value: uintValue(uint64(linkSpec.VLAN.ID))},
		)
	}
	if connection.IPv4 != nil {
		family := base + "/ietf-ip:ipv4"
		items = append(items, familyItems(family, connection.IPv4.Family)...)
		for _, address := range connection.IPv4.SourceAddresses {
			items = append(items, Item{
				Path:  family + "/goodkind-mwan-steering:source-addresses",
				Value: address.String(),
			})
		}
	}
	if connection.IPv6 != nil {
		family := base + "/ietf-ip:ipv6"
		items = append(items, familyItems(family, connection.IPv6.Family)...)
		if connection.IPv6.AcceptRA != nil {
			items = append(items, Item{
				Path:  family + "/goodkind-mwan-steering:accept-ra",
				Value: boolValue(*connection.IPv6.AcceptRA),
			})
		}
		if connection.IPv6.Delegation != nil {
			items = append(items, delegationItems(family+"/goodkind-mwan-steering:delegation", *connection.IPv6.Delegation)...)
		}
	}
	items = append(items, freeFormItems(base+"/goodkind-mwan-steering:networkd", connection.Networkd)...)
	return items
}

// familyItems describes the leaves both families carry: the published
// forwarding flag and address list, and the steering module's dhcp, gateway,
// and route-metric leaves beside them.
func familyItems(base string, family interfaceintent.Family) []Item {
	var items []Item
	if family.Enabled != nil {
		items = append(items, Item{Path: base + "/enabled", Value: boolValue(*family.Enabled)})
	}
	if family.Forwarding != nil {
		items = append(items, Item{Path: base + "/forwarding", Value: boolValue(*family.Forwarding)})
	}
	for _, address := range family.Addresses {
		items = append(items, Item{
			Path:  base + "/address[ip='" + address.Prefix.Addr().String() + "']/prefix-length",
			Value: strconv.Itoa(address.Prefix.Bits()),
		})
	}
	if family.DHCP != nil {
		items = append(items, Item{Path: base + "/goodkind-mwan-steering:dhcp", Value: boolValue(*family.DHCP)})
	}
	if family.Gateway.IsValid() {
		items = append(items, Item{Path: base + "/goodkind-mwan-steering:gateway", Value: family.Gateway.String()})
	}
	if family.RouteMetric != nil {
		items = append(items, Item{
			Path:  base + "/goodkind-mwan-steering:route-metric",
			Value: uintValue(uint64(*family.RouteMetric)),
		})
	}
	for _, route := range family.Routes {
		path := base + "/goodkind-mwan-steering:route[destination='" + route.Destination.String() + "']"
		items = append(items, Item{Path: path + "/table-id", Value: uintValue(uint64(route.TableID))}, Item{Path: path + "/metric", Value: uintValue(uint64(route.Metric))})
		if route.Gateway.IsValid() {
			items = append(items, Item{Path: path + "/gateway", Value: route.Gateway.String()})
		}
	}
	return items
}

// delegationItems describes the delegation the interface requests. Every leaf
// is optional in the model, so each publishes only when the loaded value
// carries it.
func delegationItems(base string, delegation interfaceintent.Delegation) []Item {
	var items []Item
	if delegation.Hint.IsValid() {
		items = append(items, Item{Path: base + "/hint", Value: delegation.Hint.String()})
	}
	if delegation.DUIDType != "" {
		items = append(items, Item{Path: base + "/duid-type", Value: delegation.DUIDType})
	}
	if delegation.DUID != "" {
		items = append(items, Item{Path: base + "/duid", Value: delegation.DUID})
	}
	if delegation.WithoutRA != "" {
		items = append(items, Item{Path: base + "/without-ra", Value: delegation.WithoutRA})
	}
	if delegation.UseDelegatedPrefix != nil {
		items = append(items, Item{Path: base + "/use-delegated-prefix", Value: boolValue(*delegation.UseDelegatedPrefix)})
	}
	if delegation.RouterLifetimeSeconds != nil {
		items = append(items, Item{
			Path:  base + "/router-lifetime-seconds",
			Value: uintValue(uint64(*delegation.RouterLifetimeSeconds)),
		})
	}
	if delegation.IAID != nil {
		items = append(items, Item{Path: base + "/iaid", Value: uintValue(uint64(*delegation.IAID))})
	}
	return items
}

// freeFormItems describes the free-form sections, addressing each section and
// each line by the positional key the model gives it.
func freeFormItems(base string, files []interfaceintent.UnitFile) []Item {
	var items []Item
	for _, file := range files {
		filePath := base + "/file[kind='" + file.Kind + "']"
		for _, section := range file.Sections {
			sectionPath := filePath + "/section[index='" + uintValue(uint64(section.Index)) + "']"
			items = append(items, Item{Path: sectionPath + "/name", Value: section.Name})
			for _, entry := range section.Entries {
				entryPath := sectionPath + "/entry[index='" + uintValue(uint64(entry.Index)) + "']"
				items = append(items,
					Item{Path: entryPath + "/key", Value: entry.Key},
					Item{Path: entryPath + "/value", Value: entry.Value},
				)
			}
		}
	}
	return items
}

// steeringGroupItems describes the settings that apply to the member set as a
// whole. A value the gateway does not hold publishes nothing: a role that runs
// no steering module carries no hash mode, and a caller holding no network
// configuration carries no group values, so the tree never shows a value the
// daemon does not act on. The internal link is always held, because it is the
// interface entry every member routes toward.
func steeringGroupItems(g Gateway) []Item {
	group := g.Group
	items := make([]Item, 0, itemsForGroup+len(group.ReservedTables))
	if g.HashMode != "" {
		items = append(items, Item{Path: steeringGroupPath + "/hash-mode", Value: g.HashMode})
	}
	for _, table := range group.ReservedTables {
		items = append(items, Item{Path: steeringGroupPath + "/reserved-tables", Value: uintValue(uint64(table))})
	}
	translation := steeringGroupPath + "/translation"
	if group.InternalPrefix.IsValid() {
		items = append(items, Item{Path: translation + "/internal-prefix", Value: group.InternalPrefix.String()})
	}
	if group.OpnsenseEdgeV6.IsValid() {
		items = append(items, Item{Path: translation + "/opnsense-edge-v6", Value: group.OpnsenseEdgeV6.String()})
	}
	if group.MwanbrEdgeV6.IsValid() {
		items = append(items, Item{Path: translation + "/mwanbr-edge-v6", Value: group.MwanbrEdgeV6.String()})
	}
	routes := steeringGroupPath + "/routes"
	items = append(items, Item{Path: routes + "/internal-iface", Value: g.InternalIface})
	if group.InternalNetV4.IsValid() {
		items = append(items, Item{Path: routes + "/internal-net-v4", Value: group.InternalNetV4.String()})
	}
	if group.ProbeTimeoutMillis != 0 {
		items = append(items, Item{
			Path:  steeringGroupPath + "/health/probe-timeout",
			Value: uintValue(uint64(group.ProbeTimeoutMillis)),
		})
	}
	return items
}

func firewallItems(g Gateway) []Item {
	cfg := g.Firewall
	if !cfg.Enabled {
		return nil
	}
	base := steeringGroupPath + "/firewall"
	items := []Item{
		{Path: base + "/management-interface", Value: cfg.ManagementInterface},
		{Path: base + "/pinned-set-v4-name", Value: cfg.PinnedSetV4Name},
		{Path: base + "/pinned-set-v6-name", Value: cfg.PinnedSetV6Name},
	}
	for _, service := range cfg.ManagementServices {
		path := base + "/management-service[protocol='" + service.Protocol +
			"'][port='" + uintValue(uint64(service.Port)) + "']"
		items = append(items, Item{Path: path, Value: ""})
		for _, source := range service.Sources {
			items = append(items, Item{Path: path + "/allowed-source", Value: source.String()})
		}
	}
	items = append(items, firewallPinItems(g, base)...)
	for _, prefix := range cfg.PinnedIPv4 {
		items = append(items, Item{Path: base + "/pinned-v4", Value: prefix.String()})
	}
	for _, prefix := range cfg.PinnedIPv6 {
		items = append(items, Item{Path: base + "/pinned-v6", Value: prefix.String()})
	}
	return items
}

// hashModes are the values the model's enumeration accepts. A value outside the
// set would make the datastore reject the whole replace, taking every other
// item with it, so it is caught before the write.
var hashModes = map[string]bool{
	"random":             true,
	"source":             true,
	"source-destination": true,
}

// interfacePath is the list entry for one link.
func interfacePath(iface string) string {
	return interfacesPath + "/interface[name='" + iface + "']"
}

// invalid logs and returns one rejection, so every validation failure is
// both visible in the daemon log and matchable with ErrInvalidGateway.
func invalid(reason string) error {
	slog.Warn("wanconfig: invalid gateway", "reason", reason)
	return fmt.Errorf("%w: %s", ErrInvalidGateway, reason)
}

// validate rejects a Gateway the projection cannot express as paths.
func validate(g Gateway) error {
	if err := validateKey("internal link", g.InternalIface); err != nil {
		return err
	}
	if err := validateFirewall(g); err != nil {
		return err
	}
	if g.HashMode != "" && !hashModes[g.HashMode] {
		return invalid(fmt.Sprintf("hash mode %q is not one of the model's values", g.HashMode))
	}
	if err := validateGroup(g.Group); err != nil {
		return err
	}
	seen, err := validateConnections(g.Connections)
	if err != nil {
		return err
	}
	if err := validateMembers(g.Members, seen); err != nil {
		return err
	}
	return validateConnectionIDInterfaces(g.ConnectionIDs)
}

func validateConnectionIDInterfaces(ids map[string]connectionid.ID) error {
	for iface := range ids {
		if err := validateKey("connection ID interface", iface); err != nil {
			return err
		}
	}
	return nil
}

// validateProvider rejects a provider value its leaf would refuse. A refused
// item fails the whole replace, taking every other item with it, so each one
// is caught before the write.
func validateProvider(member Member) error {
	if member.TableID == 0 {
		return invalid(fmt.Sprintf("member %s table-id must be at least 1", member.Name))
	}
	if member.FwMark == 0 {
		return invalid(fmt.Sprintf("member %s fw-mark must be at least 1", member.Name))
	}
	if member.ForcedDSCP > maxDSCP {
		return invalid(fmt.Sprintf("member %s forced-dscp %d exceeds %d", member.Name, member.ForcedDSCP, maxDSCP))
	}
	if member.V4Source != "" && !isIPv4AddressOrPrefix(member.V4Source) {
		return invalid(fmt.Sprintf("member %s v4-source %q is not an IPv4 address or prefix", member.Name, member.V4Source))
	}
	if err := validateMappings(member); err != nil {
		return err
	}
	if err := validateProbe(member.Name, member.Health); err != nil {
		return err
	}
	return nil
}

// validateMappings rejects a static mapping the list would refuse: an address
// outside IPv4, or an external address the provider maps twice, which the list
// key would collapse into one entry.
func validateMappings(member Member) error {
	if member.TranslationV4 == nil {
		return nil
	}
	mappings := member.TranslationV4.StaticMappings
	seen := make(map[netip.Addr]bool, len(mappings))
	for _, mapping := range mappings {
		if !mapping.External.Is4() || !mapping.Internal.Is4() {
			return invalid(fmt.Sprintf("member %s static mapping %q to %q is not IPv4",
				member.Name, mapping.External, mapping.Internal))
		}
		if seen[mapping.External] {
			return invalid(fmt.Sprintf("member %s maps external address %s twice", member.Name, mapping.External))
		}
		seen[mapping.External] = true
	}
	return nil
}

// validateProbe rejects a probe target outside its leaf-list's address family.
func validateProbe(name string, probe *ProbeSettings) error {
	if probe == nil {
		return nil
	}
	for _, target := range probe.TargetsV4 {
		if !target.Is4() {
			return invalid(fmt.Sprintf("member %s health target %q is not an IPv4 address", name, target))
		}
	}
	for _, target := range probe.TargetsV6 {
		if !target.Is6() {
			return invalid(fmt.Sprintf("member %s health target %q is not an IPv6 address", name, target))
		}
	}
	return nil
}

// validateGroup rejects a group value outside its leaf's address family.
func validateGroup(group GroupSettings) error {
	if group.InternalPrefix.IsValid() && !group.InternalPrefix.Addr().Is6() {
		return invalid(fmt.Sprintf("internal prefix %q is not IPv6", group.InternalPrefix))
	}
	if group.OpnsenseEdgeV6.IsValid() && !group.OpnsenseEdgeV6.Is6() {
		return invalid(fmt.Sprintf("router edge address %q is not IPv6", group.OpnsenseEdgeV6))
	}
	if group.MwanbrEdgeV6.IsValid() && !group.MwanbrEdgeV6.Is6() {
		return invalid(fmt.Sprintf("gateway edge address %q is not IPv6", group.MwanbrEdgeV6))
	}
	if group.InternalNetV4.IsValid() && !group.InternalNetV4.Addr().Is4() {
		return invalid(fmt.Sprintf("internal IPv4 network %q is not IPv4", group.InternalNetV4))
	}
	return nil
}

// isIPv4AddressOrPrefix reports whether value is one of the two forms the
// v4-source leaf's union accepts.
func isIPv4AddressOrPrefix(value string) bool {
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Is4()
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Addr().Is4()
	}
	return false
}

// validateKey rejects an empty key or one that cannot sit inside the
// single-quoted predicate the paths use.
func validateKey(what string, value string) error {
	if value == "" {
		return invalid(what + " is empty")
	}
	if strings.ContainsAny(value, "'\"[]/") {
		return invalid(fmt.Sprintf("%s %q contains a character a path cannot carry", what, value))
	}
	return nil
}
