// Package wanconfig turns gateway settings into path-value items for sysrepo.
package wanconfig

import (
	"encoding/json"
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
	"goodkind.io/mwan/internal/observation/contract"
)

// MarshalObservationItems converts a configured check into operational items.
func MarshalObservationItems(check contract.CheckSpec, intervalSeconds int) ([]Item, error) {
	definition, err := json.Marshal(check)
	if err != nil {
		slog.Error("observation definition could not be encoded", "check_id", check.ID, "err", err)
		return nil, fmt.Errorf("encode observation definition: %w", err)
	}

	base := daemonPath + "/observation/check[id='" + check.ID + "']"

	return []Item{
		{Path: base + "/id", Value: check.ID},
		{Path: base + "/dimension", Value: string(check.Dimension)},
		{Path: base + "/operation", Value: string(check.Operation)},
		{Path: base + "/family", Value: string(check.Family)},
		{Path: base + "/target", Value: check.Target},
		{Path: base + "/interval-seconds", Value: strconv.Itoa(intervalSeconds)},
		{Path: base + "/timeout-seconds", Value: strconv.Itoa(check.TimeoutSeconds)},
		{Path: base + "/max-age-seconds", Value: strconv.Itoa(check.MaxAgeSeconds)},
		{Path: base + "/definition-json", Value: string(definition)},
	}, nil
}

// Item pairs one model path with its value. The Linux publisher converts each
// item to the sysrepo binding type.
type Item struct {
	Path  string
	Value string
}

// Member configures one provider's interface, steering tier, health policy,
// and translation policy.
type Member struct {
	SelectionEnabled *bool
	// Name is the connection ID used by probe policies and translation instances.
	Name         string
	ProviderName string
	// Iface is the interface entry key.
	Iface string
	// Tier is the steering tier the configuration assigns; lower is preferred.
	Tier uint8
	// Weight is the member's share of its tier. The model requires a value of
	// at least one.
	Weight uint16
	// ProbePolicy identifies the health policy that sets the member's state.
	// An empty value means the daemon does not probe the member.
	ProbePolicy     string
	TranslationV4   *config.IPv4Translation
	TranslationV6   *config.IPv6Translation
	TranslationIDV4 uint32
	TranslationIDV6 uint32
	// TableID selects the provider's default-route table. FwMark selects that
	// table, and FwMarkPrio and FromPrio set the two policy-rule priorities.
	// The model requires a nonzero table and mark.
	TableID    uint32
	FwMark     uint32
	FwMarkPrio uint32
	FromPrio   uint32
	// V4Source is the provider's static IPv4 link address or prefix.
	V4Source string
	// ForcedDSCP assigns new flows with that DSCP value to this provider. Zero
	// means that the provider has no forced value.
	ForcedDSCP uint8
	// Health stores the provider's probe settings. Nil means the provider has
	// no health container.
	Health *ProbeSettings
	// LinkFiles identifies whether the member's unit files are rendered or
	// hand-authored. An empty value omits the leaf.
	LinkFiles   string
	BGPSessions []config.BGPSession
}

// ProbeSettings stores one provider's health probe. Pointer fields distinguish
// an omitted setting from a configured zero value.
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

// GroupSettings stores network values shared by all providers.
type GroupSettings struct {
	// ReservedTables lists routing tables that providers cannot use.
	ReservedTables []uint32
	// InternalPrefix is the internal prefix used by every member's translation.
	InternalPrefix netip.Prefix
	// OpnsenseEdgeV6 and MwanbrEdgeV6 are the router and gateway addresses on
	// the internal link.
	OpnsenseEdgeV6 netip.Addr
	MwanbrEdgeV6   netip.Addr
	// InternalNetV4 is the internal IPv4 network routed through every member.
	InternalNetV4 netip.Prefix
	// ProbeTimeoutMillis limits one probe attempt. Zero omits the model leaf.
	ProbeTimeoutMillis uint32
}

