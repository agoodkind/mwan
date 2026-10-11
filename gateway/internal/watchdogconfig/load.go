package watchdogconfig

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"goodkind.io/mwan/internal/notify"
)

// Default supplies shared defaults for both configuration loaders.
func Default() Config {
	var cfg Config
	cfg.GuestType = GuestTypeQEMU
	// The secrets and addresses left empty here come from the TOML file or
	// the environment; a default would be wrong on every host.
	cfg.Email = notify.EmailConfig{
		MinLevel:      "ERROR",
		Cooldown:      "5m",
		SMTP2GOAPIKey: "",
		AlertEmail:    "",
		From:          "",
		SubjectPrefix: "",
		BindIface:     "",
	}
	cfg.PVE = PVEConfig{
		Node: "",
	}
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
		MaxTotalSnapshots:  15,
		VsockCID:           0,
		VsockPort:          0,
		MwanAgentTCPAddr:   "",
		StatusListenPort:   0,
		StatusCommand:      nil,
		ConfigStateCommand: nil,
		LogFile:            "/var/log/mwan-watchdog.log", JSONLogFile: "/var/log/mwan-watchdog.jsonl",
		RollbackStateFile: "/run/mwan-rollback.state",
		RollbackLockFile:  "/run/mwan-watchdog-rollback.lock",
		ServiceName:       "mwan-watchdog",
	}
	return cfg
}

// LoadArguments consumes --config without modifying process-global arguments.
func LoadArguments(arguments []string) (*Config, []string, error) {
	path, remaining := ResolvePath(arguments)
	cfg, err := loadConfig(path)
	return cfg, remaining, err
}

// ResolvePath selects the first configuration path flag with a value over
// MWAN_CONFIG and /etc/mwan/config.toml. The returned arguments omit
// that flag and its value.
func ResolvePath(arguments []string) (string, []string) {
	path := "/etc/mwan/config.toml"
	if v := os.Getenv("MWAN_CONFIG"); v != "" {
		path = v
	}
	remaining := append([]string(nil), arguments...)
	for i, arg := range remaining {
		if arg == "--config" && i+1 < len(remaining) {
			path = remaining[i+1]
			remaining = append(remaining[:i], remaining[i+2:]...)
			break
		}
	}
	return path, remaining
}

// ErrEmptyPath indicates that LoadWatchdogSection received an empty path.
var ErrEmptyPath = errors.New("watchdog config path is required")

type watchdogFile struct {
	Watchdog WatchdogSection `toml:"watchdog"`
}

// LoadWatchdogSection applies watchdog defaults and skips status source
// validation because gateway consumers use timeout and policy values.
func LoadWatchdogSection(path string) (WatchdogSection, error) {
	var none WatchdogSection
	if path == "" {
		return none, ErrEmptyPath
	}
	path, data, err := readFile(path)
	if err != nil {
		return none, err
	}
	file := watchdogFile{Watchdog: Default().Watchdog}
	if err := toml.Unmarshal(data, &file); err != nil {
		return none, parseFailure(path, err)
	}
	return file.Watchdog, nil
}

func readFile(path string) (string, []byte, error) {
	// The path comes from the unit file or the operator's command line, both
	// already privileged, so cleaning it normalizes the value rather than
	// defending a trust boundary.
	path = filepath.Clean(path)

	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("read config failed", "path", path, "error", err)
		return path, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return path, data, nil
}

func parseFailure(path string, err error) error {
	slog.Error("parse config failed", "path", path, "error", err)
	return fmt.Errorf("parse config %s: %w", path, err)
}

func loadConfig(path string) (*Config, error) {
	path, data, err := readFile(path)
	if err != nil {
		return nil, err
	}

	cfg := Default()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, parseFailure(path, err)
	}

	ApplyEmailSecret(&cfg.Email)
	if err := ValidateGuestType(cfg.GuestType); err != nil {
		slog.Error("validate guest type failed", "error", err)
		return nil, fmt.Errorf("validate guest type: %w", err)
	}
	if err := ValidateStatusSource(cfg.Watchdog); err != nil {
		slog.Error("validate watchdog status source failed", "error", err)
		return nil, fmt.Errorf("validate watchdog status source: %w", err)
	}

	return &cfg, nil
}

// ApplyEmailSecret overrides SMTP2GOAPIKey only when SMTP2GO_API_KEY is
// nonempty after trimming whitespace.
func ApplyEmailSecret(email *notify.EmailConfig) {
	if v := strings.TrimSpace(os.Getenv("SMTP2GO_API_KEY")); v != "" {
		email.SMTP2GOAPIKey = v
	}
}

// ValidateStatusSource rejects simultaneous command and listener sources.
func ValidateStatusSource(section WatchdogSection) error {
	if len(section.StatusCommand) > 0 && section.StatusListenPort != 0 {
		return errors.New("[watchdog] status_command and status_listen_port are mutually exclusive")
	}
	return nil
}

// GuestTypes returns the accepted values in the order qemu, lxc for the
// top-level TOML guest_type key, mwan install --guest-type, and the OpenTofu
// mwan_role attribute guest_type.
func GuestTypes() []GuestType {
	return []GuestType{GuestTypeQEMU, GuestTypeLXC}
}

// ParseGuestType accepts only the exact lowercase names qemu and lxc.
func ParseGuestType(name string) (GuestType, error) {
	for _, guestType := range GuestTypes() {
		if string(guestType) == name {
			return guestType, nil
		}
	}
	return "", fmt.Errorf("guest_type %q must be %q or %q", name, GuestTypeQEMU, GuestTypeLXC)
}

// ValidateGuestType accepts only the exact lowercase values qemu and lxc.
func ValidateGuestType(guestType GuestType) error {
	_, err := ParseGuestType(string(guestType))
	return err
}
