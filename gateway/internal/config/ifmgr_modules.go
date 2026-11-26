package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"goodkind.io/mwan/gateway/interfaceintent"
)

// IfMgrModulesSection is the explicit TOML schema for [ifmgr.modules].
// Each field maps to one supported module table.
type IfMgrModulesSection struct {
	WAN               *IfMgrModulesWANSection        `toml:"wan"`
	Health            *IfMgrHealthSection            `toml:"health"`
	Links             *IfMgrLinksSection             `toml:"links"`
	Autoconfiguration *IfMgrAutoconfigurationSection `toml:"autoconfiguration"`
	Addresses         *IfMgrAddressesSection         `toml:"addresses"`
	Resolver          *IfMgrResolverSection          `toml:"resolver"`
	BGPSessions       *IfMgrBGPSessionsSection       `toml:"bgp_sessions"`
}

// IfMgrBGPSessionsSection configures the durable journal of the external BGP sessions that installed kernel routes.
type IfMgrBGPSessionsSection struct {
	StateFile string `toml:"state_file"`
}

// IfMgrResolverSection configures the durable per-link static DNS and domain journal.
type IfMgrResolverSection struct {
	StateFile string `toml:"state_file"`
}

// IfMgrAutoconfigurationSection configures the durable per-link kernel policy journal.
type IfMgrAutoconfigurationSection struct {
	StateFile string `toml:"state_file"`
}

// IfMgrLinksSection configures durable ownership records for MWAN-owned links.
// The state file must persist across daemon restarts.
type IfMgrLinksSection struct {
	StateFile string `toml:"state_file"`
}

// IfMgrAddressesSection configures the durable static address and route journal.
type IfMgrAddressesSection struct {
	StateFile string `toml:"state_file"`
}

// IfMgrModulesWANSection is the [ifmgr.modules.wan] table. It nests the
// wan.routes module config under the key `routes`. The module renders as
// [ifmgr.modules.wan.routes].
type IfMgrModulesWANSection struct {
	Routes *IfMgrWANRoutesSection `toml:"routes"`
}

// IfMgrWANEntry stores one connection routing policy, keyed by connection ID.
// It retains the provider display name, interface, routing slots, steering
// settings, and per-family translation policy. Shared translation values
// remain on IfMgrSection.
type IfMgrWANEntry struct {
	SelectionEnabled *bool
	ProviderName     string
	Iface            string
	TableID          int
	FwMark           int
	FwMarkPrio       int
	FromPrio         int
	TranslationV4    *IPv4Translation
	TranslationV6    *IPv6Translation
	// V4Source is the provider's static IPv4 link address, or empty on a
	// leased link. The loader sets it from the link's first static address. The
	// source rule and link address must match.
	V4Source  string
	LinkFiles string
	// ForcedDSCP is a DSCP value that selects this provider for new flows.
	// Zero means the provider has no forced-DSCP rule. The schema restricts
	// configured values to 1 through 63.
	ForcedDSCP int
	// Tier is the preference tier. The lowest-numbered tier with at least
	// one healthy provider is the tier that receives new connections.
	Tier uint8
	// Weight is this provider's share of its tier, at least one. The loader
	// refuses a missing or smaller value rather than defaulting it, because a
	// zero share would make the balancer's divisor wrong.
	Weight      int
	BGPSessions []BGPSession
}

// ConnectionSelectionEnabled treats omitted selection permission as enabled.
func ConnectionSelectionEnabled(enabled *bool) bool {
	return enabled == nil || *enabled
}

// TranslationMode selects the base packet translation for one address family.
type TranslationMode string

const (
	// TranslationNative leaves addresses unchanged.
	TranslationNative TranslationMode = "native"
	// TranslationNAPT44 translates IPv4 source addresses and ports.
	TranslationNAPT44 TranslationMode = "ietf-nat:napt44"
	// TranslationNPTv6 translates IPv6 prefixes without connection state.
	TranslationNPTv6 TranslationMode = "ietf-nat:nptv6"
	// TranslationMasquerade translates IPv6 sources to the outgoing address.
	TranslationMasquerade TranslationMode = "masquerade"
)

// PrefixSource selects how an IPv6 external prefix is obtained.
type PrefixSource string