// Gateway stores configuration for the management surface.
type Gateway struct {
	Firewall           firewall.Config
	PinnedConnectionID string
	ConnectionIDs      map[string]connectionid.ID
	Connections        []interfaceintent.Connection
	// InternalIface connects the gateway to the router. The model enables both
	// address families on this interface.
	InternalIface string
	// HashMode selects a provider for each new connection in the active tier.
	// An empty value omits the leaf.
	HashMode string
	// Group stores network values shared by providers. Zero values omit leaves.
	Group GroupSettings
	// Members are the steering members in a stable order.
	Members []Member
}

// OwnedPaths lists datastore subtrees that each publication replaces.
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

	// natPolicyID identifies the policy used by each translation instance.
	natPolicyID = 1

	boolTrue = "true"

	// maxDSCP is the largest value in the six-bit DSCP field.
	maxDSCP = 63

	// maxVLANID is the largest tag the model's vlan/id leaf accepts.
	maxVLANID = 4094

	// These values match the link-files enumeration.
	linkFilesRendered     = "rendered"
	linkFilesHandAuthored = "hand-authored"

	// These constants estimate item-slice capacity for populated members and
	// groups. They do not limit the item count.
	itemsPerMember = 28
	itemsForGroup  = 16
	// itemsPerLink sizes the items a rendered link with both families and a
	// delegation publishes.
	itemsPerLink = 24
)

// ErrInvalidGateway marks gateway values that the model cannot accept.
var ErrInvalidGateway = errors.New("wanconfig: invalid gateway")

// ConfigItems returns model items in a stable order. It returns an error when
// a key, translation pair, or value violates the model.
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

	return items, nil
}

func uintValue(value uint64) string {
	return strconv.FormatUint(value, 10)
}

func boolValue(value bool) string {
	if value {
		return boolTrue
	}

	return "false"
}

// interfaceItems projects one link and its configured address families.
func connectionItems(
	connection interfaceintent.Connection,
	explicit map[string]connectionid.ID,
) []Item {
	base := interfacePath(connection.Name)
	items := []Item{{Path: base + "/type", Value: connection.Type}}
	if connection.Enabled == nil {
		items = append(items, Item{Path: base + "/enabled", Value: boolTrue})
	} else {
		items = append(items, Item{Path: base + "/enabled", Value: boolValue(*connection.Enabled)})
	}

	if id, set := explicit[connection.Name]; set {
		items = append(
			items,
			Item{Path: base + "/goodkind-mwan-steering:connection-id", Value: id.String()},
		)
	}

	items = append(
		items,
		Item{Path: base + "/goodkind-mwan-steering:owner", Value: string(connection.Owner)},
	)
	if connection.LeaseStore != "" {
		items = append(
			items,
			Item{Path: base + "/goodkind-mwan-steering:lease-store", Value: connection.LeaseStore},
		)
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
		items = append(
			items,
			extendedIPv4Items(base+"/ietf-ip:ipv4/goodkind-mwan-steering:", connection.IPv4)...,
		)
	}

	if connection.IPv6 != nil {
		items = append(
			items,
			extendedIPv6Items(base+"/ietf-ip:ipv6/goodkind-mwan-steering:", connection.IPv6)...,
		)
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
			items = append(
				items,
				Item{Path: base + "/" + setting.name, Value: uintValue(uint64(*setting.value))},
			)
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
			items = append(
				items,
				Item{Path: base + "/" + setting.name, Value: boolValue(*setting.value)},
			)
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

// steeringItems projects each provider's tier, weight, and probe policy.
func steeringItems(member Member) []Item {
	base := interfacePath(member.Iface) + "/goodkind-mwan-steering:steering"
	items := []Item{
		{Path: base + "/tier", Value: strconv.FormatUint(uint64(member.Tier), 10)},
		{Path: base + "/weight", Value: strconv.FormatUint(uint64(member.Weight), 10)},
	}
	if member.SelectionEnabled != nil {
		items = append(
			items,
			Item{Path: base + "/enabled", Value: boolValue(*member.SelectionEnabled)},
		)
	}

	if member.ProbePolicy != "" {
		items = append(items, Item{Path: base + "/probe-policy", Value: member.ProbePolicy})
	}

	return items
}

// wanItems projects provider routing, translation, source, and health settings.
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
		items = append(
			items,
			Item{Path: base + "/forced-dscp", Value: uintValue(uint64(member.ForcedDSCP))},
		)
	}

	items = append(items, translationItems(member)...)
	items = append(items, probeItems(base+"/health", member.Health)...)
	items = append(items, bgpSessionItems(base, member.BGPSessions)...)

	return items
}

