// Package config defines the mwan TOML configuration schema and validation.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// NetworkConfig holds site-specific topology values. It carries no provider
// list: the gateway pushes its own per-provider verdict to the watchdog, so no
// reader here has to be told which interfaces exist.
type NetworkConfig struct {
	PingTargetIPv4 string   `toml:"ping_target_ipv4"`
	PingTargetIPv6 string   `toml:"ping_target_ipv6"`
	PingTargets    []string `toml:"ping_targets"`
	CurlTarget     string   `toml:"curl_target"`
	LastDeployPath string   `toml:"last_deploy_path"`
	LastChangePath string   `toml:"last_change_path"`
}

// EmailConfig holds email notification settings.
type EmailConfig struct {
	SMTP2GOAPIKey string `toml:"smtp2go_api_key"`
	AlertEmail    string `toml:"alert_email"`
	From          string `toml:"from"`
	SubjectPrefix string `toml:"subject_prefix"`
	BindIface     string `toml:"bind_iface"`
	MinLevel      string `toml:"min_level"`
	Cooldown      string `toml:"cooldown"`
}

// PVEConfig holds Proxmox VE API credentials and endpoints.
type PVEConfig struct {
	BaseURL     string `toml:"base_url"`
	Node        string `toml:"node"`
	TokenID     string `toml:"token_id"`
	TokenSecret string `toml:"token_secret"`
}

// WatchdogSection holds watchdog-specific configuration.
type WatchdogSection struct {
	DeployWindowMinutes        int `toml:"deploy_window_minutes"`
	ConnectivityTimeoutSeconds int `toml:"connectivity_timeout_seconds"`
	CheckIntervalHealthy       int `toml:"check_interval_healthy_seconds"`
	CheckIntervalDegraded      int `toml:"check_interval_degraded_seconds"`
	PostRollbackGraceSeconds   int `toml:"post_rollback_grace_seconds"`
	AlertCooldownSeconds       int `toml:"alert_cooldown_seconds"`
	DeployGracePeriodSeconds   int `toml:"deploy_grace_period_seconds"`
	MaxRollbackAttempts        int `toml:"max_rollback_attempts"`
	MaxIterations              int `toml:"max_iterations"`

	SnapshotHealthyThreshold   int `toml:"snapshot_healthy_threshold"`
	MaxKnownGoodSnapshots      int `toml:"max_known_good_snapshots"`
	HashCheckEveryNHealthy     int `toml:"hash_check_every_n_healthy"`
	MinSnapshotIntervalSeconds int `toml:"min_snapshot_interval_seconds"`
	MaxTotalSnapshots          int `toml:"max_total_snapshots"`

	VsockCID         uint32 `toml:"vsock_cid"`
	VsockPort        uint32 `toml:"vsock_port"`
	MwanAgentTCPAddr string `toml:"mwan_agent_tcp_addr"`
	// StatusListenPort is the vsock port the gateway pushes its provider
	// verdict to. Zero, the default everywhere but the two hypervisors, starts
	// no listener at all.
	StatusListenPort uint32 `toml:"status_listen_port"`

	LogFile           string `toml:"log_file"`
	JSONLogFile       string `toml:"json_log_file"`
	RollbackStateFile string `toml:"rollback_state_file"`
	RollbackLockFile  string `toml:"rollback_lock_file"`

	// ServiceName is the systemd unit name that watchdog subcommands target.
	// Defaults to "mwan-watchdog" on production. Testbed sets this to
	// "mwan-watchdog-testbed" so the same binary works against the
	// per-environment unit.
	ServiceName string `toml:"service_name"`
}

// Duration helper methods for WatchdogSection.

func (ws WatchdogSection) HealthyInterval() time.Duration {
	return time.Duration(ws.CheckIntervalHealthy) * time.Second
}

func (ws WatchdogSection) DegradedInterval() time.Duration {
	return time.Duration(ws.CheckIntervalDegraded) * time.Second
}

func (ws WatchdogSection) PostRollbackGrace() time.Duration {
	return time.Duration(ws.PostRollbackGraceSeconds) * time.Second
}

// FailoverSection holds BGP failover configuration.
type FailoverSection struct {
	LXCID string `toml:"lxc_id"`
}

