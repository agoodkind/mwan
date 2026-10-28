//go:build linux && vpp_integration

package vpp_test

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"text/template"
	"time"

	"github.com/agoodkind/netlink"
	"go.fd.io/govpp"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/ip_types"
	"go.fd.io/govpp/binapi/nat64"
	"go.fd.io/govpp/binapi/nat_types"
	"go.fd.io/govpp/binapi/tapv2"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/reporting"
	vppclient "goodkind.io/mwan/internal/vpp"
)

//go:embed testdata/startup.conf
var integrationFiles embed.FS

func TestControllerRealVPP(t *testing.T) {
	if !slices.Contains(os.Args, "mwan-vpp-integration-child") {
		for _, idleWaitUsec := range []uint32{0, 10} {
			t.Run(fmt.Sprintf("idle wait %d microseconds", idleWaitUsec), func(t *testing.T) {
				runVPPIntegrationChild(t, idleWaitUsec)
			})
		}
		return
	}
	var idleWaitUsec uint32
	switch os.Args[len(os.Args)-1] {
	case "0":
		idleWaitUsec = 0
	case "10":
		idleWaitUsec = 10
	default:
		t.Fatal("the VPP integration child requires an idle wait of 0 or 10 microseconds")
	}
	executable, err := exec.LookPath("vpp")
	if err != nil {
		t.Fatalf("install VPP 26.06 and its TAP and NAT64 plugins: %v", err)
	}
	directory, err := os.MkdirTemp("", "mwan-vpp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	settings := vppclient.Endpoints{APISocket: filepath.Join(directory, "api.sock"), StatsSocket: filepath.Join(directory, "stats.sock"), ExpectedVersion: vppclient.BindingVersion, Timeout: 10 * time.Second}
	startup, err := template.ParseFS(integrationFiles, "testdata/startup.conf")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "startup.conf")
	configuration, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := startup.Execute(configuration, struct {
		Directory, APISocket, StatsSocket, Prefix string
		IdleWaitUsec                              uint32
	}{directory, settings.APISocket, settings.StatsSocket, filepath.Base(directory), idleWaitUsec}); err != nil {
		_ = configuration.Close()
		t.Fatal(err)
	}
	if err := configuration.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(directory, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	process := exec.Command(executable, "-c", path)
	process.Stdout, process.Stderr = output, output
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan struct{})
	var processError error
	go func() {
		processError = process.Wait()
		close(processDone)
	}()
	t.Cleanup(func() {
		_ = process.Process.Signal(syscall.SIGTERM)
		select {
		case <-processDone:
		case <-time.After(5 * time.Second):
			_ = process.Process.Kill()
			<-processDone
		}
		if t.Failed() {
			data, readError := os.ReadFile(output.Name())
			t.Logf("VPP process output: %s (%v)", data, readError)
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case <-processDone:
			data, readError := os.ReadFile(output.Name())
			t.Fatalf("VPP exited during startup: %v\n%s\nread process output: %v", processError, data, readError)
		default:
		}
		_, apiError := os.Stat(settings.APISocket)
		_, statsError := os.Stat(settings.StatsSocket)
		if apiError == nil && statsError == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("VPP did not create its configured sockets: API %v, statistics %v", apiError, statsError)
		}
		time.Sleep(20 * time.Millisecond)
	}

	inside := interfaceintent.Connection{ID: "vpp-test-inside", Name: "tap901", Owner: interfaceintent.OwnerExternal, IPv6: &interfaceintent.IPv6{Family: interfaceintent.Family{Addresses: []interfaceintent.Address{{Prefix: netip.MustParsePrefix("fd06:fab:9901::2/64")}}}}}
	outside := interfaceintent.Connection{ID: "vpp-test-outside", Name: "tap902", Owner: interfaceintent.OwnerExternal, IPv4: &interfaceintent.IPv4{Family: interfaceintent.Family{Addresses: []interfaceintent.Address{{Prefix: netip.MustParsePrefix("192.0.2.2/30")}}, Routes: []interfaceintent.RouteIntent{{Destination: netip.MustParsePrefix("0.0.0.0/0"), Gateway: netip.MustParseAddr("192.0.2.1"), TableID: 0}}}}}
	hostInside := interfaceintent.Connection{ID: "host-inside", Name: "gvpp-in", Owner: interfaceintent.OwnerExternal}
	hostOutside := interfaceintent.Connection{ID: "host-outside", Name: "gvpp-out", Owner: interfaceintent.OwnerExternal}
	policy := vppclient.Policy{Connections: []interfaceintent.Connection{inside, outside, hostInside, hostOutside}, NAT64: &vppclient.NAT64Policy{Inside: inside.Name, Outside: outside.Name, Prefix: netip.MustParsePrefix("64:ff9b::/96"), Pool: []netip.Addr{netip.MustParseAddr("198.51.100.10")}, VRF: 0}}
	insideHost, err := ip_types.ParseIP6Prefix("fd06:fab:9901::1/64")
	if err != nil {
		t.Fatal(err)
	}
	outsideHost, err := ip_types.ParseIP4Prefix("192.0.2.1/30")
	if err != nil {
		t.Fatal(err)
	}
	resources := vppclient.Settings{Endpoints: settings, StateFile: filepath.Join(directory, "owned.json"), NAT64: &nat64.Nat64PluginEnableDisable{BibBuckets: 128, BibMemorySize: 8 << 20, StBuckets: 128, StMemorySize: 8 << 20}, TAPs: []vppclient.TAP{
		{ConnectionID: inside.ID.String(), Create: tapv2.TapCreateV3{ID: 901, UseRandomMac: true, NumRxQueues: 1, NumTxQueues: 1, RxRingSz: 256, TxRingSz: 256, HostIfNameSet: true, HostIfName: "gvpp-in", HostIP6PrefixSet: true, HostIP6Prefix: ip_types.IP6AddressWithPrefix(insideHost)}, RXMode: interface_types.RX_MODE_API_ADAPTIVE},
		{ConnectionID: outside.ID.String(), Create: tapv2.TapCreateV3{ID: 902, UseRandomMac: true, NumRxQueues: 1, NumTxQueues: 1, RxRingSz: 256, TxRingSz: 256, HostIfNameSet: true, HostIfName: "gvpp-out", HostIP4PrefixSet: true, HostIP4Prefix: ip_types.IP4AddressWithPrefix(outsideHost)}, RXMode: interface_types.RX_MODE_API_ADAPTIVE},
	}}
	controller, err := vppclient.NewController(resources)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	foreignHost, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(foreignHost)
	request, err := unix.NewIfreq("gvpp-reserved")
	if err != nil {
		t.Fatal(err)
	}
	request.SetUint16(unix.IFF_TAP | unix.IFF_MULTI_QUEUE)
	if err := unix.IoctlIfreq(foreignHost, unix.TUNSETIFF, request); err != nil {
		t.Fatal(err)
	}
	conflictSettings := vppclient.Settings{Endpoints: settings, StateFile: filepath.Join(directory, "foreign-host.json"), TAPs: []vppclient.TAP{{ConnectionID: "conflict-vpp", Create: tapv2.TapCreateV3{ID: 900, UseRandomMac: true, NumRxQueues: 1, NumTxQueues: 1, RxRingSz: 256, TxRingSz: 256, HostIfNameSet: true, HostIfName: "gvpp-reserved"}, RXMode: interface_types.RX_MODE_API_ADAPTIVE}}}
	conflictController, err := vppclient.NewController(conflictSettings)
	if err != nil {
		t.Fatal(err)
	}
	conflictPolicy := vppclient.Policy{Connections: []interfaceintent.Connection{{ID: "conflict-vpp", Name: "tap900", Owner: interfaceintent.OwnerExternal}, {ID: "conflict-host", Name: "gvpp-reserved", Owner: interfaceintent.OwnerExternal}}}
	if err := conflictController.Reconcile(t.Context(), logger, conflictPolicy); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("VPP must reject a foreign Linux TAP before changing it: %v", err)
	}
	verifyNAT64Recovery(t, logger, resources, policy)
	if err := controller.Reconcile(t.Context(), logger, policy); err != nil {
		t.Fatal(err)
	}
	verifyEnableJournal(t, resources.StateFile, true, false, false)
	if err := controller.Reconcile(t.Context(), logger, policy); err != nil {
		t.Fatalf("repeat VPP reconciliation: %v", err)
	}
	for _, route := range []struct{ destination, gateway, link string }{{"64:ff9b::/96", "fd06:fab:9901::2", "gvpp-in"}, {"198.51.100.10/32", "192.0.2.2", "gvpp-out"}} {
		link, err := netlink.LinkByName(route.link)
		if err != nil {
			t.Fatal(err)
		}
		_, destination, err := net.ParseCIDR(route.destination)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.RouteAdd(&netlink.Route{Dst: destination, Gw: net.ParseIP(route.gateway), LinkIndex: link.Attrs().Index}); err != nil {
			t.Fatal(err)
		}
	}
	waitHostIPv6(t, "gvpp-in", netip.MustParseAddr("fd06:fab:9901::1"))
	listener, err := net.Listen("tcp4", "192.0.2.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		_, err = io.Copy(connection, connection)
		serverResult <- err
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("fd06:fab:9901::1")}, Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(t.Context(), "tcp6", fmt.Sprintf("[64:ff9b::c000:201]:%d", port))
	if err != nil {
		t.Fatalf("complete IPv6 to IPv4 TCP connection through API configured NAT64: %v", err)
	}
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := "GoVPP configured the real NAT64 path."
	if _, err := io.WriteString(connection, payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, received); err != nil || string(received) != payload {
		t.Fatalf("NAT64 echo: %q, %v", received, err)
	}
	_ = connection.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	stats, err := vppclient.ReadInterfaceCounters(t.Context(), settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{inside.Name, outside.Name} {
		counter, present := stats[name]
		if !present || !counter.RXAvailable || !counter.TXAvailable || counter.Counters.Rx.Bytes == 0 || counter.Counters.Tx.Bytes == 0 {
			t.Fatalf("VPP shared memory did not report real TCP traffic for %s: %+v", name, counter)
		}
	}
	reportSettings := config.DefaultReportSection()
	reportSettings.StateDirectory = directory
	reportSettings.RRDToolPath = filepath.Join(directory, "rrdtool")
	reportSettings.SampleInterval = "1s"
	reportSettings.MinProbeSeconds = 1
	collector, err := reporting.NewCollector(reportSettings, &networkjson.Config{VPP: &policy}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	reportVPP := &config.VPPSection{APISocket: settings.APISocket, StatsSocket: settings.StatsSocket, ExpectedVersion: settings.ExpectedVersion, RequestTimeout: settings.Timeout.String(), StateFile: resources.StateFile}
	for _, resource := range resources.TAPs {
		reportVPP.Interface = append(reportVPP.Interface, config.VPPTAPSection{ConnectionID: resource.ConnectionID, HostConnectionID: "host-" + resource.ConnectionID, ID: resource.Create.ID, RXRingSize: resource.Create.RxRingSz, TXRingSize: resource.Create.TxRingSz, RXQueues: resource.Create.NumRxQueues, TXQueues: resource.Create.NumTxQueues, RXMode: "adaptive"})
	}
	if err := collector.ConfigureVPP(reportVPP); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	measurement, err := collector.Sample(t.Context(), started, started.Add(time.Second))
	if err != nil || len(measurement.VPPTraffic) != 2 {
		t.Fatalf("collect real VPP reporting interval: %v, %+v", err, measurement.VPPTraffic)
	}
	for _, traffic := range measurement.VPPTraffic {
		if !traffic.Available {
			t.Fatalf("readable VPP interface counters must preserve availability: %+v", traffic)
		}
	}
	t.Run("native VPP profile", func(t *testing.T) {
		profile, err := vppclient.Profile(t.Context(), settings, executable, 250*time.Millisecond)
		if err != nil && (errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS)) {
			t.Skipf("the host kernel does not permit native VPP profiling: %v", err)
		}
		if err != nil || !profile.Available || len(profile.Threads) == 0 || profile.Counts.Total == 0 {
			t.Fatalf("profile VPP Linux threads selected by the binary API: %v, %+v", err, profile)
		}
		if profile.EmptyPollingAvailable {
			t.Fatal("native instruction samples must not claim exact empty polling attribution")
		}
	})
	t.Run("optional receive queue accounting", func(t *testing.T) {
		verifyQueuePolling(t, settings, stats)
	})
	t.Run("optional queue profiling CPU cost", func(t *testing.T) {
		verifyQueueProfileCost(t, settings, executable, idleWaitUsec)
	})
	foreign, err := govpp.Connect(settings.APISocket)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Disconnect()
	oldIndex := interface_types.InterfaceIndex(stats[inside.Name].Counters.InterfaceIndex)
	if _, err := tapv2.NewServiceClient(foreign).TapDeleteV2(t.Context(), &tapv2.TapDeleteV2{SwIfIndex: oldIndex}); err != nil {
		t.Fatal(err)
	}
	foreignTAP, err := tapv2.NewServiceClient(foreign).TapCreateV3(t.Context(), &tapv2.TapCreateV3{ID: 903, UseRandomMac: true, NumRxQueues: 1, NumTxQueues: 1, RxRingSz: 256, TxRingSz: 256, HostIfNameSet: true, HostIfName: "gvpp-foreign", Tag: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	if foreignTAP.SwIfIndex != oldIndex {
		t.Fatalf("VPP did not reuse the removed TAP index: got %d, expected %d", foreignTAP.SwIfIndex, oldIndex)
	}
	if _, err := nat64.NewServiceClient(foreign).Nat64AddDelInterface(t.Context(), &nat64.Nat64AddDelInterface{SwIfIndex: oldIndex, Flags: nat_types.NAT_IS_INSIDE, IsAdd: true}); err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(t.Context(), logger, policy); err != nil {
		t.Fatalf("repair an owned TAP after another interface reused its index: %v", err)
	}
	resources.TAPs = nil
	controller, err = vppclient.NewController(resources)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(t.Context(), logger, vppclient.Policy{}); err != nil {
		t.Fatalf("remove owned VPP configuration: %v", err)
	}
	client, err := vppclient.Open(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	remaining, err := client.Interfaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if remaining[inside.Name] != nil || remaining[outside.Name] != nil || remaining["tap903"] == nil {
		t.Fatalf("cleanup did not preserve only the foreign TAP: %v", remaining)
	}
	stream, err := nat64.NewServiceClient(foreign).Nat64InterfaceDump(t.Context(), &nat64.Nat64InterfaceDump{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	foreignPreserved := false
	for {
		entry, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if entry.SwIfIndex == oldIndex && entry.Flags&nat_types.NAT_IS_INSIDE != 0 {
			foreignPreserved = true
		}
	}
	if !foreignPreserved {
		t.Fatal("immediate cleanup deleted the NAT64 feature at an unrelated interface's reused index")
	}
	verifyForeignNAT64Resources(t, foreign)
	started = time.Now()
	measurement, err = collector.Sample(t.Context(), started, started.Add(time.Second))
	if err != nil || len(measurement.VPPTraffic) != 2 {
		t.Fatalf("collect unavailable VPP reporting interval: %v, %+v", err, measurement.VPPTraffic)
	}
	for _, traffic := range measurement.VPPTraffic {
		if traffic.Available {
			t.Fatalf("removed VPP interface must not produce an available zero measurement: %+v", traffic)
		}
	}
}

func runVPPIntegrationChild(t *testing.T, idleWaitUsec uint32) {
	t.Helper()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestControllerRealVPP$", "-test.v", "--", "mwan-vpp-integration-child", fmt.Sprint(idleWaitUsec))
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run VPP integration with %d microsecond idle waiting in a private network namespace: %v\n%s", idleWaitUsec, err, output)
	}
	t.Log(string(output))
}

func waitHostIPv6(t *testing.T, name string, address netip.Addr) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range addresses {
			if candidate.IP.Equal(net.IP(address.AsSlice())) && candidate.Flags&unix.IFA_F_TENTATIVE == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("host IPv6 address %s on %s did not complete duplicate address detection", address, name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
