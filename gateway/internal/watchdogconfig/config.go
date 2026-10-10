// Package watchdogconfig loads hypervisor settings from the shared TOML file.
package watchdogconfig

import (
	"time"

	"goodkind.io/mwan/internal/notify"
)

// NetworkConfig configures site topology without a provider list because
// the gateway sends each provider verdict to the watchdog.
type NetworkConfig struct {
	PingTargetIPv4 string   `toml:"ping_target_ipv4"`
	PingTargetIPv6 string   `toml:"ping_target_ipv6"`
	PingTargets    []string `toml:"ping_targets"`
	CurlTarget     string   `toml:"curl_target"`
	LastDeployPath string   `toml:"last_deploy_path"`
	LastChangePath string   `toml:"last_change_path"`
}

// PVEConfig selects the Proxmox VE node running the watchdog for local
// pvesh task queries.
type PVEConfig struct {
	Node string `toml:"node"`
}

// WatchdogSection configures hypervisor monitoring and guest rollback.
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
	// StatusCommand configures the guest status command arguments.
	// Do not configure StatusCommand together with status_listen_port.
	StatusCommand []string `toml:"status_command"`

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

// HealthyInterval is how long the watchdog waits between checks while
// connectivity is healthy.
func (ws WatchdogSection) HealthyInterval() time.Duration {
	return time.Duration(ws.CheckIntervalHealthy) * time.Second
}

// DegradedInterval is how long the watchdog waits between checks once
// connectivity is degraded. It is shorter than HealthyInterval so a developing
// outage is diagnosed sooner.
func (ws WatchdogSection) DegradedInterval() time.Duration {
	return time.Duration(ws.CheckIntervalDegraded) * time.Second
}

// PostRollbackGrace is how long the watchdog withholds judgement after a
// rollback, which gives the guest time to boot and its routes time to
// converge before the next verdict.
func (ws WatchdogSection) PostRollbackGrace() time.Duration {
	return time.Duration(ws.PostRollbackGraceSeconds) * time.Second
}

// FailoverSection configures BGP failover for an LXC guest.
type FailoverSection struct {
	LXCID string `toml:"lxc_id"`
	// AgentTCPAddr is optional during configuration loading, but failover requires it.
	AgentTCPAddr string `toml:"agent_tcp_addr"`
}

// GuestType selects qm for QEMU guests and pct for LXC guests.
type GuestType string

const (
	// GuestTypeQEMU selects qm for guest operations.
	GuestTypeQEMU GuestType = "qemu"
	// GuestTypeLXC selects pct for guest operations.
	GuestTypeLXC GuestType = "lxc"
)

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

// IfMgrAlertsFallback supplies alert cadence when the corresponding
// [notify] setting is empty.
type IfMgrAlertsFallback struct {
	Alerts IfMgrAlertsSection `toml:"alerts"`
}

// Config defines hypervisor settings within the shared gateway TOML format.
type Config struct {
	MwanVMID string `toml:"mwan_vmid"`
	// The configuration loader defaults guest_type to qemu.
	GuestType GuestType `toml:"guest_type"`

	Email   notify.EmailConfig `toml:"email"`
	PVE     PVEConfig          `toml:"pve"`
	Network NetworkConfig      `toml:"network"`

	Watchdog WatchdogSection     `toml:"watchdog"`
	Failover FailoverSection     `toml:"failover"`
	Notify   NotifySection       `toml:"notify"`
	IfMgr    IfMgrAlertsFallback `toml:"ifmgr"`
}
