//go:build linux && firewall

package firewall_test

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

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/yangpub"
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
	binary := testenv.BuildTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestWANOwnershipRuntimeOperational$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		ownershipRuntimeChildEnv+"=1",
		ownershipRuntimeBinaryEnv+"="+binary,
	)
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
	fixture = bytes.ReplaceAll(
		fixture,
		[]byte(`"goodkind-mwan-steering:link-files": "rendered"`),
		[]byte(`"goodkind-mwan-steering:link-files": "hand-authored"`),
	)
	legacy := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored"`,
	)
	typed := []byte(
		`"name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:owner": "external",
        "goodkind-mwan-steering:link": {
          "match": { "hardware-address": "` + testenv.RuntimeWANMAC + `" }
        }`,
	)
	if !bytes.Contains(fixture, legacy) {
		t.Fatal("provider fixture changed")
	}
	fixture = bytes.Replace(fixture, legacy, typed, 1)
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	jsonLogPath := filepath.Join(root, "ifmgr.jsonl")
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\njson_log_file = %q\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = true\n",
		jsonLogPath,
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.SetRuntimeLoopback(t)
	management := testenv.NewRuntimePeer(
		t,
		gatewayNamespace,
		"enmgmt0",
		"mgmt-host",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	lan := testenv.NewRuntimePeer(
		t,
		gatewayNamespace,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer management.Namespace.Close()
	defer lan.Namespace.Close()
	testenv.SetRuntimeNamespace(t, gatewayNamespace)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	log := slog.Default()
	testenv.WithPrivateRepository(
		t,
		ctx,
		log,
		filepath.Join(root, "repository"),
		func(reader yangpub.Publisher) {
			binary := os.Getenv(ownershipRuntimeBinaryEnv)
			first := testenv.StartRuntimeDaemon(t, binary, configPath, root, "ownership-first")
			defer testenv.StopRuntimeDaemon(t, first)
			read := func() string {
				t.Helper()
				value, found, readErr := reader.ExportJSON(
					ctx,
					yangpub.DatastoreOperational,
					"/ietf-interfaces:*",
				)
				if readErr != nil || !found {
					return ""
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, []byte(value)); err != nil {
					t.Fatal(err)
				}
				return compact.String()
			}
			testenv.WaitRuntimeOwnershipRead(t, first, read, `"connection-id":"att"`)
			testenv.WaitRuntimeOwnershipRead(t, first, read, `"actual-name":"enmgmt0"`)
			testenv.WaitRuntimeOwnershipRead(t, first, read, `"actual-name":"enmwanbr0"`)
			wan := testenv.NewRuntimePeer(
				t,
				gatewayNamespace,
				"enatt0",
				"wan-host",
				[]string{"198.51.100.1/24"},
				[]string{"198.51.100.2/24"},
				testenv.RuntimeWANMAC,
			)
			defer wan.Namespace.Close()
			testenv.SetRuntimeNamespace(t, gatewayNamespace)
			link, err := netlink.LinkByName("enatt0")
			if err != nil {
				t.Fatal(err)
			}
			oldIndex := link.Attrs().Index
			testenv.WaitRuntimeOwnershipRead(
				t,
				first,
				read,
				fmt.Sprintf(`"actual-index":%d`, oldIndex),
			)
			observed := read()
			if !strings.Contains(observed, `"acquisition":"unknown"`) ||
				strings.Contains(observed, `"assignment":[`) {
				t.Fatalf("kernel state claimed a lease: %s", observed)
			}
			if err := netlink.LinkDel(link); err != nil {
				t.Fatal(err)
			}
			testenv.WaitRuntimeOwnershipRead(t, first, read, `"link-state":"absent"`)
			wan = testenv.NewRuntimePeer(
				t,
				gatewayNamespace,
				"enatt0",
				"wan-new",
				[]string{"198.51.100.1/24"},
				[]string{"198.51.100.2/24"},
				testenv.RuntimeWANMAC,
			)
			defer wan.Namespace.Close()
			testenv.SetRuntimeNamespace(t, gatewayNamespace)
			replacement, err := netlink.LinkByName("enatt0")
			if err != nil {
				t.Fatal(err)
			}
			if replacement.Attrs().Index == oldIndex {
				t.Fatal("replacement reused old index")
			}
			testenv.WaitRuntimeOwnershipRead(
				t,
				first,
				read,
				fmt.Sprintf(`"actual-index":%d`, replacement.Attrs().Index),
			)
			current := read()
			if !strings.Contains(current, `"observed-at":"`) ||
				!strings.Contains(current, `"recent-transition":[`) {
				t.Fatalf(
					"operational read lacks observation time or transition history: %s",
					current,
				)
			}
			testenv.StopRuntimeDaemon(t, first)
			firstHistory, err := os.ReadFile(jsonLogPath)
			if err != nil {
				t.Fatal(err)
			}
			if transitions := attLinkTransitions(t, firstHistory); len(transitions) == 0 {
				t.Fatal("persistent log did not record a link transition for att")
			}
			second := testenv.StartRuntimeDaemon(t, binary, configPath, root, "ownership-second")
			defer testenv.StopRuntimeDaemon(t, second)
			testenv.WaitRuntimeOwnershipRead(
				t,
				second,
				read,
				fmt.Sprintf(`"actual-index":%d`, replacement.Attrs().Index),
			)
			testenv.WaitRuntimeOwnershipRead(t, second, read, `"firewall-protection":"ready"`)
			testenv.StopRuntimeDaemon(t, second)
			secondHistory, err := os.ReadFile(jsonLogPath)
			if err != nil {
				t.Fatal(err)
			}
			restartHistory, ok := bytes.CutPrefix(secondHistory, firstHistory)
			if !ok {
				t.Fatal("restart did not preserve the first daemon's log")
			}
			if transitions := attLinkTransitions(t, restartHistory); len(transitions) != 0 {
				t.Fatalf("restart recorded link transitions for the unchanged att interface: %q", transitions)
			}
		},
	)
}

func attLinkTransitions(t *testing.T, history []byte) []string {
	t.Helper()
	var transitions []string
	for line := range bytes.Lines(history) {
		var record struct {
			Message      string          `json:"msg"`
			ConnectionID string          `json:"connection_id"`
			Transition   json.RawMessage `json:"transition"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode daemon log record: %v: %q", err, line)
		}
		if record.Message != "ifmgr: interface transition" || record.ConnectionID != "att" {
			continue
		}
		var transition struct {
			Operation string
		}
		if err := json.Unmarshal(record.Transition, &transition); err != nil {
			t.Fatalf("decode att interface transition: %v: %q", err, line)
		}
		if transition.Operation == "observe-link" {
			transitions = append(transitions, string(bytes.TrimSpace(line)))
		}
	}
	return transitions
}
