// Package bgpsession defines the configuration of one external BGP session without importing a BGP implementation.
package bgpsession

import (
	"errors"
	"fmt"
	"net/netip"
)

// ExportMode selects the condition under which a session originates an export prefix, and validation rejects the zero value.
type ExportMode uint8

const (
	// ExportAlways originates the prefix whenever the session is eligible.
	ExportAlways ExportMode = iota + 1
	// ExportBackup originates the prefix only while the session is eligible and the backup path is active.
	ExportBackup
)

const (
	maxIPv6PrefixLength     = 128
	minHoldSeconds          = 3
	maxHoldSeconds          = 65535
	kernelDefaultIPv6Metric = 1024
)

// Community is one standard BGP community in ASN:value form.
type Community struct {
	ASN   uint16
	Value uint16
}

// LargeCommunity is one large BGP community in global-administrator:local-data-1:local-data-2 form.
type LargeCommunity struct {
	GlobalAdmin uint32
	LocalData1  uint32
	LocalData2  uint32
}

// ImportRule accepts learned IPv6 prefixes inside Prefix with a prefix length between MinLength and MaxLength inclusive.
type ImportRule struct {
	Prefix    netip.Prefix
	MinLength uint8
	MaxLength uint8
}

// ExportRule configures a session to originate one IPv6 prefix, and the session sends LocalPreference only on an iBGP session.
type ExportRule struct {
	Prefix           netip.Prefix
	Mode             ExportMode
	LocalPreference  *uint32
	MED              *uint32
	PrependCount     uint8
	Communities      []Community
	LargeCommunities []LargeCommunity
	NextHop          netip.Addr
}

// Config configures one external BGP session, where equal ASNs select iBGP and empty Tables disable kernel route installation.
type Config struct {
	Name             string
	RouterID         netip.Addr
	LocalASN         uint32
	RemoteASN        uint32
	PeerAddress      netip.Addr
	PeerPort         uint16
	LocalAddress     netip.Addr
	Interface        string
	MultihopTTL      uint8
	KeepaliveSeconds uint32
	HoldSeconds      uint32
	Import           []ImportRule
	Export           []ExportRule
	Tables           []int
	RouteMetric      uint32
}

// Internal reports whether the local and remote ASNs select an iBGP session.
func (c Config) Internal() bool {
	return c.LocalASN == c.RemoteASN
}

// Validated returns a copy of cfg with separate slices and masked prefixes.
func Validated(cfg Config) (Config, error) {
	var none Config
	if cfg.Name == "" {
		return none, errors.New("name is required")
	}
	if err := validateSessionIdentity(cfg); err != nil {
		return none, err
	}
	if err := validateSessionTransport(cfg); err != nil {
		return none, err
	}
	importRules, err := validatedImportRules(cfg.Import)
	if err != nil {
		return none, err
	}
	exportRules, err := validatedExportRules(cfg.Export, cfg.Internal())
	if err != nil {
		return none, err
	}
	cfg.Import = importRules
	cfg.Export = exportRules
	cfg.Tables = append([]int(nil), cfg.Tables...)
	return cfg, nil
}

func validateSessionIdentity(cfg Config) error {
	if !cfg.RouterID.Is4() || cfg.RouterID.IsUnspecified() {
		return fmt.Errorf("router ID %s is not a usable IPv4 address", cfg.RouterID)
	}
	if cfg.LocalASN == 0 {
		return errors.New("local ASN is required")
	}
	if cfg.RemoteASN == 0 {
		return errors.New("remote ASN is required")
	}
	return nil
}