// OPNsenseSection holds OPNsense API credentials, endpoint, and its own BGP config.
// OPNsense is a BGP peer, not a speaker we control. Its BGP config is the inverse
// of the agent's: different router-id, different neighbor list.
type OPNsenseSection struct {
	URL          string      `toml:"url"`
	APIKey       string      `toml:"api_key"`
	APISecret    string      `toml:"api_secret"`
	Insecure     bool        `toml:"insecure"`
	GatewayNames []string    `toml:"gateway_names"`
	BGP          OPNsenseBGP `toml:"bgp"`

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
	UseWanconfig    bool               `toml:"use_wanconfig"`
	Announce        BGPAnnounce        `toml:"announce"`
	GracefulRestart BGPGracefulRestart `toml:"graceful_restart"`
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

// Config is the single TOML configuration for the mwan monolith.
// Default path: /etc/mwan/config.toml, override with --config or MWAN_CONFIG env.
type Config struct {
	Hostname     string `toml:"hostname"`
	MwanVMID     string `toml:"mwan_vmid"`
	MwanMgmtAddr string `toml:"mwan_mgmt_addr"`

	Email   EmailConfig   `toml:"email"`
	PVE     PVEConfig     `toml:"pve"`
	Network NetworkConfig `toml:"network"`

	Watchdog  WatchdogSection  `toml:"watchdog"`
	Failover  FailoverSection  `toml:"failover"`
	Agent     AgentSection     `toml:"agent"`
	BGP       BGPSection       `toml:"bgp"`
	OPNsense  OPNsenseSection  `toml:"opnsense"`
	IfMgr     IfMgrSection     `toml:"ifmgr"`
	Notify    NotifySection    `toml:"notify"`
	Wanconfig WanconfigSection `toml:"wanconfig"`
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

// NotifySection controls the per-(kind, key) repeat cadence that the
// internal/notify Manager applies on top of the state-change boundary.
// The shape mirrors IfMgrAlertsSection because slice A introduces the
// notify package by carving the state machine out of internal/ifmgr;
// callers wired to the new package read cfg.Notify, callers still
// wired to the ifmgr AlertManager continue to read cfg.IfMgr.Alerts
// until slice B migrates them.
//
// RepeatEvery is the global default applied to every alert kind unless
// overridden in PerKind. PerKind is keyed by alert kind (the Kind field
// on notify.Event), not by module name. Both fields accept Go duration
// strings like "30m" or "24h".
//
// Default behaviour with empty values: RepeatEvery == "0s" means
// alerts fire once per transition only and never repeat.
type NotifySection struct {
	RepeatEvery string            `toml:"repeat_every"`
	PerKind     map[string]string `toml:"per_kind"`
}

// IfMgrSection holds the mwan ifmgr daemon's role-pluggable configuration.
// Each role is a list of modules (see internal/ifmgr/roles.go), and the
// module config schema is explicitly modeled in IfMgrModulesSection.
type IfMgrSection struct {
	Role              string `toml:"role"`
	ReconcileInterval string `toml:"reconcile_interval"`
	LogFile           string `toml:"log_file"`
	JSONLogFile       string `toml:"json_log_file"`
	Debug             bool   `toml:"debug"`
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
	HashMode       string                       `toml:"-"`
	ReservedTables []int                        `toml:"-"`
	Iface          map[string]IfMgrIfaceSection `toml:"iface"`
	Modules        IfMgrModulesSection          `toml:"modules"`
	Alerts         IfMgrAlertsSection           `toml:"alerts"`
	WAN            map[string]IfMgrWANEntry     `toml:"-"`
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
type IfMgrAlertsSection struct {
	RepeatEvery string            `toml:"repeat_every"`
	PerModule   map[string]string `toml:"per_module"`
}

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
	cfg.Email = EmailConfig{MinLevel: "ERROR", Cooldown: "5m"}
	cfg.PVE = PVEConfig{BaseURL: "https://127.0.0.1:8006/api2/json"}
	cfg.Network = NetworkConfig{
		PingTargetIPv4: "1.1.1.1",
		PingTargetIPv6: "2606:4700:4700::1111",
		PingTargets:    nil,
		CurlTarget:     "",
		LastDeployPath: "/var/lib/mwan/last-deploy",
		LastChangePath: "/var/run/mwan-last-change",
	}
	cfg.Watchdog = WatchdogSection{
		DeployWindowMinutes: 30, ConnectivityTimeoutSeconds: 60,
		CheckIntervalHealthy: 30, CheckIntervalDegraded: 10,
		PostRollbackGraceSeconds: 120, AlertCooldownSeconds: 300,
		DeployGracePeriodSeconds: 60, MaxRollbackAttempts: 3,
		MaxIterations:            0,
		SnapshotHealthyThreshold: 20, MaxKnownGoodSnapshots: 3,
		HashCheckEveryNHealthy: 10, MinSnapshotIntervalSeconds: 300,
		MaxTotalSnapshots: 15,
		VsockCID:          0,
		VsockPort:         0,
		MwanAgentTCPAddr:  "",
		StatusListenPort:  0,
		LogFile:           "/var/log/mwan-watchdog.log", JSONLogFile: "/var/log/mwan-watchdog.jsonl",
		RollbackStateFile: "/run/mwan-rollback.state",
		RollbackLockFile:  "/run/mwan-watchdog-rollback.lock",
		ServiceName:       "mwan-watchdog",
	}
	cfg.Agent = AgentSection{
		VsockPort: 50051, TCPAddr: "[::]:50052",
		DeployFile:     "/var/lib/mwan/last-deploy",
		DeployExpected: true,
		LogFile:        "/var/log/mwan-agent.log",
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
	path := "/etc/mwan/config.toml"
	if v := os.Getenv("MWAN_CONFIG"); v != "" {
		path = v
	}
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			path = os.Args[i+1]
			os.Args = append(os.Args[:i], os.Args[i+2:]...)
			break
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("read config failed", "path", path, "error", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := defaultConfig()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		slog.Error("parse config failed", "path", path, "error", err)
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// Env overrides for secrets
	if v := strings.TrimSpace(os.Getenv("SMTP2GO_API_KEY")); v != "" {
		cfg.Email.SMTP2GOAPIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("PVE_TOKEN_SECRET")); v != "" {
		cfg.PVE.TokenSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("OPNSENSE_API_SECRET")); v != "" {
		cfg.OPNsense.APISecret = v
	}
	if err := validateBGPDynamicConfig(&cfg.BGP); err != nil {
		slog.Error("validate BGP dynamic configuration failed", "error", err)
		return nil, fmt.Errorf("validate BGP dynamic configuration: %w", err)
	}

	return &cfg, nil
}

// Subcommand is the typed enum of subcommand names that Validate
// dispatches on.
type Subcommand string

// Subcommand constants enumerate the subcommands recognized by Validate.
const (
	// SubWatchdog routes config validation through validateWatchdog.
	SubWatchdog Subcommand = "watchdog"
	// SubAgent routes config validation through validateAgent.
	SubAgent Subcommand = "agent"
	// SubIfMgr routes config validation through validateIfMgr.
	SubIfMgr Subcommand = "ifmgr"
)

// Validate validates the Config for a specific subcommand.
func Validate(cfg *Config, sub string, dryRun bool) error {
	if cfg.Hostname == "" {
		return errors.New("hostname is required")
	}
	switch Subcommand(sub) {
	case SubWatchdog:
		return validateWatchdog(cfg, dryRun)
	case SubAgent:
		if cfg.BGP.Enabled {
			return validateBGP(&cfg.BGP)
		}
		return nil
	case SubIfMgr:
		return validateIfMgr(cfg)
	}
	return nil
}

// validateIfMgr sanity-checks the [ifmgr] section. Module-specific
// validation lives in the module Constructor (Init); here we only catch
// gross structural issues so the CLI fails fast instead of surfacing a
// confusing runtime error from a module Init far down the call stack.
func validateIfMgr(cfg *Config) error {
	if cfg.IfMgr.Role == "" {
		return errors.New("ifmgr.role is required (or pass --role on the CLI)")
	}
	if len(cfg.IfMgr.Iface) == 0 {
		return errors.New("ifmgr: at least one [ifmgr.iface.<name>] section is required")
	}
	if len(cfg.IfMgr.Iface) > 1 {
		return errors.New("ifmgr: multi-iface configurations are not yet supported")
	}
	if cfg.IfMgr.ReconcileInterval != "" {
		if _, err := time.ParseDuration(cfg.IfMgr.ReconcileInterval); err != nil {
			slog.Error("config: reconcile_interval invalid", "err", err, "value", cfg.IfMgr.ReconcileInterval)
			return fmt.Errorf("ifmgr.reconcile_interval %q: %w", cfg.IfMgr.ReconcileInterval, err)
		}
	}
	for name, iface := range cfg.IfMgr.Iface {
		if iface.DHCPInitialBackoff != "" {
			if _, err := time.ParseDuration(iface.DHCPInitialBackoff); err != nil {
				slog.Error("config: dhcp_initial_backoff invalid", "err", err, "iface", name, "value", iface.DHCPInitialBackoff)
				return fmt.Errorf("ifmgr.iface.%s.dhcp_initial_backoff %q: %w", name, iface.DHCPInitialBackoff, err)
			}
		}
		if iface.DHCPMaxBackoff != "" {
			if _, err := time.ParseDuration(iface.DHCPMaxBackoff); err != nil {
				slog.Error("config: dhcp_max_backoff invalid", "err", err, "iface", name, "value", iface.DHCPMaxBackoff)
				return fmt.Errorf("ifmgr.iface.%s.dhcp_max_backoff %q: %w", name, iface.DHCPMaxBackoff, err)
			}
		}
	}
	if cfg.IfMgr.Alerts.RepeatEvery != "" {
		if _, err := time.ParseDuration(cfg.IfMgr.Alerts.RepeatEvery); err != nil {
			slog.Error("config: alerts.repeat_every invalid", "err", err, "value", cfg.IfMgr.Alerts.RepeatEvery)
			return fmt.Errorf("ifmgr.alerts.repeat_every %q: %w", cfg.IfMgr.Alerts.RepeatEvery, err)
		}
	}
	for kind, val := range cfg.IfMgr.Alerts.PerModule {
		if val == "" {
			continue
		}
		if _, err := time.ParseDuration(val); err != nil {
			slog.Error("config: alerts.per_module invalid", "err", err, "kind", kind, "value", val)
			return fmt.Errorf("ifmgr.alerts.per_module.%s %q: %w", kind, val, err)
		}
	}
	return nil
}

func validateWatchdog(cfg *Config, dryRun bool) error {
	if cfg.MwanVMID == "" {
		return errors.New("mwan_vmid is required")
	}
	if !dryRun && cfg.Email.SMTP2GOAPIKey == "" {
		return errors.New("[email] smtp2go_api_key is required (set in TOML or SMTP2GO_API_KEY env)")
	}
	if cfg.PVE.TokenID != "" && cfg.PVE.TokenSecret == "" {
		return errors.New("[pve] token_id set but token_secret empty")
	}
	return nil
}

func validateBGP(b *BGPSection) error {
	if b.ASN == 0 {
		return errors.New("[bgp] asn is required")
	}
	if b.RouterID == "" {
		return errors.New("[bgp] router_id is required")
	}
	if b.ListenPort == 0 {
		return errors.New("[bgp] listen_port is required")
	}
	if b.KeepaliveSeconds == 0 {
		return errors.New("[bgp] keepalive_seconds is required")
	}
	if b.HoldSeconds == 0 {
		return errors.New("[bgp] hold_seconds is required")
	}
	if b.HoldSeconds < 3*b.KeepaliveSeconds {
		return fmt.Errorf("[bgp] hold_seconds (%d) must be >= 3 * keepalive_seconds (%d)", b.HoldSeconds, b.KeepaliveSeconds)
	}
	if err := validateBGPDynamicConfig(b); err != nil {
		return err
	}
	if len(b.Neighbors) == 0 && len(b.NeighborsV6) == 0 && len(b.DynamicNeighbors) == 0 {
		return errors.New("[bgp] at least one neighbor (v4 or v6) or dynamic neighbor is required")
	}
	if len(b.Announce.IPv4) == 0 && len(b.Announce.IPv6) == 0 {
		return errors.New("[bgp.announce] at least one prefix (ipv4 or ipv6) is required")
	}
	if b.GracefulRestart.Enabled {
		if b.GracefulRestart.RestartTime == 0 {
			return errors.New("[bgp.graceful_restart] restart_time is required when enabled")
		}
		if b.GracefulRestart.RestartTime > 600 {
			return fmt.Errorf("[bgp.graceful_restart] restart_time (%d) must be <= 600", b.GracefulRestart.RestartTime)
		}
	}
	return nil
}

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
