//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	ownershipRuntimeChildEnv  = "MWAN_OWNERSHIP_RUNTIME_CHILD"
	ownershipRuntimeBinaryEnv = "MWAN_OWNERSHIP_RUNTIME_BINARY"
)

func TestWANOwnershipRuntimeOperational(t *testing.T) {
	if os.Getenv(ownershipRuntimeChildEnv) == "1" {
		runWANOwnershipRuntimeChild(t)
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
	child := exec.Command(os.Args[0], "-test.run=^TestWANOwnershipRuntimeOperational$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), ownershipRuntimeChildEnv+"=1", ownershipRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated daemon operational read: %v: %s", err, output)
	}
}

func runWANOwnershipRuntimeChild(t *testing.T) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gatewayNamespace, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gatewayNamespace.Close()
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
		t.Fatal("provider fixture changed")
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
	jsonLogPath := filepath.Join(root, "ifmgr.jsonl")
	config := fmt.Sprintf("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\njson_log_file = %q\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = true\n", jsonLogPath)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	management := newRuntimePeer(t, gatewayNamespace, "enmgmt0", "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24"}, "")
	lan := newRuntimePeer(t, gatewayNamespace, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer management.namespace.Close()
	defer lan.namespace.Close()
	setRuntimeNamespace(t, gatewayNamespace)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	log := slog.Default()
	reader, closeRepository, err := openPrivateRepository(ctx, log, selftestFlags{repository: filepath.Join(root, "repository"), modelsDir: selftestModelsDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepository()
	binary := os.Getenv(ownershipRuntimeBinaryEnv)
	first := startRuntimeDaemon(t, binary, configPath, root, "ownership-first")
	defer stopRuntimeDaemon(t, first)
	read := func() string {
		t.Helper()
		value, found, readErr := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
		if readErr != nil || !found {
			return ""
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(value)); err != nil {
			t.Fatal(err)
		}
		return compact.String()
	}
	waitRuntimeOwnershipRead(t, first, read, `"connection-id":"att"`)
	waitRuntimeOwnershipRead(t, first, read, `"actual-name":"enmgmt0"`)
	waitRuntimeOwnershipRead(t, first, read, `"actual-name":"enmwanbr0"`)
	wan := newRuntimePeer(t, gatewayNamespace, "enatt0", "wan-host", []string{"198.51.100.1/24"}, []string{"198.51.100.2/24"}, runtimeWANMAC)
	defer wan.namespace.Close()
	setRuntimeNamespace(t, gatewayNamespace)
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	oldIndex := link.Attrs().Index
	waitRuntimeOwnershipRead(t, first, read, fmt.Sprintf(`"actual-index":%d`, oldIndex))
	observed := read()
	if !strings.Contains(observed, `"acquisition":"unknown"`) || strings.Contains(observed, `"assignment":[`) {
		t.Fatalf("kernel state claimed a lease: %s", observed)
	}
	if err := netlink.LinkDel(link); err != nil {
		t.Fatal(err)
	}
	waitRuntimeOwnershipRead(t, first, read, `"link-state":"absent"`)
	wan = newRuntimePeer(t, gatewayNamespace, "enatt0", "wan-new", []string{"198.51.100.1/24"}, []string{"198.51.100.2/24"}, runtimeWANMAC)
	defer wan.namespace.Close()
	setRuntimeNamespace(t, gatewayNamespace)
	replacement, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Attrs().Index == oldIndex {
		t.Fatal("replacement reused old index")
	}
	waitRuntimeOwnershipRead(t, first, read, fmt.Sprintf(`"actual-index":%d`, replacement.Attrs().Index))
	current := read()
	if !strings.Contains(current, `"observed-at":"`) || !strings.Contains(current, `"recent-transition":[`) {
		t.Fatalf("operational read lacks observation time or transition history: %s", current)
	}
	stopRuntimeDaemon(t, first)
	firstHistory, err := os.ReadFile(jsonLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(firstHistory, []byte(`"msg":"ifmgr: interface transition"`)) {
		t.Fatalf("persistent log has no transition: %s", firstHistory)
	}
	second := startRuntimeDaemon(t, binary, configPath, root, "ownership-second")
	defer stopRuntimeDaemon(t, second)
	waitRuntimeOwnershipRead(t, second, read, fmt.Sprintf(`"actual-index":%d`, replacement.Attrs().Index))
	secondHistory, err := os.ReadFile(jsonLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(secondHistory, []byte(`"msg":"ifmgr: interface transition"`)) != bytes.Count(firstHistory, []byte(`"msg":"ifmgr: interface transition"`)) {
		t.Fatalf("restart created a transition: %s", secondHistory)
	}
}

func waitRuntimeOwnershipRead(t *testing.T, daemon *runtimeDaemon, read func() string, expected string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(read(), expected) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("operational read lacks %s: %s; daemon: %s", expected, read(), runtimeLogTail(t, daemon, 30))
}
