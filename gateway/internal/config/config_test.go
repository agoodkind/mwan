package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"goodkind.io/mwan/internal/config"
)

const sharedFileText = `
hostname = "gateway-test"
mwan_vmid = "4100"

[pve]
node = "pve-test"

[watchdog]
service_name = "mwan-watchdog-test"
connectivity_timeout_seconds = 45
status_listen_port = 50060

[failover]
lxc_id = "203"
agent_tcp_addr = "192.0.2.2:50052"

[agent]
vsock_port = 50071

[bgp]
enabled = false
asn = 65001
router_id = "192.0.2.3"

[ifmgr]
role = "wan"
reconcile_interval = "30s"
`

func TestLoadArgumentsAcceptsHypervisorSections(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(sharedFileText), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, remaining, err := config.LoadArguments([]string{"mwan", "--config", configPath, "--dry-run"})
	if err != nil {
		t.Fatalf("load config with hypervisor sections failed: %v", err)
	}

	wantRemaining := []string{"mwan", "--dry-run"}
	if !slices.Equal(remaining, wantRemaining) {
		t.Errorf("remaining arguments differ from expected arguments: %v %v", remaining, wantRemaining)
	}
	if cfg.Hostname != "gateway-test" || cfg.MwanVMID != "4100" {
		t.Errorf("hostname = %q mwan_vmid = %q, want gateway-test and 4100", cfg.Hostname, cfg.MwanVMID)
	}
	agentLoaded := cfg.Agent.VsockPort == 50071 && cfg.Agent.TCPAddr == "[::]:50052"
	bgpLoaded := cfg.BGP.ASN == 65001 && cfg.BGP.RouterID == "192.0.2.3"
	ifmgrLoaded := cfg.IfMgr.Role == "wan" && cfg.IfMgr.ReconcileInterval == "30s"
	if cfg.Path != configPath || !agentLoaded || !bgpLoaded || !ifmgrLoaded {
		t.Errorf("config path or gateway settings differ from expected values: %q %+v %+v %q %q",
			cfg.Path, cfg.Agent, cfg.BGP, cfg.IfMgr.Role, cfg.IfMgr.ReconcileInterval)
	}
}
