//go:build linux && (protocol || firewallnetns || systemd)

package commandline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type protocolService struct {
	name    string
	command *exec.Cmd
	logPath string
	cancel  context.CancelFunc
}

func startProtocolServices(t *testing.T, root string) []protocolService {
	t.Helper()
	return startProtocolServicesForInterface(t, root, "", 48)
}

func startProtocolServicesForInterface(
	t *testing.T,
	root string,
	subnetInterface string,
	delegatedPoolPrefixLength int,
) []protocolService {
	t.Helper()
	runtimeDir := filepath.Join(root, "kea-run")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, runtimeDir, "/run/kea")
	stateDir := filepath.Join(root, "kea-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, stateDir, "/var/lib/kea")
	dhcp4 := `{"Dhcp4":{"interfaces-config":{"interfaces":["wan-host"]},"lease-database":{"type":"memfile","persist":false},"valid-lifetime":600,"subnet4":[{"id":1,"subnet":"198.51.100.0/24","pools":[{"pool":"198.51.100.100-198.51.100.110"}],"option-data":[{"name":"routers","data":"198.51.100.2"}]}]}}`
	dhcp6 := fmt.Sprintf(
		`{"Dhcp6":{"interfaces-config":{"interfaces":["wan-host"]},"lease-database":{"type":"memfile","persist":false},"valid-lifetime":600,"preferred-lifetime":300,"subnet6":[{"id":1,"subnet":"2001:db8:2::/64","pools":[{"pool":"2001:db8:2::100-2001:db8:2::110"}],"pd-pools":[{"prefix":"2001:db8:30::","prefix-len":%d,"delegated-len":56}]}]}}`,
		delegatedPoolPrefixLength,
	)
	if subnetInterface != "" {
		dhcp6 = strings.Replace(
			dhcp6,
			`"subnet6":[{"id":1,`,
			fmt.Sprintf(`"subnet6":[{"id":1,"interface":%q,`, subnetInterface),
			1,
		)
	}
	ra := "interface wan-host { AdvSendAdvert on; MinRtrAdvInterval 3; MaxRtrAdvInterval 4; prefix 2001:db8:2::/64 { AdvOnLink on; AdvAutonomous on; }; };\n"
	services := []struct {
		name, config string
	}{
		{"kea-dhcp4", dhcp4},
		{"kea-dhcp6", dhcp6},
		{"radvd", ra},
	}
	started := make([]protocolService, 0, len(services))
	ready := false
	defer func() {
		if !ready {
			stopProtocolServices(t, started)
		}
	}()
	for _, service := range services {
		path := filepath.Join(root, service.name+".conf")
		if err := os.WriteFile(path, []byte(service.config), 0o600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(root, service.name+".log")
		arguments := []string{"-c", path, "-d"}
		if service.name == "radvd" {
			arguments = []string{"-n", "-C", path, "-p", filepath.Join(root, "radvd.pid")}
		}
		started = append(started, startProtocolService(t, service.name, logPath, arguments...))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		assertProtocolServicesRunning(t, started)
		if protocolServicesReady(t, started, filepath.Join(root, "radvd.pid")) {
			ready = true
			return started
		}
		if time.Now().After(deadline) {
			logs := make([]string, 0, len(started))
			for _, service := range started {
				content, err := os.ReadFile(service.logPath)
				if err != nil {
					t.Fatal(err)
				}
				logs = append(logs, service.name+": "+string(content))
			}
			t.Fatalf(
				"protocol services did not become ready within five seconds:\n%s",
				strings.Join(logs, "\n"),
			)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func startProtocolService(t *testing.T, name, logPath string, arguments ...string) protocolService {
	t.Helper()
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	// Cleanup sends SIGTERM after the test context ends.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start %s: %v", name, err)
	}
	return protocolService{name: name, command: command, logPath: logPath, cancel: cancel}
}

func protocolServicesReady(t *testing.T, services []protocolService, radvdPIDPath string) bool {
	t.Helper()
	for _, service := range services {
		if service.name == "radvd" {
			if _, err := os.Stat(radvdPIDPath); err != nil {
				return false
			}
			continue
		}
		content, err := os.ReadFile(service.logPath)
		if err != nil {
			t.Fatal(err)
		}
		marker := "DHCP4_STARTED"
		if service.name == "kea-dhcp6" {
			marker = "DHCP6_STARTED"
		}
		if !bytes.Contains(content, []byte(marker)) {
			return false
		}
	}
	return true
}

func assertProtocolServicesRunning(t *testing.T, services []protocolService) {
	t.Helper()
	for _, service := range services {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(service.command.Process.Pid, &status, syscall.WNOHANG, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			content, readErr := os.ReadFile(service.logPath)
			t.Fatalf(
				"%s exited early: status=%v read=%v log=%s",
				service.name,
				status,
				readErr,
				content,
			)
		}
	}
}

func stopProtocolServices(t *testing.T, services []protocolService) {
	t.Helper()
	for _, service := range services {
		stopProtocolService(t, service)
	}
}

func stopProtocolService(t *testing.T, service protocolService) {
	t.Helper()
	if service.cancel != nil {
		defer service.cancel()
	}
	if err := service.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("stop %s: %v", service.name, err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- service.command.Wait() }()
	select {
	case err := <-stopped:
		if err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				t.Errorf("wait %s: %v", service.name, err)
				return
			}
			status, ok := exitError.Sys().(syscall.WaitStatus)
			if !ok || status.Signal() != syscall.SIGTERM {
				t.Errorf("wait %s: %v", service.name, err)
			}
		}
	case <-time.After(3 * time.Second):
		if err := service.command.Process.Kill(); err != nil {
			t.Errorf("kill %s: %v", service.name, err)
		}
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Errorf("wait for %s after SIGKILL: deadline exceeded", service.name)
		}
		t.Errorf("%s did not stop after SIGTERM", service.name)
	}
}
