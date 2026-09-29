//go:build linux && firewallnetns

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	protocolRunnerChildEnv  = "MWAN_PROTOCOL_RUNNER_CHILD"
	protocolRunnerBinaryEnv = "MWAN_PROTOCOL_RUNNER_BINARY"
)

func TestProtocolRunnerBootstrap(t *testing.T) {
	if os.Getenv(protocolRunnerChildEnv) == "1" {
		runProtocolRunnerBootstrap(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network and mount namespaces require root")
	}
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mwan: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProtocolRunnerBootstrap$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), protocolRunnerChildEnv+"=1", protocolRunnerBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated protocol runner: %v: %s", err, output)
	}
}

func runProtocolRunnerBootstrap(t *testing.T) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte(`"goodkind-mwan-steering:link-files": "rendered"`), []byte(`"goodkind-mwan-steering:link-files": "hand-authored"`))
	legacy := []byte(`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`)
	typed := []byte(`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "` + runtimeWANMAC + `" }
        }`)
	if !bytes.Contains(fixture, legacy) {
		t.Fatal("network fixture has no external WAN entry")
	}
	fixture = bytes.Replace(fixture, legacy, typed, 1)
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	config := "[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = false\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	defer management.namespace.Close()
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer downstream.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enatt0", "wan-host", []string{"198.51.100.1/24", "2001:db8:2::1/64"}, []string{"198.51.100.2/24", "2001:db8:2::2/64"}, runtimeWANMAC)
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	services := startProtocolServices(t, root)
	defer stopProtocolServices(t, services)
	setRuntimeNamespace(t, gateway)
	addRuntimeDefault(t, "enatt0", "198.51.100.2")
	addRuntimeDefault(t, "enatt0", "2001:db8:2::2")
	addRuntimeRoute(t, gateway, downstream.namespace, "198.51.100.0/24", "192.0.2.1")
	addRuntimeRoute(t, gateway, provider.namespace, "192.0.2.0/29", "198.51.100.1")
	daemon := startRuntimeDaemon(t, os.Getenv(protocolRunnerBinaryEnv), configPath, root, "ifmgr")
	defer stopRuntimeDaemon(t, daemon)
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	waitRuntimeTable(t, daemon, "inet", "mwan_steer", 10*time.Second)
	setRuntimeNamespace(t, provider.namespace)
	assertProtocolServicesRunning(t, services)
	setRuntimeNamespace(t, gateway)
	waitRuntimeTCP(t, daemon, gateway, provider.namespace, downstream.namespace, "tcp4", "198.51.100.2:30522", "192.0.2.2:0", time.Now().Add(15*time.Second))
	setRuntimeNamespace(t, gateway)
	assertRuntimeDaemonRunning(t, daemon)
}

type protocolService struct {
	name    string
	command *exec.Cmd
	logPath string
}

func startProtocolServices(t *testing.T, root string) []protocolService {
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
	dhcp6 := `{"Dhcp6":{"interfaces-config":{"interfaces":["wan-host"]},"lease-database":{"type":"memfile","persist":false},"valid-lifetime":600,"preferred-lifetime":300,"subnet6":[{"id":1,"subnet":"2001:db8:2::/64","pools":[{"pool":"2001:db8:2::100-2001:db8:2::110"}],"pd-pools":[{"prefix":"2001:db8:30::","prefix-len":48,"delegated-len":56}]}]}}`
	ra := "interface wan-host { AdvSendAdvert on; MinRtrAdvInterval 3; MaxRtrAdvInterval 4; prefix 2001:db8:2::/64 { AdvOnLink on; AdvAutonomous on; }; };\n"
	services := []struct {
		name, executable, config string
	}{
		{"kea-dhcp4", "kea-dhcp4", dhcp4},
		{"kea-dhcp6", "kea-dhcp6", dhcp6},
		{"radvd", "radvd", ra},
	}
	started := make([]protocolService, 0, len(services))
	for _, service := range services {
		path := filepath.Join(root, service.name+".conf")
		if err := os.WriteFile(path, []byte(service.config), 0o600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(root, service.name+".log")
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		arguments := []string{"-c", path, "-d"}
		if service.name == "radvd" {
			arguments = []string{"-n", "-C", path, "-p", filepath.Join(root, "radvd.pid")}
		}
		command := exec.Command(service.executable, arguments...)
		command.Stdout = logFile
		command.Stderr = logFile
		if err := command.Start(); err != nil {
			logFile.Close()
			t.Fatal(err)
		}
		logFile.Close()
		started = append(started, protocolService{name: service.name, command: command, logPath: logPath})
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		assertProtocolServicesRunning(t, started)
		if protocolServicesReady(t, started, filepath.Join(root, "radvd.pid")) {
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
			t.Fatalf("protocol services did not become ready within five seconds:\n%s", strings.Join(logs, "\n"))
		}
		time.Sleep(25 * time.Millisecond)
	}
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
			t.Fatalf("%s exited early: status=%v read=%v log=%s", service.name, status, readErr, content)
		}
	}
}

func stopProtocolServices(t *testing.T, services []protocolService) {
	t.Helper()
	for _, service := range services {
		if err := service.command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Errorf("stop %s: %v", service.name, err)
			continue
		}
		stopped := make(chan error, 1)
		go func() { stopped <- service.command.Wait() }()
		select {
		case err := <-stopped:
			if err != nil {
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					t.Errorf("wait %s: %v", service.name, err)
					continue
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
			<-stopped
			t.Errorf("%s did not stop after SIGTERM", service.name)
		}
	}
}
