package config

import "net/netip"

// IfMgrModulesSection is the explicit TOML schema for [ifmgr.modules].
// Each field maps to one supported module table.
type IfMgrModulesSection struct {
	WG                *IfMgrWGHealthSection          `toml:"wg"`
	OOBV6             *IfMgrOOBV6Section             `toml:"oobv6"`
	OOBV4             *IfMgrOOBV4Section             `toml:"oobv4"`
	SLAACHealth       *IfMgrSLAACHealthSection       `toml:"slaac_health"`
	RALost            *IfMgrRALostSection            `toml:"ra_lost"`
	ConnectivityProbe *IfMgrConnectivityProbeSection `toml:"connectivity_probe"`
	BridgeProbe       *IfMgrBridgeProbeSection       `toml:"bridge_probe"`
	CloudflaredTap    *IfMgrCloudflaredTapSection    `toml:"cloudflared_tap"`
	MainV4            *IfMgrMainV4Section            `toml:"mainv4"`
	PolicyRules       *IfMgrPolicyRulesSection       `toml:"policy_rules"`
	HostIPv6Policy    *IfMgrHostIPv6PolicySection    `toml:"host_ipv6_policy"`
	WAN               *IfMgrModulesWANSection        `toml:"wan"`
	Health            *IfMgrHealthSection            `toml:"health"`
}

// IfMgrModulesWANSection is the [ifmgr.modules.wan] table. It nests the
// wan.routes module config under the key `routes`, so the module renders as
// [ifmgr.modules.wan.routes].
type IfMgrModulesWANSection struct {
	Routes *IfMgrWANRoutesSection `toml:"routes"`
}

// IfMgrWGHealthSection is the [ifmgr.modules.wg] table. The module observes
// WireGuard peer handshake ages and alerts when one goes stale. SSHHost selects
// the mode: set, the module reads a remote host's peers over SSH; empty, it
// reads the local interface.
type IfMgrWGHealthSection struct {
	SSHHost           string   `toml:"ssh_host"`
	SSHPort           *int     `toml:"ssh_port"`
	IdentityFile      string   `toml:"identity_file"`
	Iface             string   `toml:"iface"`
	Sudo              bool     `toml:"sudo"`
	WarnHandshakeAge  string   `toml:"warn_handshake_age"`
	ErrorHandshakeAge string   `toml:"error_handshake_age"`
	Timeout           string   `toml:"timeout"`
	IgnorePeers       []string `toml:"ignore_peers"`
}

// IfMgrOOBV6Section is the [ifmgr.modules.oobv6] table. The module holds the
// static out-of-band IPv6 address on the interface and keeps the RA-learned
// default route in the out-of-band routing table, so out-of-band access
// survives a failure of the main route.
type IfMgrOOBV6Section struct {
	Iface                 string `toml:"iface"`
	OOBAddr               string `toml:"oob_addr"`
	OOBTableID            int    `toml:"oob_table_id"`
	ManageSLAACSourceRule *bool  `toml:"manage_slaac_source_rule"`
	SLAACRulePriority     *int   `toml:"slaac_rule_priority"`
}

// IfMgrOOBV4Section is the [ifmgr.modules.oobv4] table. The module puts the
// DHCP-learned default route into the out-of-band routing table, which is the
// IPv4 counterpart of what oobv6 does from router advertisements.
type IfMgrOOBV4Section struct {
	Iface      string `toml:"iface"`
	OOBTableID int    `toml:"oob_table_id"`
}

// IfMgrSLAACHealthSection is the [ifmgr.modules.slaac_health] table. The
// module detects a deprecated SLAAC address, sends a router solicitation, and
// escalates to toggling disable_ipv6 when solicitation does not restore the
// address. MaxTogglesPerHour bounds that last resort, because the toggle drops
// every IPv6 address on the interface.
type IfMgrSLAACHealthSection struct {
	Iface             string   `toml:"iface"`
	DegradedAfter     string   `toml:"degraded_after"`
	EscalateAfter     string   `toml:"escalate_after"`
	AlertAfter        string   `toml:"alert_after"`
	MaxTogglesPerHour *int     `toml:"max_toggles_per_hour"`
	ProbeTargetsV6    []string `toml:"probe_targets_v6"`
	ProbeTimeout      string   `toml:"probe_timeout"`
}

