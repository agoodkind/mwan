package watchdogconfig_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/watchdogconfig"
)

const mixedConfigText = `
hostname = "hypervisor-test"
mwan_vmid = "4100"
guest_type = "lxc"

[email]
alert_email = "alerts@example.test"

[pve]
node = "pve-test"

[network]
ping_target_ipv4 = "192.0.2.1"

[watchdog]
service_name = "mwan-watchdog-test"
vsock_cid = 42
status_listen_port = 50060
config_state_command = ["/usr/local/bin/mwan", "config-state"]

[failover]
lxc_id = "203"
agent_tcp_addr = "192.0.2.2:50052"

[notify]
repeat_every = "30m"

[notify.per_kind]
wan-down = "2h"

[ifmgr]
role = "wan"
reconcile_interval = "30s"

[ifmgr.alerts]
repeat_every = "24h"

[ifmgr.alerts.per_module]
wg-peer-stalled = "1h"

[bgp]
enabled = false
asn = 65001
router_id = "192.0.2.3"

[opnsense]
url = "https://opnsense.example.test"
gateway_names = ["WAN_GW"]
`

const conflictingStatusText = `
[watchdog]
status_command = ["mwan", "gateway-status"]
status_listen_port = 50060

[ifmgr]
role = "wan"

[bgp]
enabled = false

[opnsense]
url = "https://opnsense.example.test"
`

const conflictingStatusError = "validate watchdog status source: [watchdog] status_command and status_listen_port are mutually exclusive"

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatalf("configuration file write failed: %v", err)
	}
	t.Setenv("MWAN_CONFIG", configPath)
	return configPath
}

func mixedWatchdogSection() watchdogconfig.WatchdogSection {
	return watchdogconfig.WatchdogSection{
		DeployWindowMinutes:        30,
		ConnectivityTimeoutSeconds: 60,
		CheckIntervalHealthy:       30,
		CheckIntervalDegraded:      10,
		PostRollbackGraceSeconds:   120,
		AlertCooldownSeconds:       300,
		DeployGracePeriodSeconds:   60,
		MaxRollbackAttempts:        3,
		MaxIterations:              0,
		SnapshotHealthyThreshold:   20,
		MaxKnownGoodSnapshots:      3,
		HashCheckEveryNHealthy:     10,
		MinSnapshotIntervalSeconds: 300,
		MaxTotalSnapshots:          15,
		VsockCID:                   42,
		VsockPort:                  0,
		MwanAgentTCPAddr:           "",
		StatusListenPort:           50060,
		StatusCommand:              nil,
		ConfigStateCommand:         []string{"/usr/local/bin/mwan", "config-state"},
		LogFile:                    "/var/log/mwan-watchdog.log",
		JSONLogFile:                "/var/log/mwan-watchdog.jsonl",
		RollbackStateFile:          "/run/mwan-rollback.state",
		RollbackLockFile:           "/run/mwan-watchdog-rollback.lock",
		ServiceName:                "mwan-watchdog-test",
	}
}

