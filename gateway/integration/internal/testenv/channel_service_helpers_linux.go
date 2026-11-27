//go:build linux && (firewall || protocol || systemd)

package testenv

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

type ProtocolService struct {
	Name    string
	Command *exec.Cmd
	LogPath string
	Cancel  context.CancelFunc
}

func StartProtocolServices(t *testing.T, root string) []ProtocolService {
	t.Helper()
	return StartProtocolServicesForInterface(t, root, "", 48)
}

func StartProtocolServicesForInterface(
	t *testing.T,
	root string,
	subnetInterface string,
	delegatedPoolPrefixLength int,
) []ProtocolService {
	t.Helper()
	runtimeDir := filepath.Join(root, "kea-run")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	BindStartupDirectory(t, runtimeDir, "/run/kea")
	stateDir := filepath.Join(root, "kea-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	BindStartupDirectory(t, stateDir, "/var/lib/kea")
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
		Name, config string
	}{
		{"kea-dhcp4", dhcp4},
		{"kea-dhcp6", dhcp6},
		{"radvd", ra},
	}
	started := make([]ProtocolService, 0, len(services))
	ready := false
	defer func() {
		if !ready {
			StopProtocolServices(t, started)
		}
	}()
	for _, service := range services {
		path := filepath.Join(root, service.Name+".conf")
		if err := os.WriteFile(path, []byte(service.config), 0o600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(root, service.Name+".log")
		arguments := []string{"-c", path, "-d"}
		if service.Name == "radvd" {
			arguments = []string{"-n", "-C", path, "-p", filepath.Join(root, "radvd.pid")}
		}
		started = append(started, StartProtocolService(t, service.Name, logPath, arguments...))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		AssertProtocolServicesRunning(t, started)
		if ProtocolServicesReady(t, started, filepath.Join(root, "radvd.pid")) {
			ready = true
			return started
		}
		if time.Now().After(deadline) {
			logs := make([]string, 0, len(started))
			for _, service := range started {
				content, err := os.ReadFile(service.LogPath)
				if err != nil {
					t.Fatal(err)
				}
				logs = append(logs, service.Name+": "+string(content))
			}
			t.Fatalf(
				"protocol services did not become ready within five seconds:\n%s",
				strings.Join(logs, "\n"),
			)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func StartProtocolService(t *testing.T, name, logPath string, arguments ...string) ProtocolService {
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
	return ProtocolService{Name: name, Command: command, LogPath: logPath, Cancel: cancel}
}

func ProtocolServicesReady(t *testing.T, services []ProtocolService, radvdPIDPath string) bool {
	t.Helper()
	for _, service := range services {
		if service.Name == "radvd" {
			if _, err := os.Stat(radvdPIDPath); err != nil {
				return false
			}
			continue
		}
		content, err := os.ReadFile(service.LogPath)
		if err != nil {
			t.Fatal(err)
		}
		marker := "DHCP4_STARTED"
		if service.Name == "kea-dhcp6" {
			marker = "DHCP6_STARTED"
		}
		if !bytes.Contains(content, []byte(marker)) {
			return false
		}
	}
	return true
}

func AssertProtocolServicesRunning(t *testing.T, services []ProtocolService) {
	t.Helper()
	for _, service := range services {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(service.Command.Process.Pid, &status, syscall.WNOHANG, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			content, readErr := os.ReadFile(service.LogPath)
			t.Fatalf(
				"%s exited early: status=%v read=%v log=%s",
				service.Name,
				status,
				readErr,
				content,
			)
		}
	}
}

func StopProtocolServices(t *testing.T, services []ProtocolService) {
	t.Helper()
	for _, service := range services {
		StopProtocolService(t, service)
	}
}

func StopProtocolService(t *testing.T, service ProtocolService) {
	t.Helper()
	if service.Cancel != nil {
		defer service.Cancel()
	}
	if err := service.Command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("stop %s: %v", service.Name, err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- service.Command.Wait() }()
	select {
	case err := <-stopped:
		if err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				t.Errorf("wait %s: %v", service.Name, err)
				return
			}
			status, ok := exitError.Sys().(syscall.WaitStatus)
			if !ok || status.Signal() != syscall.SIGTERM {
				t.Errorf("wait %s: %v", service.Name, err)
			}
		}
	case <-time.After(3 * time.Second):
		if err := service.Command.Process.Kill(); err != nil {
			t.Errorf("kill %s: %v", service.Name, err)
		}
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Errorf("wait for %s after SIGKILL: deadline exceeded", service.Name)
		}
		t.Errorf("%s did not stop after SIGTERM", service.Name)
	}
}
