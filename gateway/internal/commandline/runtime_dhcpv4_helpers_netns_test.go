//go:build linux && (protocol || firewallnetns || systemd)

package commandline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startDHCPv4RuntimeKea(
	t *testing.T,
	root string,
	classless bool,
	leaseSeconds int,
	gateway string,
	reserved string,
	mountDirs bool,
) protocolService {
	t.Helper()
	for _, path := range []string{
		filepath.Join(root, "kea-run"),
		filepath.Join(root, "kea-state"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if mountDirs {
		bindStartupDirectory(t, filepath.Join(root, "kea-run"), "/run/kea")
		bindStartupDirectory(t, filepath.Join(root, "kea-state"), "/var/lib/kea")
	}
	config := fmt.Sprintf(
		`{"Dhcp4":{"interfaces-config":{"interfaces":["wan-vlan"]},"lease-database":{"type":"memfile","persist":true,"name":"/var/lib/kea/leases4.csv"},"valid-lifetime":%d,"subnet4":[{"id":1,"subnet":"198.51.100.0/24","pools":[{"pool":"198.51.100.100-198.51.100.110"}],"reservations":[{"client-id":"01:aa:bb","ip-address":"%s"}],"option-data":[{"name":"routers","data":"%s"}]}]}}`,
		leaseSeconds,
		reserved,
		gateway,
	)
	if classless {
		hooks, err := filepath.Glob("/usr/lib/*/kea/hooks/libdhcp_flex_option.so")
		if err != nil {
			t.Fatal(err)
		}
		if len(hooks) != 1 {
			t.Fatalf("expected one installed Kea flex-option hook, found %d: %v", len(hooks), hooks)
		}
		config = fmt.Sprintf(
			`{"Dhcp4":{"interfaces-config":{"interfaces":["wan-vlan"]},"lease-database":{"type":"memfile","persist":true,"name":"/var/lib/kea/leases4.csv"},"valid-lifetime":%d,"hooks-libraries":[{"library":%q,"parameters":{"options":[{"code":1,"supersede":"255.255.255.255"}]}}],"subnet4":[{"id":1,"subnet":"198.51.100.0/24","pools":[{"pool":"198.51.100.100-198.51.100.110"}],"reservations":[{"client-id":"01:aa:bb","ip-address":"%s"}],"option-data":[{"name":"routers","data":"198.51.100.9"},{"code":121,"name":"classless-static-route","data":"%s/32 - 0.0.0.0, 0.0.0.0/0 - %s"}]}]}}`,
			leaseSeconds,
			hooks[0],
			reserved,
			gateway,
			gateway,
		)
	}
	configPath := filepath.Join(root, "kea-dhcp4.conf")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "kea-dhcp4.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	command := exec.CommandContext(ctx, "kea-dhcp4", "-c", configPath, "-d")
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		cancel()
		logFile.Close()
		t.Fatal(err)
	}
	logFile.Close()
	service := protocolService{
		name:    "kea-dhcp4",
		command: command,
		logPath: logPath,
		cancel:  cancel,
	}
	ready := false
	defer func() {
		if !ready {
			stopProtocolService(t, service)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		assertProtocolServicesRunning(t, []protocolService{service})
		content, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte("DHCP4_STARTED")) {
			ready = true
			return service
		}
		time.Sleep(25 * time.Millisecond)
	}
	content, _ := os.ReadFile(logPath)
	t.Fatalf("Kea did not start: %s", strings.TrimSpace(string(content)))
	return service
}
