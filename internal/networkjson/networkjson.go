// Package networkjson loads the gateway's network configuration: the provider
// inventory, each provider's routing slots, translation prefix, static
// mappings, health probe, and link identity, and the group-wide translation,
// internal link, and probe timeout. The file is written in the model's own
// JSON encoding and validated against the installed schema before any value is
// read, so the file the daemon loads and the tree the management surface
// serves describe one thing.
//
// The package is linux-only: validation binds libyang, which only the linux
// build links, and the only role that reads the file runs there.
package networkjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/yangpub"
)

// DefaultPath is where the deploy writes the network configuration.
const DefaultPath = "/etc/mwan/network.json"

// DefaultSchemaDir is where the wanconfig stack deploy installs the model
// files. The deploy validates the rendered file against the same files before
// it lands here, so one schema serves both checkpoints.
const DefaultSchemaDir = "/usr/local/share/wanconfig/yang"

// document mirrors the model's JSON encoding. Every scalar the daemon needs is
// a pointer, so an absent leaf is distinguishable from a zero and can be
// rejected rather than defaulted.
type document struct {
	Interfaces interfaces `json:"ietf-interfaces:interfaces"`
}

type interfaces struct {
	Interface     []ifaceEntry  `json:"interface"`
	SteeringGroup steeringGroup `json:"goodkind-mwan-steering:steering-group"`
}

type ifaceEntry struct {
	Name         string             `json:"name"`
	Type         string             `json:"type"`
	Enabled      *bool              `json:"enabled"`
	ConnectionID string             `json:"goodkind-mwan-steering:connection-id"`
	Owner        string             `json:"goodkind-mwan-steering:owner"`
	LinkFiles    string             `json:"goodkind-mwan-steering:link-files"`
	LeaseStore   string             `json:"goodkind-mwan-steering:lease-store"`
	Link         *linkIdentity      `json:"goodkind-mwan-steering:link"`
	Networkd     *networkdContainer `json:"goodkind-mwan-steering:networkd"`
	IPv4         *familyV4          `json:"ietf-ip:ipv4"`
	IPv6         *familyV6          `json:"ietf-ip:ipv6"`
	WAN          *wan               `json:"goodkind-mwan-steering:wan"`
	Steering     *steering          `json:"goodkind-mwan-steering:steering"`
}

// The two values the link-files leaf takes.
const (
	linkFilesRendered     = "rendered"
	linkFilesHandAuthored = "hand-authored"
)

// linkIdentity is the link container: how the device is matched, the address
// set on it, and the VLAN it is created as. The vlan container has presence,
// so a pointer distinguishes absent from present.
type linkIdentity struct {
	Match           *linkMatch `json:"match"`
	HardwareAddress string     `json:"hardware-address"`
	MTU             *uint32    `json:"mtu"`
	VLAN            *linkVLAN  `json:"vlan"`
	BridgeMaster    string     `json:"bridge-master"`
}

type linkMatch struct {
	Driver          string `json:"driver"`
	HardwareAddress string `json:"hardware-address"`
}

// The integer widths of the link, family, and free-form wire types are the
// model's own, so the decoder refuses a value outside them and the loader
// never narrows one.
type linkVLAN struct {
	Parent string  `json:"parent"`
	ID     *uint16 `json:"id"`
}

// networkdContainer mirrors the free-form layer: per unit file kind, an
// ordered list of sections, each an ordered list of key and value lines.
type networkdContainer struct {
	Files []networkdFile `json:"file"`
}

type networkdFile struct {
	Kind     string            `json:"kind"`
	Sections []networkdSection `json:"section"`
}

type networkdSection struct {
	Index   *uint16         `json:"index"`
	Name    string          `json:"name"`
	Entries []networkdEntry `json:"entry"`
}

type networkdEntry struct {
	Index *uint16 `json:"index"`
	Key   string  `json:"key"`
	Value string  `json:"value"`
}

