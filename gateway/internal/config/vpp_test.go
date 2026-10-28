package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
)

func TestLoadVPPResourceRequirements(t *testing.T) {
	base := `[ifmgr.modules.vpp]
api_socket = "/run/vpp/api.sock"
stats_socket = "/run/vpp/stats.sock"
expected_version = "26.06"
request_timeout = "5s"
state_file = "/var/lib/mwan/vpp.json"
profile = false
profile_queue_polling = false
[[ifmgr.modules.vpp.interface]]
connection_id = "inside"
host_connection_id = "host-inside"
id = 64
rx_ring_size = 256
tx_ring_size = 256
rx_queues = 1
tx_queues = 1
worker = 0
rx_mode = "adaptive"
`
	for _, item := range []struct {
		name, old, replacement string
		fail                   bool
	}{
		{name: "configured resources"},
		{name: "relative API socket", old: `"/run/vpp/api.sock"`, replacement: `"api.sock"`, fail: true},
		{name: "wrong binary API version", old: `"26.06"`, replacement: `"25.10"`, fail: true},
		{name: "missing timeout", old: `request_timeout = "5s"`, fail: true},
		{name: "zero queue count", old: `rx_queues = 1`, replacement: `rx_queues = 0`, fail: true},
		{name: "missing RX mode", old: `rx_mode = "adaptive"`, fail: true},
		{name: "queue profiling requires native profiling", old: `profile_queue_polling = false`, replacement: `profile_queue_polling = true`, fail: true},
		{name: "optional queue profiling", old: "profile = false\nprofile_queue_polling = false", replacement: "profile = true\nprofile_queue_polling = true\nprofile_executable = \"/usr/bin/vpp\"\nprofile_duration = \"250ms\""},
	} {
		t.Run(item.name, func(t *testing.T) {
			text := base
			if item.old != "" {
				text = strings.Replace(text, item.old, item.replacement, 1)
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := config.LoadArguments([]string{"mwan", "--config", path})
			if item.fail {
				if err == nil {
					t.Fatal("LoadArguments accepted invalid VPP resources")
				}
				return
			}
			if err != nil || loaded.IfMgr.Modules.VPP == nil {
				t.Fatalf("load VPP resources: %v", err)
			}
		})
	}
}