// probeItems projects the configured probe. Leaf-list values serve as keys.
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

		items = append(
			items,
			Item{Path: base + "/" + count.leaf, Value: uintValue(uint64(*count.value))},
		)
	}

	if probe.CheckIntervalSeconds != nil {
		items = append(
			items,
			Item{
				Path:  base + "/check-interval",
				Value: uintValue(uint64(*probe.CheckIntervalSeconds)),
			},
		)
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

// linkItems projects link ownership, identity, address families, and networkd
// sections for links that the daemon renders.
func linkItems(connection interfaceintent.Connection) []Item {
	base := interfacePath(connection.Name)
	items := make([]Item, 0, itemsPerLink)
	if connection.Owner == interfaceintent.OwnerNetworkd {
		linkFiles := linkFilesHandAuthored
		if connection.Link != nil {
			linkFiles = linkFilesRendered
		}

		items = append(
			items,
			Item{Path: base + "/goodkind-mwan-steering:link-files", Value: linkFiles},
		)
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
		items = append(
			items,
			Item{Path: link + "/match/hardware-address", Value: linkSpec.Match.HardwareAddress},
		)
	}

	if linkSpec.HardwareAddress != "" {
		items = append(
			items,
			Item{Path: link + "/hardware-address", Value: linkSpec.HardwareAddress},
		)
	}

	if linkSpec.MTU != nil {
		items = append(items, Item{Path: link + "/mtu", Value: uintValue(uint64(*linkSpec.MTU))})
	}

	if linkSpec.BridgeMaster != "" {
		items = append(items, Item{Path: link + "/bridge-master", Value: linkSpec.BridgeMaster})
	}

	if linkSpec.VLAN != nil {
		items = append(
			items,
			Item{Path: link + "/vlan/parent", Value: linkSpec.VLAN.Parent},
			Item{Path: link + "/vlan/id", Value: uintValue(uint64(linkSpec.VLAN.ID))},
		)
	}

	items = append(items, tunnelItems(link+"/tunnel", linkSpec.Tunnel)...)
	if connection.IPv4 != nil {
		family := base + "/ietf-ip:ipv4"

		items = append(items, familyItems(family, connection.IPv4.Family)...)
		for _, address := range connection.IPv4.SourceAddresses {
			items = append(
				items,
				Item{
					Path:  family + "/goodkind-mwan-steering:source-addresses",
					Value: address.String(),
				},
			)
		}
	}

	if connection.IPv6 != nil {
		family := base + "/ietf-ip:ipv6"
		items = append(items, familyItems(family, connection.IPv6.Family)...)
		if connection.IPv6.AcceptRA != nil {
			items = append(
				items,
				Item{
					Path:  family + "/goodkind-mwan-steering:accept-ra",
					Value: boolValue(*connection.IPv6.AcceptRA),
				},
			)
		}

		if connection.IPv6.Delegation != nil {
			items = append(
				items,
				delegationItems(
					family+"/goodkind-mwan-steering:delegation",
					*connection.IPv6.Delegation,
				)...,
			)
		}
	}

	items = append(
		items,
		freeFormItems(base+"/goodkind-mwan-steering:networkd", connection.Networkd)...,
	)

	return items
}

// familyItems projects address, forwarding, DHCP, gateway, and route settings.
func familyItems(base string, family interfaceintent.Family) []Item {
	var items []Item
	if family.Enabled != nil {
		items = append(items, Item{Path: base + "/enabled", Value: boolValue(*family.Enabled)})
	}

	if family.Forwarding != nil {
		items = append(
			items,
			Item{Path: base + "/forwarding", Value: boolValue(*family.Forwarding)},
		)
	}

	for _, address := range family.Addresses {
		items = append(
			items,
			Item{
				Path:  base + "/address[ip='" + address.Prefix.Addr().String() + "']/prefix-length",
				Value: strconv.Itoa(address.Prefix.Bits()),
			},
		)
	}

	if family.DHCP != nil {
		items = append(
			items,
			Item{Path: base + "/goodkind-mwan-steering:dhcp", Value: boolValue(*family.DHCP)},
		)
	}

	if family.Gateway.IsValid() {
		items = append(
			items,
			Item{Path: base + "/goodkind-mwan-steering:gateway", Value: family.Gateway.String()},
		)
	}

	if family.RouteMetric != nil {
		items = append(
			items,
			Item{
				Path:  base + "/goodkind-mwan-steering:route-metric",
				Value: uintValue(uint64(*family.RouteMetric)),
			},
		)
	}

	for _, route := range family.Routes {
		path := base + "/goodkind-mwan-steering:route[destination='" + route.Destination.String() + "']"
		items = append(
			items,
			Item{Path: path + "/table-id", Value: uintValue(uint64(route.TableID))},
			Item{Path: path + "/metric", Value: uintValue(uint64(route.Metric))},
		)
		if route.Gateway.IsValid() {
			items = append(items, Item{Path: path + "/gateway", Value: route.Gateway.String()})
		}
	}

	return items
}

// delegationItems projects the configured delegation fields. The model makes
// each field optional.
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
		items = append(
			items,
			Item{
				Path:  base + "/use-delegated-prefix",
				Value: boolValue(*delegation.UseDelegatedPrefix),
			},
		)
	}

	if delegation.RouterLifetimeSeconds != nil {
		items = append(
			items,
			Item{
				Path:  base + "/router-lifetime-seconds",
				Value: uintValue(uint64(*delegation.RouterLifetimeSeconds)),
			},
		)
	}

	if delegation.IAID != nil {
		items = append(
			items,
			Item{Path: base + "/iaid", Value: uintValue(uint64(*delegation.IAID))},
		)
	}

	return items
}