// familyWire is what the two published per-family containers share: the
// forwarding flag and address list the published model defines, and the
// leaves the steering module adds under its own namespace.
type familyWire struct {
	Enabled     *bool              `json:"enabled"`
	Forwarding  *bool              `json:"forwarding"`
	Address     []ipAddress        `json:"address"`
	DHCP        *bool              `json:"goodkind-mwan-steering:dhcp"`
	Gateway     string             `json:"goodkind-mwan-steering:gateway"`
	RouteMetric *uint32            `json:"goodkind-mwan-steering:route-metric"`
	Routes      []routeWire        `json:"goodkind-mwan-steering:route"`
	Resolver    *resolver          `json:"goodkind-mwan-steering:resolver"`
	Translation *familyTranslation `json:"goodkind-mwan-steering:translation"`
}

type routeWire struct {
	Destination string  `json:"destination"`
	Gateway     string  `json:"gateway"`
	TableID     *uint32 `json:"table-id"`
	Metric      uint32  `json:"metric"`
}

type resolver struct {
	DNSServers    []string `json:"dns"`
	SearchDomains []string `json:"search"`
}

type familyV4 struct {
	familyWire
	SourceAddresses []string `json:"goodkind-mwan-steering:source-addresses"`
	DHCPv4          *dhcpv4  `json:"goodkind-mwan-steering:dhcpv4"`
}

type dhcpv4 struct {
	ClientID  string `json:"client-id"`
	UseDNS    *bool  `json:"use-dns"`
	UseRoutes *bool  `json:"use-routes"`
}

type familyV6 struct {
	familyWire
	AcceptRA             *bool               `json:"goodkind-mwan-steering:accept-ra"`
	AutoConf             *bool               `json:"goodkind-mwan-steering:autoconf"`
	AcceptRADefaultRoute *bool               `json:"goodkind-mwan-steering:accept-ra-default-route"`
	UseRADNS             *bool               `json:"goodkind-mwan-steering:use-ra-dns"`
	Delegation           *delegation         `json:"goodkind-mwan-steering:delegation"`
	DHCPv6               *dhcpv6             `json:"goodkind-mwan-steering:dhcpv6-client"`
	ForwardingAddresses  []forwardingAddress `json:"goodkind-mwan-steering:forwarding-address"`
}

type forwardingAddress struct {
	Address  string `json:"address"`
	Delivery string `json:"delivery"`
}

type dhcpv6 struct {
	DUIDType       string  `json:"duid-type"`
	DUID           string  `json:"duid"`
	IANAIAID       *uint32 `json:"address-iaid"`
	IAPDIAID       *uint32 `json:"prefix-iaid"`
	RequestAddress *bool   `json:"request-address"`
	RequestPrefix  *bool   `json:"request-prefix"`
	WithoutRA      string  `json:"without-ra"`
	UseDNS         *bool   `json:"use-dns"`
}

type ipAddress struct {
	IP           string `json:"ip"`
	PrefixLength *uint8 `json:"prefix-length"`
}

type delegation struct {
	Hint                  string  `json:"hint"`
	DUIDType              string  `json:"duid-type"`
	DUID                  string  `json:"duid"`
	WithoutRA             string  `json:"without-ra"`
	UseDelegatedPrefix    *bool   `json:"use-delegated-prefix"`
	RouterLifetimeSeconds *uint32 `json:"router-lifetime-seconds"`
	IAID                  *uint32 `json:"iaid"`
}

// steering is the member's steering properties: which tier it sits in and how
// much of that tier's traffic it takes. Both are pointers so an absent leaf is
// distinguishable from a zero the daemon would act on. Tier zero is the
// preferred tier, and weight zero would make the balancer's divisor wrong.
type steering struct {
	Enabled *bool `json:"enabled"`
	Tier    *int  `json:"tier"`
	Weight  *int  `json:"weight"`
}

type wan struct {
	Name       string `json:"name"`
	TableID    *int   `json:"table-id"`
	FwMark     *int   `json:"fw-mark"`
	FwMarkPrio *int   `json:"fw-mark-prio"`
	FromPrio   *int   `json:"from-prio"`
	// V4Source is decoded only to be refused: the daemon derives the source
	// pin from the link's static address, and a document that still types the
	// leaf would let inventory and the address disagree.
	V4Source   string  `json:"v4-source"`
	ForcedDSCP *int    `json:"forced-dscp"`
	Health     *health `json:"health"`
}

