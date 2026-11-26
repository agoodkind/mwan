// Package config defines the mwan TOML configuration schema and loads it.
// Load applies the built-in defaults, reads the file, overlays the secrets
// from the environment, and checks the dynamic BGP settings. Every other
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

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/firewall"
	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/notify"
)

const maxNetworkdTimeoutSeconds = int64(1<<63-1) / int64(time.Second)

var (
	errNetworkdTimeoutPositive = errors.New(
		"[ifmgr] networkd_timeout_seconds must be greater than zero",
	)
	errNetworkdTimeoutExceeded = errors.New("[ifmgr] networkd_timeout_seconds must not exceed")
)

// EmailConfig is the email notification settings type.
type EmailConfig = notify.EmailConfig

// NotifySection configures repeat cadence for each alert kind.
type NotifySection struct {
	RepeatEvery string            `toml:"repeat_every"`
	PerKind     map[string]string `toml:"per_kind"`
}

// IfMgrAlertsSection configures the default repeat cadence for each alert
// kind and optional per-kind overrides.
type IfMgrAlertsSection struct {
	RepeatEvery string            `toml:"repeat_every"`
	PerModule   map[string]string `toml:"per_module"`
}

// OPNsenseSection stores OPNsense API credentials, endpoint, and BGP peer settings.
// OPNsense is a BGP peer. This process does not control its speaker. Its BGP
// settings invert the agent settings: a different router-id and a different
// neighbor list.
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

// OPNsenseBGP is the BGP configuration written to OPNsense through its API.
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

// BGPSection stores embedded GoBGP speaker configuration.
type BGPSection struct {
	Enabled  bool   `toml:"enabled"`
	ASN      uint32 `toml:"asn"`
	RouterID string `toml:"router_id"`
	// The IPv6 next hop defaults to RouterID when this value is empty.
	NextHopV6           string                 `toml:"next_hop_v6"`
	KeepaliveSeconds    uint32                 `toml:"keepalive_seconds"`
	HoldSeconds         uint32                 `toml:"hold_seconds"`
	ListenPort          int32                  `toml:"listen_port"`
	Neighbors           []BGPNeighbor          `toml:"neighbors"`
	NeighborsV6         []BGPNeighbor          `toml:"neighbors_v6"`
	DynamicNeighbors    []string               `toml:"dynamic_neighbors"`
	LearnedRouteIface   string                 `toml:"learned_route_iface"`
	Announce            BGPAnnounce            `toml:"announce"`
	GracefulRestart     BGPGracefulRestart     `toml:"graceful_restart"`
	ForwardingReadiness BGPForwardingReadiness `toml:"forwarding_readiness"`
}

// ForwardingReadinessEnabled reports whether BGP checks a readiness socket.
func (b *BGPSection) ForwardingReadinessEnabled() bool {
	return b.ForwardingReadiness.SocketPath != ""
}

// BGPForwardingReadiness configures the ifmgr readiness check for BGP announcements.
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
// the GR capability with each peer. Planned restarts of mwan-agent then
// leave the helper (OPNsense FRR) without an immediate route withdrawal.
//
// RestartTime is advertised to the helper as the maximum number of seconds
// the helper should keep our routes after the BGP session drops.
// NotificationEnabled mirrors the "N" bit (RFC 8538). A
// NOTIFICATION-triggered session reset then preserves the GR semantics.
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

// AgentSection stores the local agent listener and logging settings.
type AgentSection struct {
	SocketPath string `toml:"socket_path"`
	TCPAddr    string `toml:"tcp_addr"`
	LogFile    string `toml:"log_file"`
	Debug      bool   `toml:"debug"`
}

// Config is the single TOML configuration for the mwan monolith.
// Default path: /etc/mwan/config.toml, override with --config or MWAN_CONFIG env.
type Config struct {
	Path     string      `toml:"-"`
	Hostname string      `toml:"hostname"`
	Email    EmailConfig `toml:"email"`

	Agent     AgentSection     `toml:"agent"`
	BGP       BGPSection       `toml:"bgp"`
	OPNsense  OPNsenseSection  `toml:"opnsense"`
	IfMgr     IfMgrSection     `toml:"ifmgr"`
	Notify    NotifySection    `toml:"notify"`
	Wanconfig WanconfigSection `toml:"wanconfig"`
}

// WanconfigSection controls whether ifmgr publishes its loaded gateway
// configuration to sysrepo for RESTCONF readers. A publish failure does not
// stop gateway operation.
type WanconfigSection struct {
	Publish bool `toml:"publish"`
}