const (
	// PrefixConfigured uses the external prefix from the policy.
	PrefixConfigured PrefixSource = "configured"
	// PrefixDelegated uses a prefix from DHCPv6 delegation.
	PrefixDelegated PrefixSource = "delegated"
)

// IPv4Translation configures the IPv4 base mode and optional static mappings.
type IPv4Translation struct {
	Mode           TranslationMode
	StaticMappings []StaticMapping
}

// IPv6Translation configures the IPv6 base mode and optional NPTv6 settings.
type IPv6Translation struct {
	Mode TranslationMode
	NPT  *NPTv6Translation
}

// PolicySource provides the values callers pass to interfaceintent.NewPolicyProvider.
// The returned prefix is unmasked; interfaceintent.IPv6PolicySource masks the prefix.
// A nil receiver is valid.
func (t *IPv6Translation) PolicySource() (bool, bool, netip.Prefix) {
	if t == nil || t.Mode != TranslationNPTv6 || t.NPT == nil {
		return false, false, netip.Prefix{}
	}

	return true, t.NPT.ExternalSource == PrefixConfigured, t.NPT.ExternalPrefix
}

// NPTv6Translation configures internal and external prefix selection.
type NPTv6Translation struct {
	InternalPrefix netip.Prefix
	ExternalSource PrefixSource
	ExternalPrefix netip.Prefix
	ExpectedPrefix netip.Prefix
}

// StaticMapping defines an IPv4 translation for one provider. The provider
// translates traffic for External to Internal and translates traffic from
// Internal to External.
type StaticMapping struct {
	External netip.Addr
	Internal netip.Addr
	Delivery interfaceintent.AddressDelivery
}

// IfMgrWANRoutesSection is the [ifmgr.modules.wan.routes] table. The health
// state file is a filesystem path and stays in TOML; the internal link and
// network are network values and come from network.json.
type IfMgrWANRoutesSection struct {
	InternalIface   string `toml:"-"`
	InternalNetV4   string `toml:"-"`
	HealthStateFile string `toml:"health_state_file"`
}

// IfMgrHealthSection defines the module runtime state-file path and observation
// settings.
type IfMgrHealthSection struct {
	ObservationSettingsFile string                           `toml:"observation_settings_file"`
	StateFile               string                           `toml:"state_file"`
	MaxStateAge             string                           `toml:"max_state_age"`
	ProbeTimeoutMillis      int                              `toml:"-"`
	WAN                     map[string]IfMgrHealthWANSection `toml:"-"`
}

// ParseMaxStateAge returns a false presence flag when MaxStateAge is empty. It
// returns the parsed duration and a true presence flag for positive values.
// Invalid or nonpositive values return an error.
func (section IfMgrHealthSection) ParseMaxStateAge() (time.Duration, bool, error) {
	if section.MaxStateAge == "" {
		return 0, false, nil
	}

	maxAge, err := time.ParseDuration(section.MaxStateAge)
	if err != nil {
		slog.Warn("health max_state_age rejected", "value", section.MaxStateAge, "err", err)
		return 0, true, fmt.Errorf("max_state_age %q: %w", section.MaxStateAge, err)
	}

	if maxAge <= 0 {
		slog.Warn("health max_state_age rejected", "value", section.MaxStateAge)

		return 0, true, fmt.Errorf(
			"max_state_age %q must be greater than zero",
			section.MaxStateAge,
		)
	}

	return maxAge, true, nil
}

// IfMgrHealthWANSection is one provider's probe policy, read from network.json.
// The interval uses seconds because that is the unit in the model.
//
// The counts and the interval are pointers. A disabled probe retains every
// setting from its file, and the management surface serves those values. A nil
// value marks a leaf the file omitted. Zero cannot stand in for omission
// because the model accepts zero as a value. An enabled probe must define all
// five fields. The loader rejects a probe that omits any of them.
type IfMgrHealthWANSection struct {
	Enabled              bool
	PingCount            *int
	SuccessThreshold     *int
	CheckIntervalSeconds *int
	FailureThreshold     *int
	RecoveryThreshold    *int
	TargetsV4            []string
	TargetsV6            []string
	HTTPURLs             []string
}