type staticMapping struct {
	External string `json:"external"`
	Internal string `json:"internal"`
	Delivery string `json:"delivery"`
}

type health struct {
	Enabled           *bool    `json:"enabled"`
	PingCount         *int     `json:"ping-count"`
	SuccessThreshold  *int     `json:"success-threshold"`
	FailureThreshold  *int     `json:"failure-threshold"`
	RecoveryThreshold *int     `json:"recovery-threshold"`
	CheckInterval     *int     `json:"check-interval"`
	TargetsV4         []string `json:"targets-v4"`
	TargetsV6         []string `json:"targets-v6"`
	HTTPURLs          []string `json:"http-urls"`
}

type steeringGroup struct {
	HashMode       string        `json:"hash-mode"`
	ReservedTables []int         `json:"reserved-tables"`
	Translation    translation   `json:"translation"`
	Routes         routes        `json:"routes"`
	Firewall       *firewallWire `json:"firewall"`
	Health         groupHealth   `json:"health"`
}

type translation struct {
	InternalPrefix string `json:"internal-prefix"`
	OpnsenseEdgeV6 string `json:"opnsense-edge-v6"`
	MwanbrEdgeV6   string `json:"mwanbr-edge-v6"`
}

type routes struct {
	InternalIface string `json:"internal-iface"`
	InternalNetV4 string `json:"internal-net-v4"`
}

type groupHealth struct {
	ProbeTimeout *int `json:"probe-timeout"`
}

// Config is the network tree one file carries, in the shape the daemon's
// configuration holds it.
type Config struct {
	Firewall              firewall.Config
	PinnedConnectionID    string
	InternalPrefix        string
	OpnsenseEdgeV6        string
	MwanbrEdgeV6          string
	InternalIface         string
	InternalNetV4         string
	HashMode              string
	ReservedTables        []int
	ProbeTimeoutMillis    int
	WAN                   map[string]config.IfMgrWANEntry
	Health                map[string]config.IfMgrHealthWANSection
	ConnectionIDs         map[string]connectionid.ID
	ExplicitConnectionIDs map[string]connectionid.ID
	Connections           []interfaceintent.Connection
	Claims                []interfaceintent.Claim
	Rejected              []Rejection
}

// Rejection records one provider entry the loader refused and the reason.
// Provider is empty when the entry states no provider name.
type Rejection struct {
	Interface string
	Provider  string
	Err       error
}

