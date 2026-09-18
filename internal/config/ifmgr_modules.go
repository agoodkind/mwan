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

type IfMgrOOBV6Section struct {
	Iface                 string `toml:"iface"`
	OOBAddr               string `toml:"oob_addr"`
	OOBTableID            int    `toml:"oob_table_id"`
	ManageSLAACSourceRule *bool  `toml:"manage_slaac_source_rule"`
	SLAACRulePriority     *int   `toml:"slaac_rule_priority"`
}

type IfMgrOOBV4Section struct {
	Iface      string `toml:"iface"`
	OOBTableID int    `toml:"oob_table_id"`
}

type IfMgrSLAACHealthSection struct {
	Iface             string   `toml:"iface"`
	DegradedAfter     string   `toml:"degraded_after"`
	EscalateAfter     string   `toml:"escalate_after"`
	AlertAfter        string   `toml:"alert_after"`
	MaxTogglesPerHour *int     `toml:"max_toggles_per_hour"`
	ProbeTargetsV6    []string `toml:"probe_targets_v6"`
	ProbeTimeout      string   `toml:"probe_timeout"`
}

type IfMgrRALostSection struct {
	Iface            string `toml:"iface"`
	RALostAlertAfter string `toml:"ra_lost_alert_after"`
}

type IfMgrConnectivityProbeSection struct {
	Iface          string   `toml:"iface"`
	TargetsV6      []string `toml:"targets_v6"`
	Timeout        string   `toml:"timeout"`
	UnhealthyAfter string   `toml:"unhealthy_after"`
}

type IfMgrBridgeProbeSection struct {
	Iface              string `toml:"iface"`
	NoSignalAlertAfter string `toml:"no_signal_alert_after"`
}

type IfMgrCloudflaredTapSection struct {
	Unit              string   `toml:"unit"`
	DowngradePatterns []string `toml:"downgrade_patterns"`
	JournalctlPath    string   `toml:"journalctl_path"`
}

type IfMgrMainV4Section struct {
	Iface string `toml:"iface"`
}

type IfMgrPolicyRulesSection struct {
	Rule []IfMgrPolicyRuleSection `toml:"rule"`
}

// IfMgrWANEntry is one provider's routing configuration, keyed by provider
// name. It comes from network.json: the interface the provider rides, the
// policy-routing slots wan.routes owns, and the steering properties the
// balancer reads. Modules read the fields they need; npt uses only the name and
// interface. The shared internal prefix and edge addresses live on
// IfMgrSection, because no single provider owns them.
type IfMgrWANEntry struct {
	Iface      string
	TableID    int
	FwMark     int
	FwMarkPrio int
	FromPrio   int
	NptPrefix  string
	V4Source   string
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
	// StaticMappings are the provider's one-to-one IPv4 translations, in the
	// order the configuration lists them.
	StaticMappings []StaticMapping
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

// IfMgrHealthSection keeps the module's two state-file paths and the address it
// pushes its verdict to, all of which stay in TOML, beside the probe timeout and
// the per-provider policy, which come from network.json. The push address is
// not a network value: it names a transport between two processes on one
// machine, so it belongs where state_file belongs.
type IfMgrHealthSection struct {
	StateFile          string                           `toml:"state_file"`
	PersistStateFile   string                           `toml:"persist_state_file"`
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

type IfMgrPolicyRuleSection struct {
	Family   string `toml:"family"`
	Priority int    `toml:"priority"`
	From     string `toml:"from"`
	UIDRange string `toml:"uid_range"`
	UIDUser  string `toml:"uid_user"`
	Table    string `toml:"table"`
	TableID  int    `toml:"table_id"`
}