func TestLoadMatchesGatewayLoaderForHypervisorSections(t *testing.T) {
	configPath := writeConfig(t, mixedConfigText)
	t.Setenv("SMTP2GO_API_KEY", " key-from-environment ")
	arguments := []string{"mwan", "--config", configPath, "--dry-run"}

	got, gotRemaining, err := watchdogconfig.LoadArguments(arguments)
	if err != nil {
		t.Fatalf("watchdog configuration load failed: %v", err)
	}
	want, gatewayRemaining, err := config.LoadArguments(arguments)
	if err != nil {
		t.Fatalf("gateway configuration load failed: %v", err)
	}

	wantRemaining := []string{"mwan", "--dry-run"}
	if !reflect.DeepEqual(gotRemaining, wantRemaining) {
		t.Errorf("remaining arguments differ between loaders: %v %v", gotRemaining, wantRemaining)
	}
	if !reflect.DeepEqual(gatewayRemaining, wantRemaining) {
		t.Errorf("remaining arguments differ between loaders: %v %v", gatewayRemaining, wantRemaining)
	}
	if got.MwanVMID != "4100" || got.MwanVMID != want.MwanVMID {
		t.Errorf("unexpected virtual machine identifier: %q %q", got.MwanVMID, want.MwanVMID)
	}
	if got.GuestType != watchdogconfig.GuestTypeLXC || got.GuestType != want.GuestType {
		t.Errorf("unexpected guest type: %q %q", got.GuestType, want.GuestType)
	}
	if got.Email.SMTP2GOAPIKey != "key-from-environment" || got.Email != want.Email {
		t.Errorf("unexpected email settings: %+v %+v", got.Email, want.Email)
	}
	wantPVE := watchdogconfig.PVEConfig{Node: "pve-test"}
	if got.PVE != wantPVE {
		t.Errorf("unexpected hypervisor node settings: %+v %+v", got.PVE, wantPVE)
	}
	if got.Network.PingTargetIPv4 != "192.0.2.1" || !reflect.DeepEqual(got.Network, want.Network) {
		t.Errorf("unexpected network settings: %+v %+v", got.Network, want.Network)
	}
	wantWatchdog := mixedWatchdogSection()
	if !reflect.DeepEqual(got.Watchdog, wantWatchdog) {
		t.Errorf("unexpected watchdog settings: %+v %+v", got.Watchdog, wantWatchdog)
	}
	if got.Network.LastDeployPath != "/var/lib/mwan/last-deploy" || got.Network.PingTargetIPv6 != "2606:4700:4700::1111" {
		t.Errorf("unexpected network settings: %+v %+v", got.Network, want.Network)
	}
	wantFailover := watchdogconfig.FailoverSection{LXCID: "203", AgentTCPAddr: "192.0.2.2:50052"}
	if got.Failover != wantFailover {
		t.Errorf("unexpected failover settings: %+v %+v", got.Failover, wantFailover)
	}
	wantCadence := notify.Config{
		RepeatEvery: 30 * time.Minute,
		PerKind:     map[string]time.Duration{"wan-down": 2 * time.Hour},
	}
	if !reflect.DeepEqual(got.NotifyConfig(), wantCadence) {
		t.Errorf("notification cadence differs between loaders: %+v %+v", got.NotifyConfig(), wantCadence)
	}
	if !reflect.DeepEqual(got.NotifyConfig(), want.NotifyConfig()) {
		t.Errorf("notification cadence differs between loaders: %+v %+v", got.NotifyConfig(), want.NotifyConfig())
	}
}

func TestLoadWatchdogSectionReturnsSectionWithDefaults(t *testing.T) {
	configPath := writeConfig(t, mixedConfigText)

	got, err := watchdogconfig.LoadWatchdogSection(configPath)
	if err != nil {
		t.Fatalf("watchdog configuration load failed: %v", err)
	}

	wantWatchdog := mixedWatchdogSection()
	if !reflect.DeepEqual(got, wantWatchdog) {
		t.Errorf("unexpected watchdog settings: %+v %+v", got, wantWatchdog)
	}
}

func TestLoadRejectsConflictingStatusSource(t *testing.T) {
	writeConfig(t, conflictingStatusText)
	arguments := []string{"mwan"}

	_, _, err := watchdogconfig.LoadArguments(arguments)
	if err == nil {
		t.Fatalf("watchdog loader accepted conflicting status sources: %s", conflictingStatusText)
	}
	if err.Error() != conflictingStatusError {
		t.Errorf("status source errors differ between loaders: %q %q", err.Error(), conflictingStatusError)
	}
}

func TestLoadAcceptsEitherStatusSourceAlone(t *testing.T) {
	writeConfig(t, `
[watchdog]
status_command = ["/usr/local/bin/mwan", "gateway-status"]
`)
	command, _, err := watchdogconfig.LoadArguments([]string{"mwan"})
	if err != nil {
		t.Fatalf("Load rejected status_command alone: %v", err)
	}
	wantCommand := []string{"/usr/local/bin/mwan", "gateway-status"}
	if !slices.Equal(command.Watchdog.StatusCommand, wantCommand) {
		t.Fatalf("status_command = %v, want %v", command.Watchdog.StatusCommand, wantCommand)
	}

	writeConfig(t, `
[watchdog]
status_listen_port = 50053
`)
	port, _, err := watchdogconfig.LoadArguments([]string{"mwan"})
	if err != nil {
		t.Fatalf("Load rejected status_listen_port alone: %v", err)
	}
	if port.Watchdog.StatusListenPort != 50053 {
		t.Fatalf("status_listen_port = %d, want 50053", port.Watchdog.StatusListenPort)
	}
	if len(port.Watchdog.StatusCommand) != 0 {
		t.Fatalf("status_command = %v, want empty", port.Watchdog.StatusCommand)
	}
}