// Load reads path, validates it against the models in schemaDir, and returns
// the network tree encoded in it. An unreadable file, a file the schema
// rejects, a missing group-wide value, and a conflict between two providers
// are fatal: none of them has a safe default and none of them belongs to one
// entry. A defect inside one provider entry rejects that entry alone: the
// loader records it in Rejected, logs it, and returns the remaining providers.
// One provider's mistake never removes steering from the others. A document
// with no loadable provider is fatal.
func Load(path string, schemaDir string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("networkjson: read failed", "err", err, "path", path)
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	schema, err := yangpub.LoadSchema(schemaDir)
	if err != nil {
		slog.Error("networkjson: schema load failed", "err", err, "schema_dir", schemaDir)
		return nil, fmt.Errorf("load schema from %s: %w", schemaDir, err)
	}
	defer schema.Close()
	if err := schema.ValidateConfigJSON(data); err != nil {
		slog.Error("networkjson: schema validation failed", "err", err, "path", path)
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Error("networkjson: decode failed", "err", err, "path", path)
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	loaded, err := build(&doc)
	if err != nil {
		// build returns a missing group-wide value, a provider-set conflict (a
		// duplicate routing number, a reserved table), and a document with no
		// loadable provider. The wrapped error text states which.
		slog.Error("networkjson: configuration rejected", "err", err, "path", path)
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return loaded, nil
}

// kernelReservedTables are the routing tables the kernel owns: unspecified,
// default, main, and local. They are not inventory values, so they are reserved
// here rather than typed into the reserved-tables leaf-list, which carries only
// the tables this gateway's other software claims.
var kernelReservedTables = []int{0, 253, 254, 255}

// build turns the decoded document into the daemon's shape, rejecting a value
// the schema cannot require. The schema makes a provider's name mandatory and
// bounds the table id and firewall mark; it leaves the rest optional, because a
// leaf's type cannot see its siblings. The daemon needs every routing number
// and every group-wide value, so those checks live here. It does not need a
// translation prefix: a provider that delegates nothing carries none, and
// every module that reads one already treats an absent prefix as no
// translation.
func build(doc *document) (*Config, error) {
	group := doc.Interfaces.SteeringGroup
	var zeroFirewall firewall.Config
	loaded := &Config{
		Firewall:              zeroFirewall,
		PinnedConnectionID:    pinnedConnectionID(group.Firewall),
		InternalPrefix:        group.Translation.InternalPrefix,
		OpnsenseEdgeV6:        group.Translation.OpnsenseEdgeV6,
		MwanbrEdgeV6:          group.Translation.MwanbrEdgeV6,
		InternalIface:         group.Routes.InternalIface,
		InternalNetV4:         group.Routes.InternalNetV4,
		HashMode:              group.HashMode,
		ReservedTables:        group.ReservedTables,
		ProbeTimeoutMillis:    0,
		WAN:                   make(map[string]config.IfMgrWANEntry, len(doc.Interfaces.Interface)),
		Health:                make(map[string]config.IfMgrHealthWANSection, len(doc.Interfaces.Interface)),
		ConnectionIDs:         make(map[string]connectionid.ID, len(doc.Interfaces.Interface)),
		ExplicitConnectionIDs: make(map[string]connectionid.ID),
		Connections:           nil,
		Claims:                nil,
		Rejected:              nil,
	}
	required := []struct {
		leaf  string
		value string
	}{
		{leaf: "steering-group/translation/internal-prefix", value: loaded.InternalPrefix},
		{leaf: "steering-group/translation/opnsense-edge-v6", value: loaded.OpnsenseEdgeV6},
		{leaf: "steering-group/translation/mwanbr-edge-v6", value: loaded.MwanbrEdgeV6},
		{leaf: "steering-group/routes/internal-iface", value: loaded.InternalIface},
		{leaf: "steering-group/routes/internal-net-v4", value: loaded.InternalNetV4},
		{leaf: "steering-group/hash-mode", value: loaded.HashMode},
	}
	for _, leaf := range required {
		if leaf.value == "" {
			return nil, fmt.Errorf("%s is required", leaf.leaf)
		}
	}
	if group.Health.ProbeTimeout == nil {
		return nil, errors.New("steering-group/health/probe-timeout is required")
	}
	loaded.ProbeTimeoutMillis = *group.Health.ProbeTimeout
	ids, explicitIDs, err := normalizeConnectionIDs(doc.Interfaces.Interface)
	if err != nil {
		return nil, err
	}
	loaded.ConnectionIDs = ids
	loaded.ExplicitConnectionIDs = explicitIDs
	connections, rejected, err := compileConnections(doc.Interfaces.Interface, ids, loaded.InternalIface, group.Firewall)
	if err != nil {
		return nil, err
	}
	loaded.Connections = connections
	loaded.Rejected = append(loaded.Rejected, rejected...)
	byName := make(map[string]interfaceintent.Connection, len(connections))
	for _, connection := range connections {
		byName[connection.Name] = connection
	}

	if err := compileProviderProjections(doc.Interfaces.Interface, byName, loaded); err != nil {
		return nil, err
	}
	active := loaded.Connections[:0]
	for _, connection := range loaded.Connections {
		if connection.Roles&interfaceintent.RoleProvider != 0 {
			if _, accepted := loaded.WAN[connection.ID.String()]; !accepted {
				continue
			}
		}
		active = append(active, connection)
	}
	loaded.Connections = active
	if err := validateActiveDependencies(loaded.Connections); err != nil {
		return nil, err
	}
	if len(loaded.WAN) == 0 {
		if len(loaded.Rejected) > 0 {
			slog.Error("networkjson: every provider entry was rejected; nothing to steer",
				"rejected", len(loaded.Rejected), "err", loaded.Rejected[0].Err)
			return nil, fmt.Errorf("every provider entry was rejected; the first: %w", loaded.Rejected[0].Err)
		}
		return nil, errors.New("no interface declares a provider")
	}
	if err := checkProviderSet(loaded); err != nil {
		return nil, err
	}
	loaded.Claims, err = compileClaims(loaded.Connections, loaded.WAN)
	if err != nil {
		return nil, err
	}
	firewallConfig, err := buildFirewall(doc, loaded)
	if err != nil {
		return nil, err
	}
	loaded.Firewall = firewallConfig
	return loaded, nil
}

func compileProviderProjections(entries []ifaceEntry, byName map[string]interfaceintent.Connection, loaded *Config) error {
	dscpOwners := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.WAN == nil {
			continue
		}
		connection, accepted := byName[entry.Name]
		if !accepted {
			continue
		}
		id := loaded.ConnectionIDs[entry.Name].String()
		routing, probe, err := buildProvider(entry)
		if err != nil {
			if entry.Owner == string(interfaceintent.OwnerMWAN) {
				slog.Error("networkjson: MWAN-owned provider invalid", "interface", entry.Name, "err", err)
				return fmt.Errorf("interface %s: %w", entry.Name, err)
			}
			loaded.Rejected = append(loaded.Rejected, rejectEntry(entry, err))
			continue
		}
		routing.V4Source = staticV4Source(connection)
		if entry.WAN.ForcedDSCP != nil {
			value := *entry.WAN.ForcedDSCP
			if owner, taken := dscpOwners[value]; taken {
				return fmt.Errorf("wan %s: forced-dscp %d is already taken by wan %s", id, value, owner)
			}
			dscpOwners[value] = id
		}
		routing.ProviderName = entry.WAN.Name
		loaded.WAN[id] = routing
		if probe != nil {
			loaded.Health[id] = *probe
		}
	}
	return nil
}

// rejectEntry records one refused provider entry and logs it. The log line is
// the operator's first signal: the daemon starts without this provider, and
// the health state file and the served tree omit it.
func rejectEntry(entry ifaceEntry, err error) Rejection {
	provider := ""
	if entry.WAN != nil {
		provider = entry.WAN.Name
	}
	slog.Error("networkjson: provider entry rejected; the daemon runs without it",
		"interface", entry.Name, "provider", provider, "err", err)
	return Rejection{Interface: entry.Name, Provider: provider, Err: err}
}

// checkProviderSet runs the checks that need the whole provider set rather than
// one entry: every routing number is unique across providers (fw-mark-prio and
// from-prio share one namespace, since both select an ip rule by priority), no
// provider sits on a reserved table, and every weight is at least one. Nothing
// derives these numbers, so a typo in inventory has no other place to surface.
// A failure here stops the daemon before it writes anything to the kernel,
// which is the existing failure contract for a bad configuration.
func checkProviderSet(loaded *Config) error {
	reserved := make(map[int]string, len(loaded.ReservedTables)+len(kernelReservedTables))
	for _, table := range kernelReservedTables {
		reserved[table] = "the kernel"
	}
	for _, table := range loaded.ReservedTables {
		// The kernel's own reservation is the true reason a table is off limits,
		// so inventory redundantly listing it must not overwrite that label with
		// a less accurate one.
		if _, alreadyReserved := reserved[table]; !alreadyReserved {
			reserved[table] = "steering-group/reserved-tables"
		}
	}

	names := make([]string, 0, len(loaded.WAN))
	for name := range loaded.WAN {
		names = append(names, name)
	}
	slices.Sort(names)

	slots := []struct {
		leaf  string
		value func(entry config.IfMgrWANEntry) int
	}{
		{leaf: "table-id", value: func(entry config.IfMgrWANEntry) int { return entry.TableID }},
		{leaf: "fw-mark", value: func(entry config.IfMgrWANEntry) int { return entry.FwMark }},
	}
	for _, slot := range slots {
		owner := make(map[int]string, len(names))
		for _, name := range names {
			value := slot.value(loaded.WAN[name])
			if taken, seen := owner[value]; seen {
				return fmt.Errorf("wan %s: %s %d is already taken by wan %s",
					name, slot.leaf, value, taken)
			}
			owner[value] = name
		}
	}

	// fw-mark-prio and from-prio both select an ip rule by the same numeric
	// priority, and the routing module already treats them as one slot space,
	// so they are checked against one shared set rather than two independent
	// ones. That set catches a collision across providers and a collision
	// between a single provider's own two leaves, whichever leaf is checked
	// second.
	type priorityOwner struct {
		provider string
		leaf     string
	}
	priorityLeaves := []struct {
		leaf  string
		value func(entry config.IfMgrWANEntry) int
	}{
		{leaf: "fw-mark-prio", value: func(entry config.IfMgrWANEntry) int { return entry.FwMarkPrio }},
		{leaf: "from-prio", value: func(entry config.IfMgrWANEntry) int { return entry.FromPrio }},
	}
	priorityOwners := make(map[int]priorityOwner, len(names)*len(priorityLeaves))
	for _, name := range names {
		entry := loaded.WAN[name]
		for _, priorityLeaf := range priorityLeaves {
			value := priorityLeaf.value(entry)
			if taken, seen := priorityOwners[value]; seen {
				return fmt.Errorf("wan %s: %s %d is already taken by wan %s's %s",
					name, priorityLeaf.leaf, value, taken.provider, taken.leaf)
			}
			priorityOwners[value] = priorityOwner{provider: name, leaf: priorityLeaf.leaf}
		}
	}

	if err := checkMappedExternals(loaded, names); err != nil {
		return err
	}

	for _, name := range names {
		entry := loaded.WAN[name]
		if owner, isReserved := reserved[entry.TableID]; isReserved {
			return fmt.Errorf("wan %s: table-id %d is reserved by %s", name, entry.TableID, owner)
		}
		// The schema already ranges weight at 1..max, so libyang rejects a zero
		// before build ever runs, and no test through Load can reach this branch
		// today. It stays as a decode-boundary guard: checkProviderSet is the
		// one place that reasons about the whole provider set, and a schema
		// revision that drops or loosens the range must not silently let a
		// zero-weight provider through it.
		if entry.Weight < 1 {
			return fmt.Errorf("wan %s: steering/weight must be at least 1, got %d", name, entry.Weight)
		}
	}
	return nil
}

// buildStaticMappings parses one provider's static mappings. The schema types
// both addresses before this runs, so an address that does not parse means the
// document reached the loader unvalidated; it is refused rather than dropped.
func buildStaticMappings(label string, entries []staticMapping) ([]config.StaticMapping, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	mappings := make([]config.StaticMapping, 0, len(entries))
	for _, entry := range entries {
		external, err := netip.ParseAddr(entry.External)
		if err != nil {
			slog.Error("networkjson: static-mapping external unparsable",
				"wan", label, "external", entry.External, "err", err)
			return nil, fmt.Errorf("%s: static-mapping external %q: %w", label, entry.External, err)
		}
		internal, err := netip.ParseAddr(entry.Internal)
		if err != nil {
			slog.Error("networkjson: static-mapping internal unparsable",
				"wan", label, "internal", entry.Internal, "err", err)
			return nil, fmt.Errorf("%s: static-mapping internal %q: %w", label, entry.Internal, err)
		}
		if !external.Is4() || !internal.Is4() {
			return nil, fmt.Errorf("%s: static-mapping requires IPv4 addresses", label)
		}
		for _, existing := range mappings {
			if existing.External == external {
				return nil, fmt.Errorf("%s: static-mapping external %s is duplicated", label, external)
			}
		}
		mappings = append(mappings, config.StaticMapping{External: external, Internal: internal, Delivery: interfaceintent.AddressDelivery(entry.Delivery)})
	}
	return mappings, nil
}

// parseAddress parses one address leaf, naming the leaf when it fails.
func parseAddress(label string, leaf string, raw string) (netip.Addr, error) {
	parsed, err := netip.ParseAddr(raw)
	if err != nil {
		slog.Error("networkjson: address unparsable", "entry", label, "leaf", leaf, "value", raw, "err", err)
		return netip.Addr{}, fmt.Errorf("%s: %s %q: %w", label, leaf, raw, err)
	}
	return parsed, nil
}

func buildProvider(entry ifaceEntry) (config.IfMgrWANEntry, *config.IfMgrHealthWANSection, error) {
	provider := entry.WAN
	if provider.Name == "" {
		return config.IfMgrWANEntry{}, nil, fmt.Errorf("interface %q carries a provider with no name", entry.Name)
	}
	label := "wan " + provider.Name
	numbers := []struct {
		leaf  string
		value *int
	}{
		{leaf: "table-id", value: provider.TableID},
		{leaf: "fw-mark", value: provider.FwMark},
		{leaf: "fw-mark-prio", value: provider.FwMarkPrio},
		{leaf: "from-prio", value: provider.FromPrio},
	}
	for _, number := range numbers {
		if number.value == nil {
			return config.IfMgrWANEntry{}, nil, fmt.Errorf("%s: %s is required", label, number.leaf)
		}
	}
	if provider.V4Source != "" {
		return config.IfMgrWANEntry{}, nil, fmt.Errorf(
			"%s: v4-source is derived from the link's static ipv4 address and must not be typed", label)
	}
	tier, weight, err := buildSteering(label, entry.Steering)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	policyV4, err := buildTranslationV4(label+" ipv4", entry)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	policyV6, err := buildTranslationV6(label+" ipv6", entry)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	forcedDSCP := 0
	if provider.ForcedDSCP != nil {
		forcedDSCP = *provider.ForcedDSCP
	}
	routing := config.IfMgrWANEntry{
		ProviderName:  provider.Name,
		Iface:         entry.Name,
		TableID:       *provider.TableID,
		FwMark:        *provider.FwMark,
		FwMarkPrio:    *provider.FwMarkPrio,
		FromPrio:      *provider.FromPrio,
		TranslationV4: policyV4,
		TranslationV6: policyV6,
		// The caller fills the source pin from the link specification, which
		// buildLink builds after this returns.
		V4Source:         "",
		LinkFiles:        entry.LinkFiles,
		ForcedDSCP:       forcedDSCP,
		SelectionEnabled: entry.Steering.Enabled,
		Tier:             tier,
		Weight:           weight,
	}
	if provider.Health == nil {
		return routing, nil, nil
	}
	probe, err := buildHealth(label, provider.Health)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	return routing, probe, nil
}

// buildSteering reads one provider's tier and weight. The schema cannot require
// the container, because an interface carrying no provider must be free of it,
// and the schema's weight default fills only the served tree rather than the
// file this decodes. Both values are therefore required here: a provider with
// no tier cannot be placed in the failover order, and one with no weight cannot
// be given a share of its tier.
func buildSteering(label string, member *steering) (uint8, int, error) {
	if member == nil {
		return 0, 0, fmt.Errorf("%s: steering is required", label)
	}
	if member.Tier == nil {
		return 0, 0, fmt.Errorf("%s: steering/tier is required", label)
	}
	if member.Weight == nil {
		return 0, 0, fmt.Errorf("%s: steering/weight is required", label)
	}
	tier := *member.Tier
	if tier < 0 || tier > int(^uint8(0)) {
		return 0, 0, fmt.Errorf("%s: steering/tier %d is outside 0 to 255", label, tier)
	}
	return uint8(tier), *member.Weight, nil
}

// buildHealth reads one provider's probe. Every setting of an enabled probe is
// required, matching the daemon's rule that an enabled provider fully specifies
// its policy rather than inheriting a module-wide default. A disabled probe is
// the second of the two ways a provider goes unprobed, beside carrying no
// health container at all. The daemon runs none of its settings, so it needs
// nothing beyond the flag, but it keeps every setting the file carries: the
// management surface serves the probe from this section, and a setting dropped
// here would make the served tree disagree with the file.
func buildHealth(label string, probe *health) (*config.IfMgrHealthWANSection, error) {
	if probe.Enabled == nil {
		return nil, fmt.Errorf("%s: health/enabled is required", label)
	}
	section := &config.IfMgrHealthWANSection{
		Enabled:              *probe.Enabled,
		PingCount:            probe.PingCount,
		SuccessThreshold:     probe.SuccessThreshold,
		CheckIntervalSeconds: probe.CheckInterval,
		FailureThreshold:     probe.FailureThreshold,
		RecoveryThreshold:    probe.RecoveryThreshold,
		TargetsV4:            probe.TargetsV4,
		TargetsV6:            probe.TargetsV6,
		HTTPURLs:             probe.HTTPURLs,
	}
	if !section.Enabled {
		return section, nil
	}
	counts := []struct {
		leaf  string
		value *int
	}{
		{leaf: "ping-count", value: probe.PingCount},
		{leaf: "success-threshold", value: probe.SuccessThreshold},
		{leaf: "failure-threshold", value: probe.FailureThreshold},
		{leaf: "recovery-threshold", value: probe.RecoveryThreshold},
		{leaf: "check-interval", value: probe.CheckInterval},
	}
	for _, count := range counts {
		if count.value == nil {
			return nil, fmt.Errorf("%s: health/%s is required", label, count.leaf)
		}
	}
	return section, nil
}

// ApplyFrom loads the network configuration at path, validates it against the
// models in schemaDir, and writes it onto cfg. Every process that reads the
// network tree goes through here rather than repeating the sequence, so one
// file owns each value and one implementation decides what a bad file means.
// cfg is left untouched when the load fails, so a caller that carries on with a
// diagnostic never shows a half-filled tree.
func ApplyFrom(cfg *config.Config, path string, schemaDir string) error {
	loaded, err := Load(path, schemaDir)
	if err != nil {
		return err
	}
	loaded.Apply(cfg)
	return nil
}

// ApplyDefault applies the network configuration from the paths the deploy
// installs.
func ApplyDefault(cfg *config.Config) error {
	return ApplyFrom(cfg, DefaultPath, DefaultSchemaDir)
}

// Apply writes the loaded tree onto cfg, filling the fields the TOML sections
// filled before this file owned them. The health and routes sections keep the
// filesystem paths TOML still owns. Apply writes only the network values.
func (c *Config) Apply(cfg *config.Config) {
	cfg.IfMgr.Firewall = c.Firewall
	cfg.IfMgr.PinnedConnectionID = c.PinnedConnectionID
	cfg.IfMgr.InternalPrefix = c.InternalPrefix
	cfg.IfMgr.OpnsenseEdgeV6 = c.OpnsenseEdgeV6
	cfg.IfMgr.MwanbrEdgeV6 = c.MwanbrEdgeV6
	cfg.IfMgr.HashMode = c.HashMode
	cfg.IfMgr.ReservedTables = c.ReservedTables
	cfg.IfMgr.WAN = c.WAN
	cfg.IfMgr.ConnectionIDs = c.ConnectionIDs
	cfg.IfMgr.ExplicitConnectionIDs = c.ExplicitConnectionIDs
	cfg.IfMgr.Connections = c.Connections

	if cfg.IfMgr.Modules.WAN == nil {
		cfg.IfMgr.Modules.WAN = &config.IfMgrModulesWANSection{Routes: nil}
	}
	if cfg.IfMgr.Modules.WAN.Routes == nil {
		cfg.IfMgr.Modules.WAN.Routes = &config.IfMgrWANRoutesSection{
			InternalIface:   "",
			InternalNetV4:   "",
			HealthStateFile: "",
		}
	}
	cfg.IfMgr.Modules.WAN.Routes.InternalIface = c.InternalIface
	cfg.IfMgr.Modules.WAN.Routes.InternalNetV4 = c.InternalNetV4

	if cfg.IfMgr.Modules.Health == nil {
		cfg.IfMgr.Modules.Health = &config.IfMgrHealthSection{
			StateFile:          "",
			StatusPushCID:      0,
			StatusPushPort:     0,
			ProbeTimeoutMillis: 0,
			WAN:                nil,
		}
	}
	cfg.IfMgr.Modules.Health.ProbeTimeoutMillis = c.ProbeTimeoutMillis
	cfg.IfMgr.Modules.Health.WAN = c.Health
}
