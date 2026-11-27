//go:build linux && firewall

package firewall_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/forwardingready"
	"goodkind.io/mwan/gateway/internal/yangpub"
)

const selectionReadinessChildEnv = "MWAN_SELECTION_READINESS_CHILD"

func TestSelectionExcludedReadinessDaemonRuntime(t *testing.T) {
	if os.Getenv(selectionReadinessChildEnv) == "1" {
		runSelectionExcludedReadinessDaemonRuntime(t)
		return
	}
	t.Parallel()
	binary := testenv.ProtocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestSelectionExcludedReadinessDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		selectionReadinessChildEnv+"=1",
		testenv.MappedRuntimeBinaryEnv+"="+binary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated selection readiness test: %v: %s", err, output)
	}
}

func runSelectionExcludedReadinessDaemonRuntime(t *testing.T) {
	testenv.MountPrivateRuntimeDirectories(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	networkDir := filepath.Join(root, "network")
	networkdDir := filepath.Join(root, "networkd")
	for _, directory := range []string{networkDir, networkdDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	socket := filepath.Join(root, "forwarding.sock")
	configPath := filepath.Join(root, "config.toml")
	configuration := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = true\n[bgp]\nenabled = true\nuse_wanconfig = true\n[bgp.forwarding_readiness]\nsocket_path = %q\npoll_interval_milliseconds = 100\nread_timeout_milliseconds = 500\n",
		socket,
	)
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.SetRuntimeLoopback(t)
	management := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"management",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	defer management.Namespace.Close()
	lan := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"downstream",
		[]string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"},
		[]string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"},
		"",
	)
	defer lan.Namespace.Close()
	excludedPeer := testenv.NewRuntimePeer(
		t,
		gateway,
		"enwebpass0",
		"excluded",
		[]string{"10.50.1.1/24", "fd50:1::1/64"},
		[]string{"10.50.1.2/24", "fd50:1::2/64"},
		"",
	)
	defer excludedPeer.Namespace.Close()
	selectablePeer := testenv.NewRuntimePeer(
		t,
		gateway,
		"enatt0",
		"selectable",
		[]string{"10.50.2.1/24", "fd50:2::1/64"},
		[]string{"10.50.2.2/24", "fd50:2::2/64"},
		"",
	)
	defer selectablePeer.Namespace.Close()
	testenv.SetRuntimeNamespace(t, gateway)
	for _, name := range []string{"enmwanbr0", "enwebpass0", "enatt0"} {
		testenv.WaitAutoconfigurationLinkLocal(t, name)
	}
	for _, path := range []string{
		"/proc/sys/net/ipv4/ip_forward",
		"/proc/sys/net/ipv6/conf/all/forwarding",
	} {
		if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Start the excluded provider with a usable path. Its readiness must not count toward
	// gateway readiness.
	addSelectionReadinessDefaults(t, "enwebpass0", "10.50.1.2", "fd50:1::2", 100)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	testenv.WithPrivateRepository(
		t,
		ctx,
		slog.Default(),
		filepath.Join(root, "repository"),
		func(reader yangpub.Publisher) {
			read := func() string {
				data, found, err := reader.ExportJSON(
					ctx,
					yangpub.DatastoreOperational,
					"/ietf-interfaces:*",
				)
				if err != nil || !found {
					return ""
				}
				return data
			}
			writeSelectionRuntimeNetwork(t, networkDir, false)
			// Seed the pinned destination sets in this isolated namespace. It does not run the
			// systemd updater.
			removeSelectionReadinessPinnedProvider(t, networkDir)
			daemon := testenv.StartRuntimeDaemon(
				t,
				os.Getenv(testenv.MappedRuntimeBinaryEnv),
				configPath,
				root,
				"selection-readiness",
			)
			defer testenv.KillOwnedRuntimeDaemon(t, daemon)
			waitSelectionReadinessRouting(
				t,
				daemon,
				read,
				map[string]string{"enwebpass0": "ready", "enatt0": "not-ready"},
			)
			testenv.WaitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{})
			addSelectionReadinessDefaults(t, "enatt0", "10.50.2.2", "fd50:2::2", 101)
			waitSelectionReadinessRouting(
				t,
				daemon,
				read,
				map[string]string{"enwebpass0": "ready", "enatt0": "ready"},
			)
			testenv.WaitOwnedRoleForwarding(
				t,
				daemon,
				socket,
				forwardingready.State{IPv4: true, IPv6: true},
			)
		},
	)
}

func addSelectionReadinessDefaults(t *testing.T, name, gatewayV4, gatewayV6 string, priority int) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, gatewayAddress := range []string{gatewayV4, gatewayV6} {
		route := netlink.Route{
			LinkIndex: link.Attrs().Index,
			Gw:        net.ParseIP(gatewayAddress),
			Priority:  priority,
			Table:     unix.RT_TABLE_MAIN,
		}
		if err := netlink.RouteAdd(&route); err != nil {
			t.Fatal(err)
		}
	}
}

func removeSelectionReadinessPinnedProvider(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	interfaces := document["ietf-interfaces:interfaces"]
	var group map[string]json.RawMessage
	if err := json.Unmarshal(
		interfaces["goodkind-mwan-steering:steering-group"],
		&group,
	); err != nil {
		t.Fatal(err)
	}
	group["firewall"] = json.RawMessage(
		`{"management-interface":"enmgmt0","management-service":[{"protocol":"tcp","port":22}],"pinned-set-v4-name":"att_pinned_v4","pinned-set-v6-name":"att_pinned_v6"}`,
	)
	if interfaces["goodkind-mwan-steering:steering-group"], err = json.Marshal(group); err != nil {
		t.Fatal(err)
	}
	if data, err = json.Marshal(document); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitSelectionReadinessRouting(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	read func() string,
	wanted map[string]string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var document struct {
			Interfaces struct {
				Entries []struct {
					Name string                 `json:"name"`
					V4   selectionRuntimeFamily `json:"ietf-ip:ipv4"`
					V6   selectionRuntimeFamily `json:"ietf-ip:ipv6"`
				} `json:"interface"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if json.Unmarshal([]byte(read()), &document) == nil {
			matched := 0
			for _, entry := range document.Interfaces.Entries {
				routing, found := wanted[entry.Name]
				if found && entry.V4.Ownership.Routing == routing &&
					entry.V6.Ownership.Routing == routing {
					matched++
				}
			}
			if matched == len(wanted) {
				return
			}
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"provider routing %v absent: %s; daemon=%s",
		wanted,
		read(),
		testenv.RuntimeLogTail(t, daemon, 100),
	)
}
