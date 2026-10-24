// Package config defines the mwan TOML configuration schema and loads it.
// Load applies the built-in defaults, reads the file, overlays the secrets
// held in the environment, and checks the dynamic BGP settings. Every other
// section is validated by the subsystem that reads it, at the point it is
// used.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/watchdogconfig"
)

// NetworkConfig omits provider lists because the gateway supplies verdicts.
type NetworkConfig = watchdogconfig.NetworkConfig

// EmailConfig holds email notification settings.
type EmailConfig = notify.EmailConfig

// OPNsenseSection holds OPNsense API credentials, endpoint, and its own BGP config.
// OPNsense is a BGP peer, not a speaker we control. Its BGP config is the inverse
// of the agent's: different router-id, different neighbor list.
type OPNsenseSection struct {
	URL                string      `toml:"url"`
	APIKey             string      `toml:"api_key"`
	APISecret          string      `toml:"api_secret"`
	Insecure           bool        `toml:"insecure"`
	NPTv6HairpinAlias  string      `toml:"nptv6_hairpin_alias"`
	NPTv6HairpinCAFile string      `toml:"nptv6_hairpin_ca_file"`
	GatewayNames       []string    `toml:"gateway_names"`
	BGP                OPNsenseBGP `toml:"bgp"`

	// SSHUser is the SSH login on OPNsense. OPNsense disables root SSH by
	// default and ships with an admin user that has wheel + NOPASSWD sudo.
	// Defaults to "agoodkind". OPNsense operations that need root are
	// wrapped with "sudo" automatically.
	SSHUser string `toml:"ssh_user"`
}

// OPNsenseBGP describes the BGP configuration to push to OPNsense via its API.
type OPNsenseBGP struct {
	RouterID         string                `toml:"router_id"`
	Neighbors        []OPNsenseBGPNeighbor `toml:"neighbors"`
	FirewallSourceV4 string                `toml:"firewall_source_v4"` // e.g. "10.250.250.0/29"
	FirewallSourceV6 string                `toml:"firewall_source_v6"` // e.g. "3d06:bad:b01:201::/64"
}

// OPNsenseBGPNeighbor is a BGP peer from OPNsense's perspective.
type OPNsenseBGPNeighbor struct {
	Address     string `toml:"address"`
	Description string `toml:"description"`
	Preference  string `toml:"preference"` // "primary" or "backup"
}

// BGPSection holds embedded GoBGP speaker configuration.
type BGPSection struct {
	Enabled           bool          `toml:"enabled"`
	ASN               uint32        `toml:"asn"`
	RouterID          string        `toml:"router_id"`
	NextHopV6         string        `toml:"next_hop_v6"` // IPv6 next-hop for announced routes (optional, defaults to RouterID)
	KeepaliveSeconds  uint32        `toml:"keepalive_seconds"`
	HoldSeconds       uint32        `toml:"hold_seconds"`
	ListenPort        int32         `toml:"listen_port"`
	Neighbors         []BGPNeighbor `toml:"neighbors"`
	NeighborsV6       []BGPNeighbor `toml:"neighbors_v6"`
	DynamicNeighbors  []string      `toml:"dynamic_neighbors"`
	LearnedRouteIface string        `toml:"learned_route_iface"`
	// UseWanconfig marks a host whose BGP route installer owns the
	// per-provider routing tables. Such a host reads the provider set from
	// /etc/mwan/network.json, the wanconfig network configuration, and refuses
	// to start without it. The failover container leaves it unset and owns the
	// main table only.
	UseWanconfig        bool                   `toml:"use_wanconfig"`
	Announce            BGPAnnounce            `toml:"announce"`
	GracefulRestart     BGPGracefulRestart     `toml:"graceful_restart"`
	ForwardingReadiness BGPForwardingReadiness `toml:"forwarding_readiness"`
}

// BGPForwardingReadiness configures the primary speaker's ifmgr readiness check.
type BGPForwardingReadiness struct {
	SocketPath               string `toml:"socket_path"`
	PollIntervalMilliseconds int    `toml:"poll_interval_milliseconds"`
	ReadTimeoutMilliseconds  int    `toml:"read_timeout_milliseconds"`
}

