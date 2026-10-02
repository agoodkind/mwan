//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

	"goodkind.io/mwan/internal/observation"
)

const distributionRuntimeChild = "MWAN_DISTRIBUTION_RUNTIME_CHILD"

func TestDistributionObservationDaemonRuntime(t *testing.T) {
	if os.Getenv(distributionRuntimeChild) == "1" {
		runDistributionObservationRuntime(t)
		return
	}
	binary := protocolTestBinary(t)
	command := exec.Command(os.Args[0], "-test.run=^TestDistributionObservationDaemonRuntime$")
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	command.Env = append(os.Environ(), distributionRuntimeChild+"=1", mappedRuntimeBinaryEnv+"="+binary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real downstream distribution: %v: %s", err, output)
	}
}

func runDistributionObservationRuntime(t *testing.T) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	host, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	gateway, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	defer setRuntimeNamespace(t, host)
	setRuntimeLoopback(t)
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "enmgmt0"}}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "enmgmt0", []string{"203.0.113.1/24"})
	setRuntimeNamespace(t, host)
	setRuntimeLoopback(t)
	transit := distributionGatewayBridge(t, host, gateway, "transit", "lantap", "enmwanbr0", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64", "2001:db8:b01:1::3/64"})
	configureRuntimeLink(t, "transit", []string{"192.0.2.4/29", "192.0.2.5/29", "2001:db8:b01:1::4/64", "2001:db8:b01:1::5/64"})
	addRuntimeDefault(t, "transit", "192.0.2.1")
	addRuntimeDefault(t, "transit", "2001:db8:b01:1::3")
	var providers []observation.ProviderIngress
	var upstreams []netns.NsHandle
	for index, name := range []string{"enwebpass0", "enatt0"} {
		bridge := fmt.Sprintf("provider%d", index)
		distributionGatewayBridge(t, host, gateway, bridge, fmt.Sprintf("wantap%d", index), name, []string{fmt.Sprintf("10.50.%d.1/24", index+1), fmt.Sprintf("fd50:%d::1/64", index+1)})
		port := fmt.Sprintf("isptap%d", index)
		peer := newRuntimePeer(t, host, port, "isp", nil, []string{fmt.Sprintf("10.50.%d.2/24", index+1), fmt.Sprintf("fd50:%d::2/64", index+1), "10.50.9.2/32", "fd50:9::2/128"}, "")
		defer peer.namespace.Close()
		upstreams = append(upstreams, peer.namespace)
		setRuntimeNamespace(t, peer.namespace)
		link, err := netlink.LinkByName("isp")
		if err != nil {
			t.Fatal(err)
		}
		mac := link.Attrs().HardwareAddr
		addMappedRuntimeRoute(t, "192.0.2.0/29", fmt.Sprintf("10.50.%d.1", index+1), "isp")
		addMappedRuntimeRoute(t, "2001:db8:b01::/60", fmt.Sprintf("fd50:%d::1", index+1), "isp")
		for _, target := range []struct{ network, address string }{{"tcp4", "10.50.9.2:45001"}, {"tcp6", "[fd50:9::2]:45002"}} {
			listener, err := net.Listen(target.network, target.address)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("provider-application-ok"))
			}))
			server.Listener = listener
			server.Start()
			defer server.Close()
		}
		setRuntimeNamespace(t, host)
		distributionBridgePort(t, bridge, port, mac)
		providers = append(providers, observation.ProviderIngress{ConnectionID: name, Bridge: bridge, PortInterface: port, DestinationMAC: mac.String(), Tier: 1, Weight: 1, Eligible: true})
		setRuntimeNamespace(t, gateway)
		device, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, nextHop := range []string{fmt.Sprintf("10.50.%d.2", index+1), fmt.Sprintf("fd50:%d::2", index+1)} {
			if err := netlink.RouteAdd(&netlink.Route{LinkIndex: device.Attrs().Index, Gw: net.ParseIP(nextHop), Priority: 100 + index, Table: unix.RT_TABLE_MAIN}); err != nil {
				t.Fatal(err)
			}
		}
		setRuntimeNamespace(t, host)
	}
	setRuntimeNamespace(t, gateway)
	internal, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		t.Fatal(err)
	}
	_, downstream, err := net.ParseCIDR("2001:db8:b01:1::/64")
	if err != nil {
		t.Fatal(err)
	}
	// External ownership supplies guest return routes independently of WAN defaults.
	for _, table := range []int{100, 200} {
		if err := netlink.RouteAdd(&netlink.Route{LinkIndex: internal.Attrs().Index, Dst: downstream, Table: table, Scope: netlink.SCOPE_LINK}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/proc/sys/net/ipv4/ip_forward", "/proc/sys/net/ipv6/conf/all/forwarding"} {
		if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	network := filepath.Join(root, "network")
	networkd := filepath.Join(root, "networkd")
	for _, directory := range []string{network, networkd} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, network, "/etc/mwan")
	bindStartupDirectory(t, networkd, "/etc/systemd/network")
	bindStartupDirectory(t, schema, "/usr/local/share/wanconfig/yang")
	writeDistributionRuntimeNetwork(t, network)
	configuration := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configuration, []byte("[ifmgr]\nrole = \"wan\"\nreconcile_interval = \"1h\"\n[ifmgr.iface.enmwanbr0]\n[wanconfig]\npublish = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv(mappedRuntimeBinaryEnv)
	daemon := startRuntimeDaemon(t, binary, configuration, root, "distribution")
	defer killOwnedRuntimeDaemon(t, daemon)
	defer func() {
		if !t.Failed() {
			return
		}
		setRuntimeNamespace(t, gateway)
		output, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
		t.Logf("gateway rules: %v: %s", err, output)
		logDistributionRuntimeNetwork(t, "gateway")
		t.Logf("gateway daemon: %s", runtimeLogTail(t, daemon, 50))
		setRuntimeNamespace(t, host)
		logDistributionRuntimeNetwork(t, "downstream")
		for index, upstream := range upstreams {
			setRuntimeNamespace(t, upstream)
			logDistributionRuntimeNetwork(t, fmt.Sprintf("upstream%d", index))
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		output, err := exec.Command("nft", "list", "chain", "inet", "mwan_steer", "prerouting").CombinedOutput()
		if err == nil && strings.Contains(string(output), "numgen random mod 2") && strings.Contains(string(output), "ip saddr") && strings.Contains(string(output), "ip6 saddr") {
			break
		}
		assertRuntimeDaemonRunning(t, daemon)
		if time.Now().After(deadline) {
			t.Fatalf("random steering unavailable: %v: %s; daemon: %s", err, output, runtimeLogTail(t, daemon, 50))
		}
		time.Sleep(20 * time.Millisecond)
	}
	setRuntimeNamespace(t, host)
	observer := observation.Endpoint{Kind: observation.EndpointLocal, MachineID: "distribution-runtime-host"}
	for _, family := range []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6} {
		now := time.Now().UTC()
		nextHop, target, sources := "192.0.2.1", "http://10.50.9.2:45001", []string{"192.0.2.4", "192.0.2.5"}
		if family == observation.FamilyIPv6 {
			nextHop, target, sources = "2001:db8:b01:1::3", "http://[fd50:9::2]:45002", []string{"2001:db8:b01:1::4", "2001:db8:b01:1::5"}
		}
		var requests []observation.CheckSpec
		for index, source := range sources {
			requests = append(requests, observation.CheckSpec{ID: fmt.Sprintf("guest%d-%s", index, family), Dimension: observation.DimensionDownstreamApplication, Operation: observation.OperationHTTP, Observer: observer, Router: observation.RouterPrimary, Source: netip.MustParseAddr(source), ExpectedNextHop: netip.MustParseAddr(nextHop), Family: family, Target: target, TimeoutSeconds: 1, MaxAgeSeconds: 30, ExpectedHTTPStatus: []int{200}, ExpectedBody: "provider-application-ok"})
		}
		for index := range providers {
			providers[index].Family, providers[index].ObservedAt = family, now
		}
		transit.Family, transit.ObservedAt = family, now
		calibration := &observation.DistributionCalibration{HashMode: "random", ActiveTier: 1, Samples: 40, Providers: []observation.CalibratedProvider{{ConnectionID: "enwebpass0", Tier: 1, Weight: 1, MinSamples: 13, MaxSamples: 27}, {ConnectionID: "enatt0", Tier: 1, Weight: 1, MinSamples: 13, MaxSamples: 27}}}
		plan := &observation.DistributionPlan{HashMode: "random", ActiveTier: 1, ObservedAt: now, Providers: providers, Transit: []observation.ProviderIngress{transit}, Calibration: calibration, Requests: requests}
		spec := observation.CheckSpec{ID: "real-distribution", Dimension: observation.DimensionConnectionDistribution, Operation: observation.OperationDistribution, Observer: observer, Family: family, Target: target, TimeoutSeconds: 4, MaxAgeSeconds: 30, DistributionSamples: 40, DistributionPlan: plan}
		paths := []observation.PathIdentity{{Interface: "transit", NextHop: netip.MustParseAddr(nextHop), Router: observation.RouterPrimary, ObservedAt: now}}
		result := runDistributionRuntimeProcess(t, binary, spec, paths)
		if result.Availability != observation.AvailabilityComplete {
			t.Fatalf("real steering measurement missing: %+v", result)
		}
		t.Logf("actual %s provider samples: %+v", family, result.DistributionProviders)
		withinBounds := true
		for _, share := range result.DistributionProviders {
			if share.Samples == 0 {
				t.Fatalf("actual steering omitted a provider: %+v", result)
			}
			withinBounds = withinBounds && share.Samples >= 13 && share.Samples <= 27
		}
		if observation.RequiredPassed(spec, result, time.Now()) != withinBounds || (result.Outcome == observation.OutcomePass) != withinBounds {
			t.Fatalf("calibration verdict disagrees with actual counts: %+v", result)
		}
		calibration.Providers[0].MinSamples, calibration.Providers[0].MaxSamples = 0, 0
		calibration.Providers[1].MinSamples, calibration.Providers[1].MaxSamples = 40, 40
		outside := runDistributionRuntimeProcess(t, binary, spec, paths)
		if outside.Availability != observation.AvailabilityComplete || outside.DistributionProviders[0].Samples == 0 || outside.Outcome != observation.OutcomeFail || observation.RequiredPassed(spec, outside, time.Now()) {
			t.Fatalf("observed provider counts outside explicit bounds did not fail: %+v", outside)
		}
		for index := range calibration.Providers {
			calibration.Providers[index].MinSamples, calibration.Providers[index].MaxSamples = 13, 27
		}
		plan.HashMode, calibration.HashMode = "source", "source"
		plan.Requests = requests[:1]
		missingDiversity := runDistributionRuntimeProcess(t, binary, spec, paths)
		if missingDiversity.Availability != observation.AvailabilityMissing || missingDiversity.Outcome != observation.OutcomeUnknown {
			t.Fatalf("one source produced a false weighted balancing verdict: %+v", missingDiversity)
		}
		plan.HashMode, calibration.HashMode = "random", "random"
		plan.Requests = requests
		plan.Providers[0].Weight = 2
		missing := runDistributionRuntimeProcess(t, binary, spec, paths)
		if missing.Availability != observation.AvailabilityMissing || missing.Outcome != observation.OutcomeUnknown {
			t.Fatalf("changed weight reused calibration: %+v", missing)
		}
		plan.Providers[0].Weight = 1
		plan.Calibration = nil
		missing = runDistributionRuntimeProcess(t, binary, spec, paths)
		if missing.Availability != observation.AvailabilityMissing || missing.Outcome != observation.OutcomeUnknown {
			t.Fatalf("missing calibration reported health: %+v", missing)
		}
	}
}

func logDistributionRuntimeNetwork(t *testing.T, scope string) {
	t.Helper()
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		routes, routeError := netlink.RouteListFiltered(family, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
		rules, ruleError := netlink.RuleList(family)
		neighbors, neighborError := netlink.NeighList(0, family)
		t.Logf("%s family %d routes=%+v error=%v rules=%+v error=%v neighbors=%+v error=%v", scope, family, routes, routeError, rules, ruleError, neighbors, neighborError)
	}
	links, err := netlink.LinkList()
	t.Logf("%s links: %v", scope, err)
	for _, link := range links {
		addresses, err := netlink.AddrList(link, unix.AF_UNSPEC)
		t.Logf("%s link %s index %d master %d flags %s addresses=%+v error=%v", scope, link.Attrs().Name, link.Attrs().Index, link.Attrs().MasterIndex, link.Attrs().Flags, addresses, err)
		for _, setting := range []string{"rp_filter", "forwarding", "arp_ignore", "arp_filter"} {
			value, err := os.ReadFile(filepath.Join("/proc/sys/net/ipv4/conf", link.Attrs().Name, setting))
			t.Logf("%s %s %s: %s error=%v", scope, link.Attrs().Name, setting, bytes.TrimSpace(value), err)
		}
	}
}

func distributionGatewayBridge(t *testing.T, host, gateway netns.NsHandle, bridgeName, portName, gatewayName string, addresses []string) observation.ProviderIngress {
	t.Helper()
	setRuntimeNamespace(t, host)
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: bridgeName}}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, bridgeName, nil)
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: portName}, PeerName: gatewayName, PeerNamespace: netlink.NsFd(gateway)}); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, portName, nil)
	setRuntimeNamespace(t, gateway)
	configureRuntimeLink(t, gatewayName, addresses)
	link, err := netlink.LinkByName(gatewayName)
	if err != nil {
		t.Fatal(err)
	}
	mac := link.Attrs().HardwareAddr
	setRuntimeNamespace(t, host)
	distributionBridgePort(t, bridgeName, portName, mac)
	return observation.ProviderIngress{ConnectionID: gatewayName, Bridge: bridgeName, PortInterface: portName, DestinationMAC: mac.String()}
}