// IfMgrRALostSection is the [ifmgr.modules.ra_lost] table. The module alerts
// when no router advertisement has arrived on the interface for
// RALostAlertAfter, which is the first sign the upstream router stopped
// advertising.
type IfMgrRALostSection struct {
	Iface            string `toml:"iface"`
	RALostAlertAfter string `toml:"ra_lost_alert_after"`
}

// IfMgrConnectivityProbeSection is the [ifmgr.modules.connectivity_probe]
// table. The module pings each target every reconcile tick and alerts once any
// one of them has been failing for UnhealthyAfter. The delay debounces each
// target separately, so a single dropped probe does not alert while a target
// that stays down does.
type IfMgrConnectivityProbeSection struct {
	Iface          string   `toml:"iface"`
	TargetsV6      []string `toml:"targets_v6"`
	Timeout        string   `toml:"timeout"`
	UnhealthyAfter string   `toml:"unhealthy_after"`
}

// IfMgrBridgeProbeSection is the [ifmgr.modules.bridge_probe] table. The
// module alerts when no neighbour-discovery or DHCP traffic has arrived on the
// interface for NoSignalAlertAfter, which is what a dangling host-side veth
// looks like from inside the guest: the link stays up and nothing arrives.
type IfMgrBridgeProbeSection struct {
	Iface              string `toml:"iface"`
	NoSignalAlertAfter string `toml:"no_signal_alert_after"`
}

// IfMgrCloudflaredTapSection is the [ifmgr.modules.cloudflared_tap] table. The
// module reads the named unit's journal and re-emits its lines, downgrading
// those matching DowngradePatterns so routine cloudflared chatter does not
// read as an error.
type IfMgrCloudflaredTapSection struct {
	Unit              string   `toml:"unit"`
	DowngradePatterns []string `toml:"downgrade_patterns"`
	JournalctlPath    string   `toml:"journalctl_path"`
}

// IfMgrMainV4Section is the [ifmgr.modules.mainv4] table. The module puts the
// DHCP-learned IPv4 default route into the main routing table, and stays inert
// unless the named interface has DHCPv4 enabled.
type IfMgrMainV4Section struct {
	Iface string `toml:"iface"`
}

// IfMgrPolicyRulesSection is the [ifmgr.modules.policy_rules] table. The
// module reconciles the listed ip rules, which is how traffic from the
// cloudflared user and from the out-of-band source address is directed to the
// out-of-band table rather than the main one.
type IfMgrPolicyRulesSection struct {
	Rule []IfMgrPolicyRuleSection `toml:"rule"`
}