// IfMgrSection stores the mwan ifmgr daemon's role-pluggable configuration.
// Each role is a list of modules (see internal/ifmgr/roles.go), and the
// module config schema is explicitly modeled in IfMgrModulesSection.
type IfMgrSection struct {
	OVSDataplane           bool            `toml:"-"`
	Firewall               firewall.Config `toml:"-"`
	PinnedConnectionID     string          `toml:"-"`
	Role                   string          `toml:"role"`
	ReconcileInterval      string          `toml:"reconcile_interval"`
	NetworkdTimeoutSeconds int             `toml:"networkd_timeout_seconds"`
	LeaseDirectory         string          `toml:"lease_directory"`
	LogFile                string          `toml:"log_file"`
	JSONLogFile            string          `toml:"json_log_file"`
	Debug                  bool            `toml:"debug"`
	// These three translation values and the WAN map below come from
	// /etc/mwan/network.json. The skip tag stops the decoder reading a
	// stale key out of a config.toml that still contains one. Exactly one
	// file owns them at every moment.
	InternalPrefix string `toml:"-"`
	OpnsenseEdgeV6 string `toml:"-"`
	MwanbrEdgeV6   string `toml:"-"`
	// HashMode and ReservedTables come from network.json's steering group
	// beside the WAN map. The same skip tag keeps a stale config.toml key
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

// NetworkdTimeout returns the configured wait for networkd operations.
func (section IfMgrSection) NetworkdTimeout() time.Duration {
	return time.Duration(section.NetworkdTimeoutSeconds) * time.Second
}

// IfMgrIfaceSection stores one [ifmgr.iface.<name>] sub-table. The map
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

// AgentDefaults avoids a second set of CLI flag defaults.
func AgentDefaults() AgentSection {
	return defaultConfigBase().Agent
}

func defaultConfigBase() Config {
	// Assign field-by-field instead of via a struct literal. The
	// exhaustruct lint then does not require enumerating every zero-value
	// sub-section (OPNsense, IfMgr, and their many nested sub-structs).
	var cfg Config
	cfg.Email.MinLevel = "ERROR"
	cfg.Email.Cooldown = "5m"
	cfg.IfMgr.NetworkdTimeoutSeconds = 60
	cfg.Agent = AgentSection{
		SocketPath: "",
		TCPAddr:    "[::]:50052",
		LogFile:    "/var/log/mwan-agent.log",
		Debug:      false,
	}
	cfg.BGP.GracefulRestart = BGPGracefulRestart{
		Enabled:             true,
		RestartTime:         30,
		NotificationEnabled: true,
	}

	return cfg
}

// LoadPath validates gateway settings from the selected TOML file.
func LoadPath(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("MWAN_CONFIG")
		if path == "" {
			path = "/etc/mwan/config.toml"
		}
	}

	return loadConfig(path)
}

func loadConfig(path string) (*Config, error) {
	// The unit file and command line can select any configuration path.
	path = filepath.Clean(path)

	absPath, err := filepath.Abs(path)
	if err != nil {
		slog.Error("resolve config path failed", "path", path, "error", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	absPath, err = filepath.EvalSymlinks(absPath)
	if err != nil {
		slog.Error("resolve config path failed", "path", path, "error", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		slog.Error("open filesystem root failed", "error", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	defer root.Close()

	relativePath, err := filepath.Rel(string(filepath.Separator), absPath)
	if err != nil {
		slog.Error("resolve config path failed", "path", path, "error", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	data, err := root.ReadFile(relativePath)
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

	// Environment variables override configured API secrets.
	if value := strings.TrimSpace(os.Getenv("SMTP2GO_API_KEY")); value != "" {
		cfg.Email.SMTP2GOAPIKey = value
	}

	if v := strings.TrimSpace(os.Getenv("OPNSENSE_API_SECRET")); v != "" {
		cfg.OPNsense.APISecret = v
	}

	if cfg.IfMgr.NetworkdTimeoutSeconds <= 0 {
		return nil, errNetworkdTimeoutPositive
	}

	if int64(cfg.IfMgr.NetworkdTimeoutSeconds) > maxNetworkdTimeoutSeconds {
		return nil, fmt.Errorf("%w %d", errNetworkdTimeoutExceeded, maxNetworkdTimeoutSeconds)
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

	if err := validateBGPForwardingReadiness(&cfg); err != nil {
		slog.Error("validate BGP forwarding readiness failed", "error", err)
		return nil, fmt.Errorf("validate BGP forwarding readiness: %w", err)
	}

	return &cfg, nil
}

// validateBGPDynamicConfig checks the dynamic-neighbor settings. Load runs it
// on every start. A gateway with a malformed dynamic_neighbors prefix
// refuses to come up instead of accepting a configuration with no peers.
func validateBGPDynamicConfig(b *BGPSection) error {
	if err := validateBGPDynamicNeighbors(b.DynamicNeighbors); err != nil {
		return err
	}

	if len(b.DynamicNeighbors) > 0 && b.LearnedRouteIface == "" {
		return errors.New(
			"[bgp] learned_route_iface is required when dynamic_neighbors is configured",
		)
	}

	return nil
}

func validateBGPDynamicNeighbors(prefixes []string) error {
	for index, prefixText := range prefixes {
		prefix, err := netip.ParsePrefix(prefixText)
		if err != nil {
			return fmt.Errorf(
				"[bgp] dynamic_neighbors[%d] %q must be a CIDR prefix: %s",
				index,
				prefixText,
				err.Error(),
			)
		}

		if prefix.Bits() == 0 {
			return fmt.Errorf(
				"[bgp] dynamic_neighbors[%d] %q must not be a default route",
				index,
				prefixText,
			)
		}
	}

	return nil
}

func validateBGPForwardingReadiness(cfg *Config) error {
	b := &cfg.BGP
	if !b.Enabled || !b.ForwardingReadinessEnabled() {
		return nil
	}

	readiness := b.ForwardingReadiness
	if !filepath.IsAbs(readiness.SocketPath) {
		return errors.New("[bgp.forwarding_readiness] socket_path must be an absolute path")
	}

	if readiness.PollIntervalMilliseconds <= 0 {
		return errors.New(
			"[bgp.forwarding_readiness] poll_interval_milliseconds must be greater than zero",
		)
	}

	if readiness.ReadTimeoutMilliseconds <= 0 {
		return errors.New(
			"[bgp.forwarding_readiness] read_timeout_milliseconds must be greater than zero",
		)
	}

	return nil
}
