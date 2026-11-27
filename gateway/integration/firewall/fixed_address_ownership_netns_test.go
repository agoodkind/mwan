//go:build linux && firewall

package firewall_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
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
	staticRuntimeChildEnv  = "MWAN_STATIC_RUNTIME_TEST_CHILD"
	staticRuntimeBinaryEnv = "MWAN_STATIC_RUNTIME_TEST_BINARY"
)

func TestOwnedStaticDaemonRuntime(t *testing.T) {
	if os.Getenv(staticRuntimeChildEnv) == "1" {
		runOwnedStaticDaemonRuntime(t)
		return
	}
	t.Parallel()
	binary := testenv.ProtocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestOwnedStaticDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), staticRuntimeChildEnv+"=1", staticRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated static daemon test: %v: %s", err, output)
	}
}

func runOwnedStaticDaemonRuntime(t *testing.T) {
	testenv.MountPrivateRuntimeDirectories(t)
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
	schemaDir, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkDir, "/etc/mwan")
	testenv.BindStartupDirectory(t, schemaDir, "/usr/local/share/wanconfig/yang")
	networkdDir := filepath.Join(root, "networkd")
	if err := os.MkdirAll(networkdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.BindStartupDirectory(t, networkdDir, "/etc/systemd/network")
	configPath := filepath.Join(root, "config.toml")
	staticStateDir := filepath.Join(root, "static-state")
	if err := os.MkdirAll(staticStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(
		"[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[ifmgr.modules.links]\nstate_file = %q\n[ifmgr.modules.addresses]\nstate_file = %q\n[ifmgr.modules.autoconfiguration]\nstate_file = %q\n[wanconfig]\npublish = true\n",
		filepath.Join(root, "owned-links.json"),
		filepath.Join(staticStateDir, "owned-static.json"),
		filepath.Join(root, "kernel-policy.json"),
	)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.SetRuntimeLoopback(t)
	management := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"mgmt-host",
		[]string{"203.0.113.1/24"},
		[]string{"203.0.113.2/24"},
		"",
	)
	defer management.Namespace.Close()
	lan := testenv.NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29", "2001:db8:b01:fe::1/64"},
		[]string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"},
		"",
	)
	defer lan.Namespace.Close()
	parentPeer := testenv.NewRuntimePeer(
		t,
		gateway,
		"ownphys0",
		"owned-peer",
		nil,
		nil,
		testenv.OwnedRuntimeParentMAC,
	)
	defer parentPeer.Namespace.Close()
	testenv.SetRuntimeNamespace(t, parentPeer.Namespace)
	parent, err := netlink.LinkByName("owned-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{Name: "peer397", ParentIndex: parent.Attrs().Index},
		VlanId:    397,
	}); err != nil {
		t.Fatal(err)
	}
	testenv.ConfigureRuntimeLink(
		t,
		"peer397",
		[]string{
			"10.39.7.2/24",
			"10.39.7.3/24",
			"10.39.9.2/24",
			"fd39:7::2/64",
			"fd39:7::3/64",
			"fd39:9::2/64",
		},
	)
	if err := os.WriteFile(
		"/proc/sys/net/ipv4/conf/all/rp_filter",
		[]byte("2"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	// Loose reverse path validation requires a return route for the downstream source.
	_, downstreamV4, err := net.ParseCIDR("192.0.2.0/29")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		Dst: downstreamV4,
		Gw:  net.ParseIP("10.39.7.1"),
	}); err != nil {
		t.Fatal(err)
	}
	testenv.SetRuntimeNamespace(t, gateway)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	testenv.WithPrivateRepository(
		t,
		ctx,
		slog.Default(),
		filepath.Join(root, "repository"),
		func(reader yangpub.Publisher) {
			read := func() string {
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
			testenv.WriteStaticRuntimeNetwork(
				t,
				networkDir,
				"10.39.7.1",
				"fd39:7::1",
				"10.39.7.2",
				"fd39:7::2",
				398,
				24,
				true,
			)
			_, isolatedDestination, err := net.ParseCIDR("10.39.9.0/24")
			if err != nil {
				t.Fatal(err)
			}
			// Install a fallback route. A missing specific route must select that fallback for the
			// test to detect it.
			if err := netlink.RouteAdd(&netlink.Route{
				Dst:      isolatedDestination,
				Table:    unix.RT_TABLE_MAIN,
				Type:     unix.RTN_BLACKHOLE,
				Priority: 10000,
				Protocol: unix.RTPROT_STATIC,
			}); err != nil {
				t.Fatal(err)
			}
			binary := os.Getenv(staticRuntimeBinaryEnv)
			first := testenv.StartRuntimeDaemon(t, binary, configPath, root, "static-first")
			defer testenv.KillOwnedRuntimeDaemon(t, first)
			defer func() {
				if !t.Failed() {
					return
				}
				testenv.SetRuntimeNamespace(t, gateway)
				output, commandErr := exec.Command("nft", "list", "ruleset").CombinedOutput()
				t.Logf("failed packet rules: %s error=%v", output, commandErr)
				routes, routeErr := netlink.RouteListFiltered(
					netlink.FAMILY_ALL,
					&netlink.Route{Table: unix.RT_TABLE_MAIN},
					netlink.RT_FILTER_TABLE,
				)
				t.Logf("failed packet main routes=%v error=%v", routes, routeErr)
				t.Logf("failed packet public state=%s", read())
			}()
			testenv.WaitRuntimeTable(t, first, "inet", "filter", 10*time.Second)
			testenv.WaitStaticRuntimeAddress(t, first, "owned397", "10.39.7.1/24", true)
			promotionPath := "/proc/sys/net/ipv4/conf/owned397/promote_secondaries"
			promotion, err := os.ReadFile(promotionPath)
			if err != nil || string(promotion) != "1\n" {
				t.Fatalf("owned IPv4 promotion: value=%q err=%v", promotion, err)
			}
			testenv.WaitStaticRuntimeAddress(t, first, "owned397", "fd39:7::1/64", true)
			testenv.WaitStaticRuntimeRoute(t, first, "ipv4", "10.39.7.2", 398, true)
			testenv.WaitStaticRuntimeRoute(t, first, "ipv6", "fd39:7::2", 398, true)
			configuredRoute := testenv.WaitConfiguredRuntimeRoute(t, first, "10.39.9.0/24")
			testenv.WaitConfiguredRuntimeRoute(t, first, "fd39:9::/64")
			testenv.WaitRuntimeOwnershipRead(t, first, read, `"kind":"static-route"`)
			testenv.WaitRuntimeOwnershipRead(t, first, read, `"gateway":"10.39.7.2"`)
			testenv.WaitOwnedRuntimeAbsent(t, first, "late397", 10*time.Second)
			testenv.AssertStaticRuntimePacket(
				t,
				gateway,
				parentPeer.Namespace,
				"udp4",
				"10.39.7.2:39701",
			)
			testenv.AssertStaticRuntimePacket(
				t,
				gateway,
				parentPeer.Namespace,
				"udp6",
				"[fd39:7::2]:39702",
			)
			if err := os.WriteFile(
				"/proc/sys/net/ipv4/ip_forward",
				[]byte("1"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			testenv.SetRuntimeNamespace(t, lan.Namespace)
			if err := netlink.RouteAdd(&netlink.Route{Gw: net.ParseIP("192.0.2.1")}); err != nil {
				t.Fatal(err)
			}
			testenv.SetRuntimeNamespace(t, gateway)
			_, selectedDestination, err := net.ParseCIDR("10.39.9.0/24")
			if err != nil {
				t.Fatal(err)
			}
			selectedRoute := netlink.NewRule()
			selectedRoute.Priority = 10
			selectedRoute.Table = unix.RT_TABLE_MAIN
			selectedRoute.Dst = selectedDestination
			if err := netlink.RuleAdd(selectedRoute); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				"/proc/sys/net/ipv6/conf/all/forwarding",
				[]byte("1"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			testenv.SetRuntimeNamespace(t, lan.Namespace)
			if err := netlink.RouteAdd(&netlink.Route{
				Gw: net.ParseIP("2001:db8:b01:fe::1"),
			}); err != nil {
				t.Fatal(err)
			}
			testenv.SetRuntimeNamespace(t, gateway)
			_, selectedDestinationV6, err := net.ParseCIDR("fd39:9::/64")
			if err != nil {
				t.Fatal(err)
			}
			selectedRouteV6 := netlink.NewRule()
			selectedRouteV6.Family = unix.AF_INET6
			selectedRouteV6.Priority = 10
			selectedRouteV6.Table = unix.RT_TABLE_MAIN
			selectedRouteV6.Dst = selectedDestinationV6
			if err := netlink.RuleAdd(selectedRouteV6); err != nil {
				t.Fatal(err)
			}
			if err := netlink.RouteAdd(&netlink.Route{
				Dst:      selectedDestinationV6,
				Table:    unix.RT_TABLE_MAIN,
				Type:     unix.RTN_BLACKHOLE,
				Priority: 10000,
				Protocol: unix.RTPROT_STATIC,
			}); err != nil {
				t.Fatal(err)
			}
			waitStaticRuntimeSelection(t, first, read)
			testenv.AssertStaticRuntimePacket(
				t,
				lan.Namespace,
				parentPeer.Namespace,
				"udp6",
				"[fd39:9::2]:39803",
			)
			testenv.SetRuntimeNamespace(t, gateway)
			configuredRouteV6 := testenv.WaitConfiguredRuntimeRoute(t, first, "fd39:9::/64")
			if err := netlink.RouteDel(&configuredRouteV6); err != nil {
				t.Fatal(err)
			}
			testenv.WaitConfiguredRuntimeRoute(t, first, "fd39:9::/64")
			testenv.AssertStaticRuntimePacket(
				t,
				lan.Namespace,
				parentPeer.Namespace,
				"udp6",
				"[fd39:9::2]:39804",
			)
			testenv.SetRuntimeNamespace(t, gateway)
			selection, selectionErr := exec.Command(
				"nft",
				"list",
				"chain",
				"inet",
				"mwan_steer",
				"prerouting",
			).CombinedOutput()
			t.Logf(
				"initial IPv4 packet selection: %s error=%v public state=%s",
				selection,
				selectionErr,
				read(),
			)
			for _, path := range []string{
				"/proc/sys/net/ipv4/ip_forward",
				"/proc/sys/net/ipv4/conf/all/rp_filter",
				"/proc/sys/net/ipv4/conf/enmwanbr0/rp_filter",
				"/proc/sys/net/ipv4/conf/all/src_valid_mark",
			} {
				value, readErr := os.ReadFile(path)
				t.Logf("initial IPv4 packet setting %s=%q error=%v", path, value, readErr)
			}
			defer captureStaticRuntimeFailure(t)()
			testenv.AssertStaticRuntimePacket(
				t,
				lan.Namespace,
				parentPeer.Namespace,
				"udp4",
				"10.39.9.2:39703",
			)
			testenv.SetRuntimeNamespace(t, gateway)
			if err := netlink.RouteDel(&configuredRoute); err != nil {
				t.Fatal(err)
			}
			testenv.WaitConfiguredRuntimeRoute(t, first, "10.39.9.0/24")
			testenv.AssertStaticRuntimePacket(
				t,
				lan.Namespace,
				parentPeer.Namespace,
				"udp4",
				"10.39.9.2:39704",
			)
			testenv.SetRuntimeNamespace(t, gateway)
			link, err := netlink.LinkByName("owned397")
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := netlink.ParseAddr("10.39.7.99/24")
			if err != nil {
				t.Fatal(err)
			}
			if err := netlink.AddrAdd(link, foreign); err != nil {
				t.Fatal(err)
			}
			_, foreignDestination, err := net.ParseCIDR("10.39.10.0/24")
			if err != nil {
				t.Fatal(err)
			}
			unrelatedRoute := &netlink.Route{
				LinkIndex: link.Attrs().Index,
				Dst:       foreignDestination,
				Table:     unix.RT_TABLE_MAIN,
				Scope:     netlink.SCOPE_LINK,
				Protocol:  unix.RTPROT_STATIC,
			}
			if err := netlink.RouteAdd(unrelatedRoute); err != nil {
				t.Fatal(err)
			}
			testenv.WaitStaticRuntimeAddress(t, first, "owned397", "10.39.7.99/24", true)
			if files, err := os.ReadDir(networkdDir); err != nil || len(files) != 0 {
				t.Fatalf("networkd files changed: %v, %v", files, err)
			}
			testenv.KillOwnedRuntimeDaemon(t, first)
			configuredRoute = testenv.WaitConfiguredRuntimeRoute(t, first, "10.39.9.0/24")
			if err := netlink.RouteDel(&configuredRoute); err != nil {
				t.Fatal(err)
			}
			assertConfiguredRuntimePacketAbsent(
				t,
				gateway,
				lan.Namespace,
				parentPeer.Namespace,
				"udp4",
				"10.39.9.2:39706",
			)
			configuredRouteV6 = testenv.WaitConfiguredRuntimeRoute(t, first, "fd39:9::/64")
			if err := netlink.RouteDel(&configuredRouteV6); err != nil {
				t.Fatal(err)
			}
			assertConfiguredRuntimePacketAbsent(
				t,
				gateway,
				lan.Namespace,
				parentPeer.Namespace,
				"udp6",
				"[fd39:9::2]:39806",
			)
			testenv.WriteStaticRuntimeNetwork(
				t,
				networkDir,
				"10.39.7.4",
				"fd39:7::4",
				"10.39.7.3",
				"fd39:7::3",
				400,
				24,
				true,
			)
			second := testenv.StartRuntimeDaemon(t, binary, configPath, root, "static-second")
			defer testenv.KillOwnedRuntimeDaemon(t, second)
			testenv.WaitRuntimeTable(t, second, "inet", "filter", 10*time.Second)
			testenv.WaitStaticRuntimeAddress(t, second, "owned397", "10.39.7.4/24", true)
			testenv.WaitStaticRuntimeAddress(t, second, "owned397", "fd39:7::4/64", true)
			testenv.WaitStaticRuntimeAddress(t, second, "owned397", "10.39.7.1/24", false)
			testenv.WaitStaticRuntimeAddress(t, second, "owned397", "fd39:7::1/64", false)
			testenv.WaitStaticRuntimeAddress(t, second, "owned397", "10.39.7.99/24", true)
			testenv.WaitStaticRuntimeRoute(t, second, "ipv4", "10.39.7.3", 400, true)
			testenv.WaitStaticRuntimeRoute(t, second, "ipv6", "fd39:7::3", 400, true)
			testenv.WaitStaticRuntimeRoute(t, second, "ipv4", "10.39.7.2", 398, false)
			testenv.WaitConfiguredRuntimeRoute(t, second, "10.39.9.0/24")
			routesAfterRestart, err := netlink.RouteList(link, unix.AF_INET)
			if err != nil {
				t.Fatal(err)
			}
			preservedRoute := false
			for _, route := range routesAfterRestart {
				preservedRoute = preservedRoute ||
					route.Dst != nil && route.Dst.String() == "10.39.10.0/24" &&
						route.Protocol == unix.RTPROT_STATIC
			}
			if !preservedRoute {
				t.Fatal("restart removed unrelated configured route")
			}
			testenv.AssertStaticRuntimePacket(
				t,
				lan.Namespace,
				parentPeer.Namespace,
				"udp4",
				"10.39.9.2:39705",
			)
			testenv.SetRuntimeNamespace(t, gateway)
			testenv.WaitConfiguredRuntimeRoute(t, second, "fd39:9::/64")
			waitStaticRuntimeTranslation(t, second, read, "owned397", "ipv6")
			waitStaticRuntimeSelection(t, second, read)
			testenv.AssertStaticRuntimePacket(
				t,
				lan.Namespace,
				parentPeer.Namespace,
				"udp6",
				"[fd39:9::2]:39805",
			)
			testenv.SetRuntimeNamespace(t, gateway)
			latePeer := testenv.NewRuntimePeer(
				t,
				gateway,
				"latephys0",
				"late-peer",
				nil,
				nil,
				testenv.OwnedRuntimeLateMAC,
			)
			defer latePeer.Namespace.Close()
			testenv.SetRuntimeNamespace(t, gateway)
			testenv.WaitStaticRuntimeAddress(t, second, "late397", "10.39.8.1/24", true)
			lateLink, err := netlink.LinkByName("late397")
			if err != nil {
				t.Fatal(err)
			}
			lateParent, err := netlink.LinkByName("latephys0")
			if err != nil || lateLink.Attrs().ParentIndex != lateParent.Attrs().Index {
				t.Fatalf(
					"late static link parent: link=%v parent=%v err=%v",
					lateLink,
					lateParent,
					err,
				)
			}
			testenv.KillOwnedRuntimeDaemon(t, second)
			testenv.WriteStaticRuntimeNetwork(
				t,
				networkDir,
				"10.39.7.4",
				"fd39:7::4",
				"10.39.7.3",
				"fd39:7::3",
				402,
				24,
				true,
			)
			metricOnly := testenv.StartRuntimeDaemon(
				t,
				binary,
				configPath,
				root,
				"static-metric-only",
			)
			defer testenv.KillOwnedRuntimeDaemon(t, metricOnly)
			testenv.WaitStaticRuntimeRoute(t, metricOnly, "ipv4", "10.39.7.3", 402, true)
			testenv.WaitStaticRuntimeRoute(t, metricOnly, "ipv4", "10.39.7.3", 400, false)
			testenv.WaitStaticRuntimeAddress(t, metricOnly, "owned397", "10.39.7.4/24", true)
			testenv.KillOwnedRuntimeDaemon(t, metricOnly)
			testenv.WriteStaticRuntimeNetwork(
				t,
				networkDir,
				"10.39.7.4",
				"fd39:7::4",
				"10.39.7.3",
				"fd39:7::3",
				402,
				25,
				true,
			)
			prefixChange := testenv.StartRuntimeDaemon(
				t,
				binary,
				configPath,
				root,
				"static-prefix-change",
			)
			defer testenv.KillOwnedRuntimeDaemon(t, prefixChange)
			testenv.WaitStaticRuntimeAddress(t, prefixChange, "owned397", "10.39.7.4/25", true)
			testenv.WaitStaticRuntimeAddress(t, prefixChange, "owned397", "10.39.7.4/24", false)
			testenv.WaitStaticRuntimeAddress(t, prefixChange, "owned397", "10.39.7.99/24", true)
			testenv.WaitStaticRuntimeRoute(t, prefixChange, "ipv4", "10.39.7.3", 402, true)
			testenv.KillOwnedRuntimeDaemon(t, prefixChange)
			foreignRoute := &netlink.Route{
				LinkIndex: link.Attrs().Index,
				Table:     unix.RT_TABLE_MAIN,
				Family:    unix.AF_INET,
				Gw:        net.ParseIP("10.39.7.2"),
				Priority:  401,
				Protocol:  unix.RTPROT_STATIC,
			}
			if err := netlink.RouteAdd(foreignRoute); err != nil {
				t.Fatal(err)
			}
			testenv.WriteStaticRuntimeNetwork(
				t,
				networkDir,
				"10.39.7.4",
				"fd39:7::4",
				"10.39.7.3",
				"fd39:7::3",
				401,
				25,
				true,
			)
			conflict := testenv.StartRuntimeDaemon(t, binary, configPath, root, "static-conflict")
			defer testenv.KillOwnedRuntimeDaemon(t, conflict)
			testenv.WaitStaticRuntimeLog(
				t,
				conflict,
				"default route metric 401 conflicts with an unowned route",
			)
			testenv.WaitStaticRuntimeRoute(t, conflict, "ipv4", "10.39.7.3", 402, true)
			testenv.WaitStaticRuntimeRoute(t, conflict, "ipv4", "10.39.7.3", 401, false)
			waitStaticRuntimeFamilyApply(
				t,
				conflict,
				read,
				"owned397",
				"ipv4",
				"failed",
				"default route metric 401 conflicts with an unowned route",
			)
			if routes, err := netlink.RouteListFiltered(
				unix.AF_INET,
				&netlink.Route{Table: unix.RT_TABLE_MAIN},
				netlink.RT_FILTER_TABLE,
			); err != nil {
				t.Fatal(err)
			} else {
				found := false
				for _, route := range routes {
					found = found ||
						route.Priority == 401 && route.Protocol == unix.RTPROT_STATIC &&
							route.Gw.Equal(net.ParseIP("10.39.7.2"))
				}
				if !found {
					t.Fatal("foreign default route was changed")
				}
			}
			testenv.KillOwnedRuntimeDaemon(t, conflict)
			if err := netlink.LinkDel(lateParent); err != nil {
				t.Fatal(err)
			}
			testenv.WriteStaticRuntimeNetwork(
				t,
				networkDir,
				"",
				"fd39:7::4",
				"",
				"fd39:7::3",
				400,
				24,
				false,
			)
			third := testenv.StartRuntimeDaemon(t, binary, configPath, root, "static-third")
			defer testenv.KillOwnedRuntimeDaemon(t, third)
			testenv.WaitRuntimeTable(t, third, "inet", "filter", 10*time.Second)
			waitStaticRuntimeFamilyApply(
				t,
				third,
				read,
				"late397",
				"ipv4",
				"failed",
				"connection late397 link is not ready",
			)
			testenv.WaitStaticRuntimeAddress(t, third, "owned397", "10.39.7.4/25", false)
			testenv.WaitStaticRuntimeAddress(t, third, "owned397", "fd39:7::4/64", true)
			testenv.WaitStaticRuntimeAddress(t, third, "owned397", "10.39.7.99/24", true)
			testenv.WaitStaticRuntimeRoute(t, third, "ipv4", "10.39.7.3", 402, false)
			testenv.WaitStaticRuntimeRoute(t, third, "ipv6", "fd39:7::3", 400, true)
			waitStaticRuntimePromotion(t, third, promotionPath, "0\n")
			latePeer = testenv.NewRuntimePeer(
				t,
				gateway,
				"latephys0",
				"late-peer-new",
				nil,
				nil,
				testenv.OwnedRuntimeLateMAC,
			)
			defer latePeer.Namespace.Close()
			testenv.SetRuntimeNamespace(t, gateway)
			testenv.WaitStaticRuntimeAddress(t, third, "late397", "10.39.8.1/24", true)
			waitStaticRuntimeFamilyApply(t, third, read, "late397", "ipv4", "ready", "")
			for _, name := range []string{"ownphys0", "enmgmt0", "enmwanbr0"} {
				if _, err := netlink.LinkByName(name); err != nil {
					t.Fatalf("static family removal changed %s: %v", name, err)
				}
			}
			testenv.KillOwnedRuntimeDaemon(t, third)
			removeStaticRuntimeConnection(t, networkDir, "owned397")
			if err := unix.Mount(staticStateDir, staticStateDir, "", unix.MS_BIND, ""); err != nil {
				t.Fatal(err)
			}
			mounted := true
			t.Cleanup(func() {
				if mounted {
					_ = unix.Unmount(staticStateDir, 0)
				}
			})
			if err := unix.Mount(
				"",
				staticStateDir,
				"",
				unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY,
				"",
			); err != nil {
				t.Fatal(err)
			}
			failedRemoval := testenv.StartRuntimeDaemon(
				t,
				binary,
				configPath,
				root,
				"static-remove-failed",
			)
			defer testenv.KillOwnedRuntimeDaemon(t, failedRemoval)
			waitStaticPendingRemoval(t, failedRemoval, read, "owned397", "ipv6", true)
			if err := unix.Unmount(staticStateDir, 0); err != nil {
				t.Fatal(err)
			}
			mounted = false
			lateParent, err = netlink.LinkByName("latephys0")
			if err != nil {
				t.Fatal(err)
			}
			if err := netlink.LinkSetDown(lateParent); err != nil {
				t.Fatal(err)
			}
			if err := netlink.LinkSetUp(lateParent); err != nil {
				t.Fatal(err)
			}
			waitStaticPendingRemoval(t, failedRemoval, read, "owned397", "ipv6", false)
			testenv.WaitOwnedRuntimeAbsent(t, failedRemoval, "owned397", 10*time.Second)
		},
	)
}

func removeStaticRuntimeConnection(t *testing.T, directory, name string) {
	t.Helper()
	path := filepath.Join(directory, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	retained := entries[:0]
	for _, entry := range entries {
		var entryName string
		if err := json.Unmarshal(entry["name"], &entryName); err != nil {
			t.Fatal(err)
		}
		if entryName != name {
			retained = append(retained, entry)
		}
	}
	interfaces["interface"], err = json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitStaticRuntimePromotion(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	path, expected string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var value []byte
	var err error
	for time.Now().Before(deadline) {
		value, err = os.ReadFile(path)
		if err == nil && string(value) == expected {
			return
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("IPv4 promotion: value=%q expected=%q err=%v", value, expected, err)
}

func waitStaticRuntimeSelection(t *testing.T, daemon *testenv.RuntimeDaemon, read func() string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var selection []byte
	var err error
	for time.Now().Before(deadline) {
		selection, err = exec.Command(
			"nft",
			"list",
			"chain",
			"inet",
			"mwan_steer",
			"prerouting",
		).CombinedOutput()
		if err == nil {
			for _, line := range strings.Split(string(selection), "\n") {
				if strings.Contains(line, "ip6 saddr 2001:db8:b01:fe::2 ") &&
					strings.Contains(line, "meta mark set 0x00000004") {
					return
				}
			}
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"IPv6 provider selection absent: %s; error=%v; public state=%s",
		selection,
		err,
		read(),
	)
}

func waitStaticRuntimeTranslation(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	read func() string,
	name string,
	family string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var document struct {
			Interfaces struct {
				Entries []map[string]json.RawMessage `json:"interface"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if err := json.Unmarshal([]byte(read()), &document); err == nil {
			for _, entry := range document.Interfaces.Entries {
				var entryName string
				if json.Unmarshal(entry["name"], &entryName) != nil || entryName != name {
					continue
				}
				var state struct {
					Translation struct {
						State struct {
							Ready bool `json:"ready"`
						} `json:"state"`
					} `json:"goodkind-mwan-steering:translation"`
				}
				if json.Unmarshal(entry["ietf-ip:"+family], &state) == nil &&
					state.Translation.State.Ready {
					return
				}
			}
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"%s %s translation readiness absent: %s; daemon: %s",
		name,
		family,
		read(),
		testenv.RuntimeLogTail(t, daemon, 40),
	)
}

func waitStaticRuntimeFamilyApply(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	read func() string,
	name string,
	family string,
	result string,
	reason string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var document struct {
			Interfaces struct {
				Entries []map[string]json.RawMessage `json:"interface"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if err := json.Unmarshal([]byte(read()), &document); err == nil {
			for _, entry := range document.Interfaces.Entries {
				var entryName string
				if json.Unmarshal(entry["name"], &entryName) != nil || entryName != name {
					continue
				}
				var familyState struct {
					Ownership struct {
						LastApply struct {
							Result string `json:"result"`
							Reason string `json:"reason"`
						} `json:"last-apply"`
					} `json:"goodkind-mwan-steering:ownership-family-state"`
				}
				if json.Unmarshal(entry["ietf-ip:"+family], &familyState) == nil &&
					familyState.Ownership.LastApply.Result == result && familyState.Ownership.LastApply.Reason == reason {
					return
				}
			}
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"%s %s apply result=%s reason=%q absent from operational state: %s",
		name,
		family,
		result,
		reason,
		read(),
	)
}

func waitStaticPendingRemoval(
	t *testing.T,
	daemon *testenv.RuntimeDaemon,
	read func() string,
	id string,
	family string,
	present bool,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		value := read()
		var document struct {
			Interfaces struct {
				Group struct {
					State struct {
						Pending []struct {
							ConnectionID string `json:"connection-id"`
							Family       string `json:"family"`
							Result       string `json:"result"`
							Reason       string `json:"reason"`
						} `json:"pending-removal"`
					} `json:"state"`
				} `json:"goodkind-mwan-steering:steering-group"`
			} `json:"ietf-interfaces:interfaces"`
		}
		if value != "" && json.Unmarshal([]byte(value), &document) == nil {
			found := false
			for _, pending := range document.Interfaces.Group.State.Pending {
				if pending.ConnectionID == id && pending.Family == family {
					found = pending.Result == "failed" && pending.Reason != ""
				}
			}
			if found == present {
				return
			}
		}
		testenv.AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"pending removal %s/%s present=%t absent from operational state: %s; daemon: %s",
		id,
		family,
		present,
		read(),
		testenv.RuntimeLogTail(t, daemon, 50),
	)
}

func captureStaticRuntimeFailure(t *testing.T) func() {
	t.Helper()
	protocol := uint16(unix.ETH_P_ALL<<8 | unix.ETH_P_ALL>>8)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(protocol))
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		defer unix.Close(fd)
		if !t.Failed() {
			return
		}
		for range 128 {
			packet := make([]byte, 1600)
			count, source, receiveErr := unix.Recvfrom(fd, packet, unix.MSG_DONTWAIT)
			if receiveErr != nil {
				t.Logf("packet capture ended: %v", receiveErr)
				return
			}
			t.Logf("gateway packet source=%+v frame=%x", source, packet[:count])
		}
	}
}

func assertConfiguredRuntimePacketAbsent(
	t *testing.T,
	gateway netns.NsHandle,
	source netns.NsHandle,
	peer netns.NsHandle,
	network string,
	destination string,
) {
	t.Helper()
	testenv.SetRuntimeNamespace(t, peer)
	listener, err := net.ListenPacket(network, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	testenv.SetRuntimeNamespace(t, source)
	connection, err := net.DialTimeout(network, destination, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("missing-route")); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	_, _, err = listener.ReadFrom(buffer)
	testenv.SetRuntimeNamespace(t, gateway)
	if !os.IsTimeout(err) {
		t.Fatalf("packet without configured route: error=%v", err)
	}
}