// PollInterval returns the configured readiness polling period.
func (r BGPForwardingReadiness) PollInterval() time.Duration {
	return time.Duration(r.PollIntervalMilliseconds) * time.Millisecond
}

// ReadTimeout returns the configured socket read deadline.
func (r BGPForwardingReadiness) ReadTimeout() time.Duration {
	return time.Duration(r.ReadTimeoutMilliseconds) * time.Millisecond
}

// BGPGracefulRestart configures BGP Graceful Restart (RFC 4724) on the
// embedded GoBGP speaker. When Enabled is true, the speaker negotiates
// the GR capability with each peer so that planned restarts of mwan-agent
// do not trigger immediate route withdrawal on the helper (OPNsense FRR).
//
// RestartTime is advertised to the helper as the maximum number of seconds
// the helper should hold our routes after the BGP session drops.
// NotificationEnabled mirrors the "N" bit (RFC 8538) so a
// NOTIFICATION-triggered session reset still preserves the GR semantics.
type BGPGracefulRestart struct {
	Enabled             bool   `toml:"enabled"`
	RestartTime         uint32 `toml:"restart_time"`
	NotificationEnabled bool   `toml:"notification_enabled"`
}

// BGPNeighbor identifies a single BGP peer.
type BGPNeighbor struct {
	Address string `toml:"address"`
}

// BGPAnnounce specifies prefixes to originate via BGP.
type BGPAnnounce struct {
	IPv4 []string `toml:"ipv4"`
	IPv6 []string `toml:"ipv6"`
}

// AgentSection holds agent-specific configuration.
//
// DeployExpected gates the "deploy-file-missing" warning emitted by
// GetConfigState when DeployFile cannot be read. The default is true,
// which preserves the historical behaviour for production and testbed
// hosts that are deployed by Ansible. Fresh hosts that have never been
// deployed should set this to false so the missing file is treated as
// steady state and no notify event is fired.
type AgentSection struct {
	VsockPort      uint32 `toml:"vsock_port"`
	TCPAddr        string `toml:"tcp_addr"`
	DeployFile     string `toml:"deploy_file"`
	DeployExpected bool   `toml:"deploy_expected"`
	LogFile        string `toml:"log_file"`
	Debug          bool   `toml:"debug"`
}

// GuestType selects qm for QEMU guests and pct for LXC guests.
type GuestType = watchdogconfig.GuestType

const (
	// GuestTypeQEMU selects qm for guest operations.
	GuestTypeQEMU = watchdogconfig.GuestTypeQEMU
	// GuestTypeLXC selects pct for guest operations.
	GuestTypeLXC = watchdogconfig.GuestTypeLXC
)

// Config is the single TOML configuration for the mwan monolith.
// Default path: /etc/mwan/config.toml, override with --config or MWAN_CONFIG env.
type Config struct {
	Path         string `toml:"-"`
	Hostname     string `toml:"hostname"`
	MwanVMID     string `toml:"mwan_vmid"`
	MwanMgmtAddr string `toml:"mwan_mgmt_addr"`
	// The configuration loader defaults guest_type to qemu.
	GuestType GuestType `toml:"guest_type"`

	Email   EmailConfig   `toml:"email"`
	Network NetworkConfig `toml:"network"`

	Agent     AgentSection     `toml:"agent"`
	BGP       BGPSection       `toml:"bgp"`
	OPNsense  OPNsenseSection  `toml:"opnsense"`
	IfMgr     IfMgrSection     `toml:"ifmgr"`
	Notify    NotifySection    `toml:"notify"`
	Wanconfig WanconfigSection `toml:"wanconfig"`
	Report    ReportSection    `toml:"report"`
}

// WanconfigSection gates the gateway's management surface: whether this
// host's ifmgr publishes the configuration it loaded into the wanconfig
// datastore (sysrepo) for RESTCONF readers. It is false unless the rendered
// config says otherwise, so a host without a sysrepo repository (the
// hypervisors, the failover container, production until its stack is
// installed) never opens a connection it cannot complete. Publishing is
// never a precondition for running: a failed publish is logged and the
// daemon carries on without a management surface.
type WanconfigSection struct {
	Publish bool `toml:"publish"`
}

