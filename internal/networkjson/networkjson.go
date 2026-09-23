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
	"goodkind.io/mwan/internal/networkd"
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
	Name      string             `json:"name"`
	Type      string             `json:"type"`
	LinkFiles string             `json:"goodkind-mwan-steering:link-files"`
	Link      *linkIdentity      `json:"goodkind-mwan-steering:link"`
	Networkd  *networkdContainer `json:"goodkind-mwan-steering:networkd"`
	IPv4      *familyV4          `json:"ietf-ip:ipv4"`
	IPv6      *familyV6          `json:"ietf-ip:ipv6"`
	WAN       *wan               `json:"goodkind-mwan-steering:wan"`
	Steering  *steering          `json:"goodkind-mwan-steering:steering"`
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
	VLAN            *linkVLAN  `json:"vlan"`
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
	Forwarding  *bool              `json:"forwarding"`
	Address     []ipAddress        `json:"address"`
	DHCP        *bool              `json:"goodkind-mwan-steering:dhcp"`
	Gateway     string             `json:"goodkind-mwan-steering:gateway"`
	RouteMetric *uint32            `json:"goodkind-mwan-steering:route-metric"`
	Translation *familyTranslation `json:"goodkind-mwan-steering:translation"`
}

type familyV4 struct {
	familyWire
	SourceAddresses []string `json:"goodkind-mwan-steering:source-addresses"`
}

type familyV6 struct {
	familyWire
	AcceptRA   *bool       `json:"goodkind-mwan-steering:accept-ra"`
	Delegation *delegation `json:"goodkind-mwan-steering:delegation"`
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
}