// freeFormItems projects networkd sections and entries by their index keys.
func freeFormItems(base string, files []interfaceintent.UnitFile) []Item {
	var items []Item

	for _, file := range files {
		filePath := base + "/file[kind='" + file.Kind + "']"
		for _, section := range file.Sections {
			sectionPath := filePath + "/section[index='" + uintValue(uint64(section.Index)) + "']"

			items = append(items, Item{Path: sectionPath + "/name", Value: section.Name})
			for _, entry := range section.Entries {
				entryPath := sectionPath + "/entry[index='" + uintValue(uint64(entry.Index)) + "']"
				items = append(
					items,
					Item{Path: entryPath + "/key", Value: entry.Key},
					Item{Path: entryPath + "/value", Value: entry.Value},
				)
			}
		}
	}

	return items
}

// steeringGroupItems projects settings shared by the provider set. Zero values
// omit optional leaves. The internal link identifies the route toward the
// router.
func steeringGroupItems(g Gateway) []Item {
	group := g.Group
	items := make([]Item, 0, itemsForGroup+len(group.ReservedTables))
	if g.HashMode != "" {
		items = append(items, Item{Path: steeringGroupPath + "/hash-mode", Value: g.HashMode})
	}

	for _, table := range group.ReservedTables {
		items = append(
			items,
			Item{Path: steeringGroupPath + "/reserved-tables", Value: uintValue(uint64(table))},
		)
	}

	translation := steeringGroupPath + "/translation"
	if group.InternalPrefix.IsValid() {
		items = append(
			items,
			Item{Path: translation + "/internal-prefix", Value: group.InternalPrefix.String()},
		)
	}

	if group.OpnsenseEdgeV6.IsValid() {
		items = append(
			items,
			Item{Path: translation + "/opnsense-edge-v6", Value: group.OpnsenseEdgeV6.String()},
		)
	}

	if group.MwanbrEdgeV6.IsValid() {
		items = append(
			items,
			Item{Path: translation + "/mwanbr-edge-v6", Value: group.MwanbrEdgeV6.String()},
		)
	}

	routes := steeringGroupPath + "/routes"
	items = append(items, Item{Path: routes + "/internal-iface", Value: g.InternalIface})
	if group.InternalNetV4.IsValid() {
		items = append(
			items,
			Item{Path: routes + "/internal-net-v4", Value: group.InternalNetV4.String()},
		)
	}

	if group.ProbeTimeoutMillis != 0 {
		items = append(
			items,
			Item{
				Path:  steeringGroupPath + "/health/probe-timeout",
				Value: uintValue(uint64(group.ProbeTimeoutMillis)),
			},
		)
	}

	return items
}