func distributionBridgePort(t *testing.T, bridgeName, portName string, mac net.HardwareAddr) {
	t.Helper()
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		t.Fatal(err)
	}
	port, err := netlink.LinkByName(portName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMaster(port, bridge); err != nil {
		t.Fatal(err)
	}
	// NUD_PERMANENT makes the MAC local to the bridge and consumes peer replies.
	if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: port.Attrs().Index, Family: unix.AF_BRIDGE, State: unix.NUD_NOARP, Flags: unix.NTF_MASTER, HardwareAddr: mac}); err != nil {
		t.Fatal(err)
	}
}

func writeDistributionRuntimeNetwork(t *testing.T, directory string) {
	t.Helper()
	writeSelectionRuntimeNetwork(t, directory, true)
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
	if err := json.Unmarshal(interfaces["goodkind-mwan-steering:steering-group"], &group); err != nil {
		t.Fatal(err)
	}
	group["hash-mode"] = json.RawMessage(`"random"`)
	interfaces["goodkind-mwan-steering:steering-group"], err = json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if string(entry["name"]) == `"enwebpass0"` || string(entry["name"]) == `"enatt0"` {
			entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"tier":1,"weight":1}`)
		}
	}
	interfaces["interface"], err = json.Marshal(entries)
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

func runDistributionRuntimeProcess(t *testing.T, binary string, spec observation.CheckSpec, paths []observation.PathIdentity) observation.Result {
	t.Helper()
	request, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	directory := t.TempDir()
	machineID := filepath.Join(directory, "machine-id")
	if err := os.WriteFile(machineID, []byte(spec.Observer.MachineID), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(observation.RuntimeConfig{MachineIDPath: machineID})
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(directory, "runtime.json")
	if err := os.WriteFile(runtimePath, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "observe", "--check", string(request), "--paths", string(identities), "--runtime-settings", runtimePath)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("downstream observation command: %v: %s", err, stderr.String())
	}
	var result observation.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Availability != observation.AvailabilityComplete {
		t.Logf("observation stderr: %s", stderr.String())
	}
	return result
}