// NotifySection configures repeat cadence for each alert kind and key.
type NotifySection = watchdogconfig.NotifySection

// IfMgrSection holds the mwan ifmgr daemon's role-pluggable configuration.
// Each role is a list of modules (see internal/ifmgr/roles.go), and the
// module config schema is explicitly modeled in IfMgrModulesSection.
type IfMgrSection struct {
	GuestType          GuestType       `toml:"-"`
	Firewall           firewall.Config `toml:"-"`
	PinnedConnectionID string          `toml:"-"`
	Role               string          `toml:"role"`
	ReconcileInterval  string          `toml:"reconcile_interval"`
	LeaseDirectory     string          `toml:"lease_directory"`
	LogFile            string          `toml:"log_file"`
	JSONLogFile        string          `toml:"json_log_file"`
	Debug              bool            `toml:"debug"`
	// These three translation values and the WAN map below come from
	// /etc/mwan/network.json. The skip tag is what stops the decoder reading a
	// stale key out of a config.toml that still carries one, so exactly one
	// file owns them at every moment.
	InternalPrefix string `toml:"-"`
	OpnsenseEdgeV6 string `toml:"-"`
	MwanbrEdgeV6   string `toml:"-"`
	// HashMode and ReservedTables come from network.json's steering group
	// beside the WAN map, so the same skip tag keeps a stale config.toml key
	// out of them. HashMode decides how the steering module assigns a new
	// connection; ReservedTables is the set no provider may route into.
	HashMode              string                       `toml:"-"`
	ReservedTables        []int                        `toml:"-"`
	Iface                 map[string]IfMgrIfaceSection `toml:"iface"`
	Modules               IfMgrModulesSection          `toml:"modules"`
	Alerts                IfMgrAlertsSection           `toml:"alerts"`
	WAN                   map[string]IfMgrWANEntry     `toml:"-"`
	ConnectionIDs         map[string]connectionid.ID   `toml:"-"`
	ExplicitConnectionIDs map[string]connectionid.ID   `toml:"-"`
	Connections           []interfaceintent.Connection `toml:"-"`
}

// IfMgrAlertsSection controls the per-alert repeat cadence for the
// AlertManager. RepeatEvery is the global default applied to every alert
// kind unless overridden in PerModule. PerModule is keyed by alert kind
// (the first arg to AlertManager.Notify, e.g. "wg-peer-stalled" or
// "wg-reconcile-failed"), not by module name. Keying on alert kind lets
// one module emit multiple kinds and get a separate cadence for each.
//
// Default behaviour with empty values: RepeatEvery == "0s" means alerts
// fire once per transition only and never repeat.
type IfMgrAlertsSection = watchdogconfig.IfMgrAlertsSection

// IfMgrIfaceSection holds one [ifmgr.iface.<name>] sub-table. The map
// key is the conventional iface name (mbrains, eth0, ...); Name overrides
// it when set explicitly.
type IfMgrIfaceSection struct {
	Name               string `toml:"name"`
	DHCPv4             bool   `toml:"dhcp_v4"`
	RASolicit          bool   `toml:"ra_solicit"`
	DHCPInitialBackoff string `toml:"dhcp_initial_backoff"`
	DHCPMaxBackoff     string `toml:"dhcp_max_backoff"`
}

func defaultConfig() Config {
	return defaultConfigBase()
}

func defaultConfigBase() Config {
	// Assign field-by-field instead of via a struct literal so the
	// exhaustruct lint does not require enumerating every zero-value
	// sub-section (OPNsense, IfMgr, and their many nested sub-structs).
	var cfg Config
	watchdogDefaults := watchdogconfig.Default()
	cfg.GuestType = watchdogDefaults.GuestType
	cfg.Email = watchdogDefaults.Email
	cfg.Network = watchdogDefaults.Network
	cfg.Report = DefaultReportSection()
	cfg.Agent = AgentSection{
		VsockPort: 50051, TCPAddr: "[::]:50052",
		DeployFile:     "/var/lib/mwan/last-deploy",
		DeployExpected: true,
		LogFile:        "/var/log/mwan-agent.log",
		Debug:          false,
	}
	cfg.BGP.GracefulRestart = BGPGracefulRestart{
		Enabled:             true,
		RestartTime:         30,
		NotificationEnabled: true,
	}
	return cfg
}

