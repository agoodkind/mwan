package watchdogconfig_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/config"
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

[failover]
lxc_id = "203"
agent_tcp_addr = "192.0.2.2:50052"

[notify]
repeat_every = "30m"

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

func writeConfig(t *testing.T, text string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatalf("configuration file write failed: %v", err)
	}
	t.Setenv("MWAN_CONFIG", configPath)
}

func TestLoadMatchesGatewayLoaderForHypervisorSections(t *testing.T) {
	writeConfig(t, mixedConfigText)
	t.Setenv("SMTP2GO_API_KEY", " key-from-environment ")
	arguments := []string{"mwan", "--dry-run"}

	got, gotRemaining, err := watchdogconfig.LoadArguments(arguments)
	if err != nil {
		t.Fatalf("watchdog configuration load failed: %v", err)
	}
	want, wantRemaining, err := config.LoadArguments(arguments)
	if err != nil {
		t.Fatalf("gateway configuration load failed: %v", err)
	}

	if !reflect.DeepEqual(gotRemaining, wantRemaining) {
		t.Errorf("remaining arguments differ between loaders: %v %v", gotRemaining, wantRemaining)
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
	if got.PVE.Node != "pve-test" || got.PVE != want.PVE {
		t.Errorf("unexpected hypervisor node settings: %+v %+v", got.PVE, want.PVE)
	}
	if got.Network.PingTargetIPv4 != "192.0.2.1" || !reflect.DeepEqual(got.Network, want.Network) {
		t.Errorf("unexpected network settings: %+v %+v", got.Network, want.Network)
	}
	if got.Watchdog.ServiceName != "mwan-watchdog-test" || !reflect.DeepEqual(got.Watchdog, want.Watchdog) {
		t.Errorf("unexpected watchdog settings: %+v %+v", got.Watchdog, want.Watchdog)
	}
	if got.Failover.LXCID != "203" || got.Failover != want.Failover {
		t.Errorf("unexpected failover settings: %+v %+v", got.Failover, want.Failover)
	}
	if !reflect.DeepEqual(got.NotifyConfig(), want.NotifyConfig()) {
		t.Errorf("notification cadence differs between loaders: %+v %+v", got.NotifyConfig(), want.NotifyConfig())
	}
}

func TestLoadRejectsConflictingStatusSourceLikeGatewayLoader(t *testing.T) {
	writeConfig(t, conflictingStatusText)
	arguments := []string{"mwan"}

	_, _, err := watchdogconfig.LoadArguments(arguments)
	if err == nil {
		t.Fatalf("watchdog loader accepted conflicting status sources: %s", conflictingStatusText)
	}
	_, _, wantErr := config.LoadArguments(arguments)
	if wantErr == nil {
		t.Fatalf("gateway loader accepted conflicting status sources: %s", conflictingStatusText)
	}
	if err.Error() != wantErr.Error() {
		t.Errorf("status source errors differ between loaders: %q %q", err.Error(), wantErr.Error())
	}
}
