// Package networkjson loads the gateway's network configuration: the provider
// inventory, each provider's routing slots, translation prefix, source pin,
// static mappings, and health probe, and the group-wide translation, internal
// link, and probe timeout. The file is written in the model's own JSON encoding and validated
// against the installed schema before any value is read, so the file the daemon
// loads and the tree the management surface serves describe one thing.
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
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	WAN      *wan      `json:"goodkind-mwan-steering:wan"`
	Steering *steering `json:"goodkind-mwan-steering:steering"`
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
	Name       string  `json:"name"`
	TableID    *int    `json:"table-id"`
	FwMark     *int    `json:"fw-mark"`
	FwMarkPrio *int    `json:"fw-mark-prio"`
	FromPrio   *int    `json:"from-prio"`
	NptPrefix  string  `json:"npt-prefix"`
	V4Source   string  `json:"v4-source"`
	ForcedDSCP *int    `json:"forced-dscp"`
	Health     *health `json:"health"`

	StaticMappings []staticMapping `json:"static-mapping"`
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
}

// Load reads path, validates it against the models in schemaDir, and returns
// the network tree it carries. Every failure is fatal to startup: an unreadable
// file, a file the schema rejects, and a missing value are all configuration
// errors, and none of them has a safe default.
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
		// build returns both a missing required value and a provider-set
		// conflict (a duplicate routing number, a reserved table), so the log
		// line names neither specifically; the wrapped error text below carries
		// the detail.
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
		routing, probe, err := buildProvider(entry)
		if err != nil {
			return nil, err
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
		return nil, errors.New("no interface carries a provider")
	}
	if err := checkProviderSet(loaded); err != nil {
		return nil, err
	}
	return loaded, nil
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
		for _, mapping := range loaded.WAN[name].StaticMappings {
			if taken, seen := mappedBy[mapping.External]; seen {
				return fmt.Errorf("wan %s: static-mapping external %s is already mapped by wan %s",
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
		mappings = append(mappings, config.StaticMapping{External: external, Internal: internal})
	}
	return mappings, nil
}

// buildProvider turns one interface's provider entry into the two sections the
// daemon holds it in: the routing entry keyed by provider name, and the health
// policy under the same name. A nil probe is how a provider the gateway does
// not probe is expressed, which is one of the two ways the daemon already
// declines to probe a provider. An empty npt-prefix is how a provider on an
// IPv4-only link is expressed: it gets no IPv6 source rule and no translation
// instance, and the modules that read the prefix already guard for it.
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
	tier, weight, err := buildSteering(label, entry.Steering)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	mappings, err := buildStaticMappings(label, provider.StaticMappings)
	if err != nil {
		return config.IfMgrWANEntry{}, nil, err
	}
	forcedDSCP := 0
	if provider.ForcedDSCP != nil {
		forcedDSCP = *provider.ForcedDSCP
	}
	routing := config.IfMgrWANEntry{
		Iface:          entry.Name,
		TableID:        *provider.TableID,
		FwMark:         *provider.FwMark,
		FwMarkPrio:     *provider.FwMarkPrio,
		FromPrio:       *provider.FromPrio,
		NptPrefix:      provider.NptPrefix,
		V4Source:       provider.V4Source,
		ForcedDSCP:     forcedDSCP,
		Tier:           tier,
		Weight:         weight,
		StaticMappings: mappings,
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
// filesystem paths TOML still carries, so only the network values are written.
func (c *Config) Apply(cfg *config.Config) {
	cfg.IfMgr.InternalPrefix = c.InternalPrefix
	cfg.IfMgr.OpnsenseEdgeV6 = c.OpnsenseEdgeV6
	cfg.IfMgr.MwanbrEdgeV6 = c.MwanbrEdgeV6
	cfg.IfMgr.HashMode = c.HashMode
	cfg.IfMgr.ReservedTables = c.ReservedTables
	cfg.IfMgr.WAN = c.WAN

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
			PersistStateFile:   "",
			StatusPushCID:      0,
			StatusPushPort:     0,
			ProbeTimeoutMillis: 0,
			WAN:                nil,
		}
	}
	cfg.IfMgr.Modules.Health.ProbeTimeoutMillis = c.ProbeTimeoutMillis
	cfg.IfMgr.Modules.Health.WAN = c.Health
}