// hashModes lists the values accepted by the model's hash-mode enumeration.
var hashModes = map[string]bool{
	"random":             true,
	"source":             true,
	"source-destination": true,
}

func interfacePath(iface string) string {
	return interfacesPath + "/interface[name='" + iface + "']"
}

// invalid logs a rejected value and returns ErrInvalidGateway.
func invalid(reason string) error {
	slog.Warn("wanconfig: invalid gateway", "reason", reason)
	return fmt.Errorf("%w: %s", ErrInvalidGateway, reason)
}

// validate rejects gateway values that the model cannot express.
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

// validateProvider rejects provider values outside the model's constraints.
func validateProvider(member Member) error {
	if member.TableID == 0 {
		return invalid(fmt.Sprintf("member %s table-id must be at least 1", member.Name))
	}

	if member.FwMark == 0 {
		return invalid(fmt.Sprintf("member %s fw-mark must be at least 1", member.Name))
	}

	if member.ForcedDSCP > maxDSCP {
		return invalid(
			fmt.Sprintf(
				"member %s forced-dscp %d exceeds %d",
				member.Name,
				member.ForcedDSCP,
				maxDSCP,
			),
		)
	}

	if member.V4Source != "" && !isIPv4AddressOrPrefix(member.V4Source) {
		return invalid(
			fmt.Sprintf(
				"member %s v4-source %q is not an IPv4 address or prefix",
				member.Name,
				member.V4Source,
			),
		)
	}

	if err := validateMappings(member); err != nil {
		return err
	}

	if err := validateProbe(member.Name, member.Health); err != nil {
		return err
	}

	return nil
}

// validateMappings rejects non-IPv4 mappings and duplicate external addresses.
func validateMappings(member Member) error {
	if member.TranslationV4 == nil {
		return nil
	}

	mappings := member.TranslationV4.StaticMappings

	seen := make(map[netip.Addr]bool, len(mappings))
	for _, mapping := range mappings {
		if !mapping.External.Is4() || !mapping.Internal.Is4() {
			return invalid(
				fmt.Sprintf(
					"member %s static mapping %q to %q is not IPv4",
					member.Name,
					mapping.External,
					mapping.Internal,
				),
			)
		}

		if seen[mapping.External] {
			return invalid(
				fmt.Sprintf(
					"member %s maps external address %s twice",
					member.Name,
					mapping.External,
				),
			)
		}

		seen[mapping.External] = true
	}

	return nil
}

// validateProbe rejects targets outside their configured address families.
func validateProbe(name string, probe *ProbeSettings) error {
	if probe == nil {
		return nil
	}

	for _, target := range probe.TargetsV4 {
		if !target.Is4() {
			return invalid(
				fmt.Sprintf("member %s health target %q is not an IPv4 address", name, target),
			)
		}
	}

	for _, target := range probe.TargetsV6 {
		if !target.Is6() {
			return invalid(
				fmt.Sprintf("member %s health target %q is not an IPv6 address", name, target),
			)
		}
	}

	return nil
}

// validateGroup rejects values outside their configured address families.
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

// isIPv4AddressOrPrefix reports whether value is an IPv4 address or prefix.
func isIPv4AddressOrPrefix(value string) bool {
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Is4()
	}

	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Addr().Is4()
	}

	return false
}

// validateKey rejects empty keys and characters that break path predicates.
func validateKey(what string, value string) error {
	if value == "" {
		return invalid(what + " is empty")
	}

	if strings.ContainsAny(value, "'\"[]/") {
		return invalid(fmt.Sprintf("%s %q contains a character a path cannot carry", what, value))
	}

	return nil
}