func validateSessionTransport(cfg Config) error {
	if !cfg.PeerAddress.IsValid() || cfg.PeerAddress.IsUnspecified() {
		return fmt.Errorf("peer address %s is not a usable address", cfg.PeerAddress)
	}
	if cfg.PeerPort == 0 {
		return errors.New("peer port is required")
	}
	if cfg.LocalAddress.IsValid() && cfg.LocalAddress.Is4() != cfg.PeerAddress.Is4() {
		return fmt.Errorf(
			"local address %s and peer address %s use different address families",
			cfg.LocalAddress, cfg.PeerAddress,
		)
	}
	if cfg.KeepaliveSeconds == 0 {
		return errors.New("keepalive timer is required")
	}
	if cfg.HoldSeconds < minHoldSeconds || cfg.HoldSeconds > maxHoldSeconds {
		return fmt.Errorf(
			"hold timer %d is outside the range %d to %d",
			cfg.HoldSeconds, minHoldSeconds, maxHoldSeconds,
		)
	}
	if cfg.KeepaliveSeconds >= cfg.HoldSeconds {
		return fmt.Errorf(
			"keepalive timer %d must be shorter than hold timer %d",
			cfg.KeepaliveSeconds, cfg.HoldSeconds,
		)
	}
	if len(cfg.Tables) > 0 && cfg.Interface == "" {
		return errors.New("interface is required when kernel tables are configured")
	}
	if len(cfg.Tables) > 0 && (cfg.RouteMetric == 0 || cfg.RouteMetric == kernelDefaultIPv6Metric) {
		return fmt.Errorf(
			"route metric %d collides with the kernel default metric %d",
			cfg.RouteMetric, kernelDefaultIPv6Metric,
		)
	}
	for _, tableID := range cfg.Tables {
		if tableID <= 0 {
			return fmt.Errorf("kernel table %d is not a valid table ID", tableID)
		}
	}
	return nil
}

func validIPv6Prefix(prefix netip.Prefix) bool {
	return prefix.IsValid() && prefix.Addr().Is6() && !prefix.Addr().Is4In6()
}

func validatedImportRules(rules []ImportRule) ([]ImportRule, error) {
	validated := make([]ImportRule, 0, len(rules))
	for _, rule := range rules {
		if !validIPv6Prefix(rule.Prefix) {
			return nil, fmt.Errorf("import prefix %s is not an IPv6 prefix", rule.Prefix)
		}
		bits := rule.Prefix.Bits()
		if int(rule.MinLength) < bits || rule.MaxLength < rule.MinLength ||
			rule.MaxLength > maxIPv6PrefixLength {
			return nil, fmt.Errorf(
				"import prefix %s has inconsistent length bounds %d to %d",
				rule.Prefix, rule.MinLength, rule.MaxLength,
			)
		}
		rule.Prefix = rule.Prefix.Masked()
		validated = append(validated, rule)
	}
	return validated, nil
}

func validatedExportRules(rules []ExportRule, internal bool) ([]ExportRule, error) {
	validated := make([]ExportRule, 0, len(rules))
	seen := make(map[netip.Prefix]struct{}, len(rules))
	for _, rule := range rules {
		if !validIPv6Prefix(rule.Prefix) {
			return nil, fmt.Errorf("export prefix %s is not an IPv6 prefix", rule.Prefix)
		}
		rule.Prefix = rule.Prefix.Masked()
		if _, duplicate := seen[rule.Prefix]; duplicate {
			return nil, fmt.Errorf("export prefix %s is configured more than once", rule.Prefix)
		}
		seen[rule.Prefix] = struct{}{}
		if rule.Mode != ExportAlways && rule.Mode != ExportBackup {
			return nil, fmt.Errorf("export prefix %s has no valid export mode", rule.Prefix)
		}
		if !rule.NextHop.Is6() || rule.NextHop.Is4In6() || rule.NextHop.IsUnspecified() {
			return nil, fmt.Errorf(
				"export prefix %s next hop %s is not an IPv6 address",
				rule.Prefix, rule.NextHop,
			)
		}
		if internal && rule.PrependCount > 0 {
			return nil, fmt.Errorf(
				"export prefix %s prepends the local ASN on an iBGP session",
				rule.Prefix,
			)
		}
		rule.Communities = append([]Community(nil), rule.Communities...)
		rule.LargeCommunities = append([]LargeCommunity(nil), rule.LargeCommunities...)
		validated = append(validated, rule)
	}
	return validated, nil
}
