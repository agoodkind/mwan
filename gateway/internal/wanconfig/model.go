package wanconfig

import (
	"net/netip"

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
	SelectionEnabled *bool
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
	LinkFiles   string
	BGPSessions []config.BGPSession
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
	GuestType          config.GuestType
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