// IfMgrWANEntry is one provider's routing configuration, keyed by provider
// name. It comes from network.json: the interface the provider rides, the
// policy-routing slots wan.routes owns, and the steering properties the
// balancer reads. Each configured family has an explicit translation policy.
// The shared internal prefix and edge addresses live on
// IfMgrSection, because no single provider owns them.
type IfMgrWANEntry struct {
	Iface         string
	TableID       int
	FwMark        int
	FwMarkPrio    int
	FromPrio      int
	TranslationV4 *IPv4Translation
	TranslationV6 *IPv6Translation
	// V4Source is the provider's static IPv4 link address, or empty on a
	// leased link. The loader derives it from the link's first static address
	// rather than reading it from the file, so the source rule and the address
	// the link holds cannot disagree.
	V4Source string
	// LinkFiles says who writes the provider link's unit files, as the
	// link-files leaf spells it: rendered, when the daemon writes them from the
	// link specification on IfMgrSection.Links, or hand-authored, when the
	// repository carries them and the daemon writes nothing.
	LinkFiles string
	// ForcedDSCP is the DSCP value that forces a new flow onto this provider,
	// or zero when the provider carries none. Zero is free to mean absent
	// because the model ranges the leaf from 1, since every unmarked packet
	// carries zero.
	ForcedDSCP int
	// Tier is the preference tier. The lowest-numbered tier holding at least
	// one healthy provider is the tier that carries new connections.
	Tier uint8
	// Weight is this provider's share of its tier, at least one. The loader
	// refuses a missing or smaller value rather than defaulting it, because a
	// zero share would make the balancer's divisor wrong.
	Weight int
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

// NPTv6Translation configures internal and external prefix selection.
type NPTv6Translation struct {
	InternalPrefix netip.Prefix
	ExternalSource PrefixSource
	ExternalPrefix netip.Prefix
	ExpectedPrefix netip.Prefix
}

// StaticMapping is one one-to-one IPv4 translation a provider carries: traffic
// arriving on the provider's link for External is delivered to Internal, and
// Internal leaves that link as External.
type StaticMapping struct {
	External netip.Addr
	Internal netip.Addr
}

// IfMgrWANRoutesSection is the [ifmgr.modules.wan.routes] table. The health
// state file is a filesystem path and stays in TOML; the internal link and
// network are network values and come from network.json.
type IfMgrWANRoutesSection struct {
	InternalIface   string `toml:"-"`
	InternalNetV4   string `toml:"-"`
	HealthStateFile string `toml:"health_state_file"`
}

// IfMgrHealthSection keeps the module's runtime state-file path and the address
// it pushes its verdict to. Both stay in TOML, beside the probe timeout and the
// per-provider policy, which come from network.json. The push address is not a
// network value. It addresses two processes on one machine, which is why TOML
// owns it alongside state_file.
type IfMgrHealthSection struct {
	StateFile          string                           `toml:"state_file"`
	StatusPushCID      uint32                           `toml:"status_push_cid"`
	StatusPushPort     uint32                           `toml:"status_push_port"`
	ProbeTimeoutMillis int                              `toml:"-"`
	WAN                map[string]IfMgrHealthWANSection `toml:"-"`
}

// IfMgrHealthWANSection is one provider's probe policy, read from network.json.
// The interval is seconds because that is the unit the model carries it in.
//
// The counts and the interval are pointers because a disabled probe keeps
// whichever settings its file carries, and the management surface serves
// exactly those. A nil value is a leaf the file left out, which a zero could
// not express: zero is a value the model accepts. An enabled probe always
// carries all five, because the loader refuses one that does not.
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

// IfMgrHostIPv6PolicySection is the explicit TOML schema for
// [ifmgr.modules.host_ipv6_policy].
type IfMgrHostIPv6PolicySection struct {
	MissingIfaceGracePeriod string                            `toml:"missing_iface_grace_period"`
	Interface               []IfMgrHostIPv6PolicyIfaceSection `toml:"interface"`
}

// IfMgrHostIPv6PolicyIfaceSection is one [[ifmgr.modules.host_ipv6_policy.interface]]
// table in the config file.
type IfMgrHostIPv6PolicyIfaceSection struct {
	Name             string `toml:"name"`
	AcceptRA         int    `toml:"accept_ra"`
	AutoConf         bool   `toml:"autoconf"`
	AcceptRADefRtr   bool   `toml:"accept_ra_defrtr"`
	SolicitRA        bool   `toml:"solicit_ra"`
	CleanupRADefault bool   `toml:"cleanup_ra_default"`
}

// IfMgrPolicyRuleSection is one [[ifmgr.modules.policy_rules.rule]] entry: a
// single ip rule the module keeps in place. A rule selects traffic either by
// source address or by owning user, and names the table to consult, so From
// and the UID fields are alternatives rather than a pair.
type IfMgrPolicyRuleSection struct {
	Family   string `toml:"family"`
	Priority int    `toml:"priority"`
	From     string `toml:"from"`
	UIDRange string `toml:"uid_range"`
	UIDUser  string `toml:"uid_user"`
	Table    string `toml:"table"`
	TableID  int    `toml:"table_id"`
}