// steering is the member's steering properties: which tier it sits in and how
// much of that tier's traffic it takes. Both are pointers so an absent leaf is
// distinguishable from a zero the daemon would act on. Tier zero is the
// preferred tier, and weight zero would make the balancer's divisor wrong.
type steering struct {
	Tier   *int `json:"tier"`
	Weight *int `json:"weight"`
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
	HashMode       string      `json:"hash-mode"`
	ReservedTables []int       `json:"reserved-tables"`
	Translation    translation `json:"translation"`
	Routes         routes      `json:"routes"`
	Health         groupHealth `json:"health"`
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
	InternalPrefix     string
	OpnsenseEdgeV6     string
	MwanbrEdgeV6       string
	InternalIface      string
	InternalNetV4      string
	HashMode           string
	ReservedTables     []int
	ProbeTimeoutMillis int
	WAN                map[string]config.IfMgrWANEntry
	Health             map[string]config.IfMgrHealthWANSection
	// Links are the link specifications of the providers whose unit files the
	// daemon renders, in document order. A provider whose entry names its
	// files hand-authored contributes none.
	Links []networkd.Spec
	// Rejected lists every provider entry the loader refused, in document
	// order. A rejected provider is absent from WAN, Health, and Links, and the
	// daemon runs without it.
	Rejected []Rejection
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
	loaded := &Config{
		InternalPrefix:     group.Translation.InternalPrefix,
		OpnsenseEdgeV6:     group.Translation.OpnsenseEdgeV6,
		MwanbrEdgeV6:       group.Translation.MwanbrEdgeV6,
		InternalIface:      group.Routes.InternalIface,
		InternalNetV4:      group.Routes.InternalNetV4,
		HashMode:           group.HashMode,
		ReservedTables:     group.ReservedTables,
		ProbeTimeoutMillis: 0,
		WAN:                make(map[string]config.IfMgrWANEntry, len(doc.Interfaces.Interface)),
		Health:             make(map[string]config.IfMgrHealthWANSection, len(doc.Interfaces.Interface)),
		Links:              nil,
		Rejected:           nil,
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

	// The firewall renders one forced-DSCP rule per provider in list order, and
	// a later rule overwrites an earlier rule's mark, so a shared value would
	// silently send every tagged flow to the last provider that claims it.
	dscpOwners := make(map[int]string, len(doc.Interfaces.Interface))
	for _, entry := range doc.Interfaces.Interface {
		if entry.WAN == nil {
			continue
		}
		// A defect inside one entry rejects that entry alone. Every check below
		// this point compares entries with each other. A collision has no
		// single bad entry, and those checks stay fatal.
		routing, probe, err := buildProvider(entry)
		if err != nil {
			loaded.Rejected = append(loaded.Rejected, rejectEntry(entry, err))
			continue
		}
		spec, err := buildLink(entry, routing.TableID)
		if err != nil {
			loaded.Rejected = append(loaded.Rejected, rejectEntry(entry, err))
			continue
		}
		if spec != nil {
			routing.V4Source = staticV4Source(*spec)
			loaded.Links = append(loaded.Links, *spec)
		}
		if _, seen := loaded.WAN[entry.WAN.Name]; seen {
			return nil, fmt.Errorf("provider %q appears on more than one interface", entry.WAN.Name)
		}
		if entry.WAN.ForcedDSCP != nil {
			value := *entry.WAN.ForcedDSCP
			if owner, taken := dscpOwners[value]; taken {
				return nil, fmt.Errorf("wan %s: forced-dscp %d is already taken by wan %s",
					entry.WAN.Name, value, owner)
			}
			dscpOwners[value] = entry.WAN.Name
		}
		loaded.WAN[entry.WAN.Name] = routing
		if probe != nil {
			loaded.Health[entry.WAN.Name] = *probe
		}
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
	return loaded, nil
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

// checkMappedExternals refuses an external address that two providers map. The
// schema keys the list inside one provider and cannot see another provider's
// list, and two providers translating one address would each hold it on any
// link it is on-link for, so which link carries it would be undefined. names
// is sorted, so the error always names the same pair.
func checkMappedExternals(loaded *Config, names []string) error {
	mappedBy := make(map[netip.Addr]string, len(names))
	for _, name := range names {
		policy := loaded.WAN[name].TranslationV4
		if policy == nil {
			continue
		}
		for _, mapping := range policy.StaticMappings {
			if taken, seen := mappedBy[mapping.External]; seen {
				return fmt.Errorf("wan %s ipv4: static-mapping external %s is already mapped by wan %s",
					name, mapping.External, taken)
			}
			mappedBy[mapping.External] = name
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
		mappings = append(mappings, config.StaticMapping{External: external, Internal: internal})
	}
	return mappings, nil
}

// buildLink turns one provider interface's link identity into the
// specification the daemon renders its unit files from. The link-files leaf
// decides: hand-authored means the repository carries the files and the entry
// describes nothing about the link, so it yields no specification and must
// carry no link, family addressing, or free-form data the daemon would ignore.
// Translation policies and DHCPv6 delegation metadata remain explicit for
// hand-authored links without giving the daemon ownership of their files.
// Rendered means every
// requirement the schema cannot express is checked here. An entry that states
// neither fails, so a link container someone forgot to write is caught rather
// than read as an exemption. The schema already accepts every value, so a
// value that does not parse means the document reached the loader
// unvalidated; it is refused rather than dropped.
func buildLink(entry ifaceEntry, tableID int) (*networkd.Spec, error) {
	label := "interface " + entry.Name
	if entry.LinkFiles == linkFilesHandAuthored {
		if entry.Link != nil || entry.Networkd != nil || handAuthoredAddressing(entry) {
			return nil, fmt.Errorf(
				"%s: link-files is hand-authored, so it must carry no link, networkd, or family addressing",
				label)
		}
		return nil, nil
	}
	if entry.LinkFiles == "" {
		return nil, fmt.Errorf("%s: link-files is required: rendered with a link container, or hand-authored", label)
	}
	if entry.LinkFiles != linkFilesRendered {
		// The schema's enumeration refuses any other value before build runs.
		// This stays as a decode-boundary guard for a schema revision that
		// adds a value the loader does not know.
		return nil, fmt.Errorf("%s: link-files %q is neither rendered nor hand-authored", label, entry.LinkFiles)
	}
	if entry.Link == nil {
		return nil, fmt.Errorf("%s: link-files is rendered, so a link container is required", label)
	}
	spec := networkd.Spec{
		Name:            entry.Name,
		TableID:         tableID,
		Match:           networkd.Match{Driver: "", HardwareAddress: ""},
		HardwareAddress: entry.Link.HardwareAddress,
		VLAN:            nil,
		IPv4:            nil,
		IPv6:            nil,
		Files:           nil,
	}
	if entry.Link.Match != nil {
		spec.Match = networkd.Match{
			Driver:          entry.Link.Match.Driver,
			HardwareAddress: entry.Link.Match.HardwareAddress,
		}
	}
	identities := 0
	if spec.Match.Driver != "" {
		identities++
	}
	if spec.Match.HardwareAddress != "" {
		identities++
	}
	if entry.Link.VLAN != nil {
		identities++
		vlan, err := buildVLAN(label, *entry.Link.VLAN)
		if err != nil {
			return nil, err
		}
		spec.VLAN = vlan
	}
	if identities != 1 {
		return nil, fmt.Errorf(
			"%s: link must set exactly one of match/driver, match/hardware-address, or vlan, got %d",
			label, identities)
	}
	if entry.IPv4 != nil {
		ipv4, err := buildFamilyV4(label, *entry.IPv4)
		if err != nil {
			return nil, err
		}
		spec.IPv4 = ipv4
	}
	if entry.IPv6 != nil {
		ipv6, err := buildFamilyV6(label, *entry.IPv6)
		if err != nil {
			return nil, err
		}
		spec.IPv6 = ipv6
	}
	if entry.Networkd != nil {
		files, err := buildFreeForm(label, *entry.Networkd)
		if err != nil {
			return nil, err
		}
		spec.Files = files
	}
	if err := networkd.Validate(spec); err != nil {
		slog.Error("networkjson: free-form section sets a typed key", "interface", entry.Name, "err", err)
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return &spec, nil
}

// buildVLAN reads the VLAN container. The parent is an interface reference
// the schema resolves against the document's own interface list, so a parent
// no entry describes fails Load at validation, before build runs: a VLAN is
// created by a line in the parent's own file, and a parent nothing describes
// creates nothing.
func buildVLAN(label string, vlan linkVLAN) (*networkd.VLAN, error) {
	if vlan.ID == nil {
		return nil, fmt.Errorf("%s: link/vlan/id is required", label)
	}
	return &networkd.VLAN{Parent: vlan.Parent, ID: *vlan.ID}, nil
}

// buildFamily reads the leaves both families share. dhcp is required, because
// a family container that does not say whether a client runs cannot be
// rendered either way.
func buildFamily(label string, family string, wire familyWire) (networkd.Family, error) {
	var none networkd.Family
	if wire.DHCP == nil {
		return none, fmt.Errorf("%s: %s/dhcp is required", label, family)
	}
	built := networkd.Family{
		Forwarding:  wire.Forwarding,
		Addresses:   nil,
		DHCP:        wire.DHCP,
		Gateway:     netip.Addr{},
		RouteMetric: wire.RouteMetric,
	}
	for _, address := range wire.Address {
		ip, err := parseAddress(label, family+"/address", address.IP)
		if err != nil {
			return none, err
		}
		if address.PrefixLength == nil {
			return none, fmt.Errorf("%s: %s/address %s carries no prefix-length", label, family, address.IP)
		}
		built.Addresses = append(built.Addresses, networkd.Address{IP: ip, PrefixLength: *address.PrefixLength})
	}
	if wire.Gateway != "" {
		parsed, err := parseAddress(label, family+"/gateway", wire.Gateway)
		if err != nil {
			return none, err
		}
		built.Gateway = parsed
	}
	return built, nil
}

// buildFamilyV4 reads the IPv4 container, including the source addresses the
// routing module pins beside the link's own.
func buildFamilyV4(label string, wire familyV4) (*networkd.FamilyV4, error) {
	shared, err := buildFamily(label, "ipv4", wire.familyWire)
	if err != nil {
		return nil, err
	}
	built := &networkd.FamilyV4{Family: shared, SourceAddresses: nil}
	for _, raw := range wire.SourceAddresses {
		parsed, err := parseAddress(label, "ipv4/source-addresses", raw)
		if err != nil {
			return nil, err
		}
		built.SourceAddresses = append(built.SourceAddresses, parsed)
	}
	return built, nil
}

// buildFamilyV6 reads the IPv6 container, including the delegation the
// interface requests.
func buildFamilyV6(label string, wire familyV6) (*networkd.FamilyV6, error) {
	shared, err := buildFamily(label, "ipv6", wire.familyWire)
	if err != nil {
		return nil, err
	}
	built := &networkd.FamilyV6{Family: shared, AcceptRA: wire.AcceptRA, Delegation: nil}
	if wire.Delegation == nil {
		return built, nil
	}
	built.Delegation = &networkd.Delegation{
		Hint:                  netip.Prefix{},
		DUIDType:              wire.Delegation.DUIDType,
		DUID:                  wire.Delegation.DUID,
		WithoutRA:             wire.Delegation.WithoutRA,
		UseDelegatedPrefix:    wire.Delegation.UseDelegatedPrefix,
		RouterLifetimeSeconds: wire.Delegation.RouterLifetimeSeconds,
	}
	if wire.Delegation.Hint != "" {
		hint, err := netip.ParsePrefix(wire.Delegation.Hint)
		if err != nil {
			slog.Error("networkjson: delegation hint unparsable",
				"entry", label, "hint", wire.Delegation.Hint, "err", err)
			return nil, fmt.Errorf("%s: ipv6/delegation/hint %q: %w", label, wire.Delegation.Hint, err)
		}
		built.Delegation.Hint = hint
	}
	return built, nil
}

// buildFreeForm reads the networkd container in document order. Both indexes
// are keys the schema requires, so an absent one means the document reached
// the loader unvalidated.
func buildFreeForm(label string, container networkdContainer) ([]networkd.File, error) {
	files := make([]networkd.File, 0, len(container.Files))
	for _, file := range container.Files {
		built := networkd.File{Kind: networkd.FileKind(file.Kind), Sections: nil}
		for _, section := range file.Sections {
			if section.Index == nil {
				return nil, fmt.Errorf("%s: networkd file %s carries a section with no index", label, file.Kind)
			}
			builtSection := networkd.Section{Index: *section.Index, Name: section.Name, Entries: nil}
			for _, entry := range section.Entries {
				if entry.Index == nil {
					return nil, fmt.Errorf("%s: networkd file %s section %s carries an entry with no index",
						label, file.Kind, section.Name)
				}
				builtSection.Entries = append(builtSection.Entries,
					networkd.Entry{Index: *entry.Index, Key: entry.Key, Value: entry.Value})
			}
			built.Sections = append(built.Sections, builtSection)
		}
		files = append(files, built)
	}
	return files, nil
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

// staticV4Source is the IPv4 source pin a rendered link carries: its first
// static address, or empty on a link that leases its address. The routing
// module pins traffic the gateway sources from that address to the provider's
// table, so the pin is the address the link holds rather than a second value
// inventory could let drift from it.
func staticV4Source(spec networkd.Spec) string {
	if spec.IPv4 == nil || len(spec.IPv4.Addresses) == 0 {
		return ""
	}
	return spec.IPv4.Addresses[0].IP.String()
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
		Iface:         entry.Name,
		TableID:       *provider.TableID,
		FwMark:        *provider.FwMark,
		FwMarkPrio:    *provider.FwMarkPrio,
		FromPrio:      *provider.FromPrio,
		TranslationV4: policyV4,
		TranslationV6: policyV6,
		// The caller fills the source pin from the link specification, which
		// buildLink builds after this returns.
		V4Source:   "",
		LinkFiles:  entry.LinkFiles,
		ForcedDSCP: forcedDSCP,
		Tier:       tier,
		Weight:     weight,
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
	cfg.IfMgr.InternalPrefix = c.InternalPrefix
	cfg.IfMgr.OpnsenseEdgeV6 = c.OpnsenseEdgeV6
	cfg.IfMgr.MwanbrEdgeV6 = c.MwanbrEdgeV6
	cfg.IfMgr.HashMode = c.HashMode
	cfg.IfMgr.ReservedTables = c.ReservedTables
	cfg.IfMgr.WAN = c.WAN
	cfg.IfMgr.Links = c.Links

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
