package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIfMgrValidatesLegacySLAACSettingsForFailover(t *testing.T) {
	binary := buildReportCommand(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	config := "[ifmgr.iface.test0]\n[ifmgr.modules.slaac_health]\niface = \"test0\"\nescalate_after = \"soon\"\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	output, diagnostics, code := runCLIProcess(t, binary, "notify", "--self-test", "--dry-run", "--config", path)
	if code != 0 {
		t.Fatalf("notify exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
	_, diagnostics, code = runCLIProcess(t, binary, "ifmgr", "--role", "failover", "--config", path)
	if code != 1 || !strings.Contains(diagnostics, "ifmgr.modules.slaac_health.escalate_after") {
		t.Fatalf("ifmgr exit=%d diagnostics=%s", code, diagnostics)
	}
	config = "[ifmgr.iface.test0]\n[ifmgr.modules.slaac_health]\niface = \"test0\"\nescalate_after = \"90s\"\nmax_toggles_per_hour = 4\ndegraded_after = \"soon\"\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostics, code = runCLIProcess(t, binary, "ifmgr", "--role", "failover", "--config", path)
	if code != 1 || !strings.Contains(diagnostics, "ifmgr.modules.slaac_health.degraded_after") {
		t.Fatalf("ifmgr exit=%d diagnostics=%s", code, diagnostics)
	}
}