// Load loads the single TOML config.
// Path: --config flag > MWAN_CONFIG env > /etc/mwan/config.toml
func Load() (*Config, error) {
	cfg, arguments, err := LoadArguments(os.Args)
	os.Args = arguments
	return cfg, err
}

// LoadArguments consumes --config without modifying process-global arguments.
func LoadArguments(arguments []string) (*Config, []string, error) {
	path, remaining := watchdogconfig.ResolvePath(arguments)
	cfg, err := loadConfig(path)
	return cfg, remaining, err
}

func loadConfig(path string) (*Config, error) {
	// The path comes from the unit file or the operator's command line, both
	// already privileged, so cleaning it normalizes the value rather than
	// defending a trust boundary.
	path = filepath.Clean(path)

	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("read config failed", "path", path, "error", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := defaultConfig()
	cfg.Path = path
	if err := toml.Unmarshal(data, &cfg); err != nil {
		slog.Error("parse config failed", "path", path, "error", err)
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// Env overrides for secrets
	watchdogconfig.ApplyEmailSecret(&cfg.Email)
	if v := strings.TrimSpace(os.Getenv("OPNSENSE_API_SECRET")); v != "" {
		cfg.OPNsense.APISecret = v
	}
	if err := watchdogconfig.ValidateGuestType(cfg.GuestType); err != nil {
		slog.Error("validate guest type failed", "error", err)
		return nil, fmt.Errorf("validate guest type: %w", err)
	}
	if cfg.IfMgr.Modules.Health != nil {
		if _, _, err := cfg.IfMgr.Modules.Health.ParseMaxStateAge(); err != nil {
			slog.Error("validate health max state age failed", "error", err)
			return nil, fmt.Errorf("validate [ifmgr.modules.health]: %w", err)
		}
	}
	if err := validateBGPDynamicConfig(&cfg.BGP); err != nil {
		slog.Error("validate BGP dynamic configuration failed", "error", err)
		return nil, fmt.Errorf("validate BGP dynamic configuration: %w", err)
	}
	if err := validateBGPForwardingReadiness(&cfg.BGP); err != nil {
		slog.Error("validate BGP forwarding readiness failed", "error", err)
		return nil, fmt.Errorf("validate BGP forwarding readiness: %w", err)
	}

	return &cfg, nil
}

// validateBGPDynamicConfig checks the dynamic-neighbor settings. Load calls it
// on every start, so a gateway with a malformed dynamic_neighbors prefix
// refuses to come up rather than silently accepting no peers.
func validateBGPDynamicConfig(b *BGPSection) error {
	if err := validateBGPDynamicNeighbors(b.DynamicNeighbors); err != nil {
		return err
	}
	if len(b.DynamicNeighbors) > 0 && b.LearnedRouteIface == "" {
		return errors.New("[bgp] learned_route_iface is required when dynamic_neighbors is configured")
	}
	return nil
}

func validateBGPDynamicNeighbors(prefixes []string) error {
	for index, prefixText := range prefixes {
		prefix, err := netip.ParsePrefix(prefixText)
		if err != nil {
			return fmt.Errorf("[bgp] dynamic_neighbors[%d] %q must be a CIDR prefix: %s", index, prefixText, err.Error())
		}
		if prefix.Bits() == 0 {
			return fmt.Errorf("[bgp] dynamic_neighbors[%d] %q must not be a default route", index, prefixText)
		}
	}
	return nil
}

func validateBGPForwardingReadiness(b *BGPSection) error {
	if !b.Enabled || !b.UseWanconfig {
		return nil
	}
	readiness := b.ForwardingReadiness
	if !filepath.IsAbs(readiness.SocketPath) {
		return errors.New("[bgp.forwarding_readiness] socket_path must be an absolute path")
	}
	if readiness.PollIntervalMilliseconds <= 0 {
		return errors.New("[bgp.forwarding_readiness] poll_interval_milliseconds must be greater than zero")
	}
	if readiness.ReadTimeoutMilliseconds <= 0 {
		return errors.New("[bgp.forwarding_readiness] read_timeout_milliseconds must be greater than zero")
	}
	return nil
}
