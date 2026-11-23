//go:build linux && protocol

package commandline

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	dhcpAcquireBackoffChildEnv  = "MWAN_DHCP_ACQUIRE_BACKOFF_CHILD"
	dhcpAcquireBackoffBinaryEnv = "MWAN_DHCP_ACQUIRE_BACKOFF_BINARY"
	dhcpLinkWakeTimeout         = 3 * time.Second
)

func TestOwnedDHCPv4LinkFlapDuringAcquireWakesRetry(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
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
	setRuntimeLoopback(t)
	downstream := newRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.namespace.Close()
	provider := newDHCPv4WANProvider(t, gateway, dhcpReconnectServerAddress+"/24")
	defer provider.namespace.Close()
	var discoverCount atomic.Int64
	var firstTransaction atomic.Uint32
	setRuntimeNamespace(t, provider.namespace)
	server, err := server4.NewServer(
		"wan-vlan",
		&net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			discoverCount.Add(1)
			transaction := binary.BigEndian.Uint32(packet.TransactionID[:])
			firstTransaction.CompareAndSwap(0, transaction)
			if transaction == firstTransaction.Load() {
				return
			}
			responseType := dhcpv4.MessageTypeOffer
			if packet.MessageType() == dhcpv4.MessageTypeRequest {
				responseType = dhcpv4.MessageTypeAck
			}
			reply, replyErr := dhcpv4.NewReplyFromRequest(
				packet,
				dhcpv4.WithMessageType(responseType),
				dhcpv4.WithYourIP(net.ParseIP("198.51.100.101")),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP("198.51.100.2"))),
				dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
				dhcpv4.WithOption(dhcpv4.OptRouter(net.ParseIP("198.51.100.2"))),
				dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(10*time.Minute)),
			)
			if replyErr == nil {
				_, _ = conn.WriteTo(reply.ToBytes(), peer)
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	defer func() {
		_ = server.Close()
		if err := <-serveResult; !errors.Is(err, net.ErrClosed) {
			t.Errorf("server4.Server.Serve: %v", err)
		}
	}()
	setRuntimeNamespace(t, gateway)
	configPath := prepareDHCPv4WAN(t, root, true, false)
	daemon := startRuntimeDaemon(
		t,
		os.Getenv(dhcpAcquireBackoffBinaryEnv),
		configPath,
		root,
		"dhcpv4",
	)
	defer func() { stopRuntimeDaemon(t, daemon) }()
	discoverDeadline := time.Now().Add(20 * time.Second)
	for discoverCount.Load() == 0 {
		if !time.Now().Before(discoverDeadline) {
			t.Fatalf("DHCP packet count = 0: %s", runtimeLogTail(t, daemon, 30))
		}
		time.Sleep(10 * time.Millisecond)
	}
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	waitDHCPv4RuntimeState(t, daemon, "dhcp: acquire failed; will retry", 20*time.Second)
	failedAt := time.Now()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.101/24", true)
	if elapsed := time.Since(failedAt); elapsed > dhcpLinkWakeTimeout {
		t.Fatalf("198.51.100.101/24 elapsed = %s, want at most %s", elapsed, dhcpLinkWakeTimeout)
	}
}

func dhcpAcquireBackoffIsolated(t *testing.T) bool {
	t.Helper()
	if os.Getenv(dhcpAcquireBackoffChildEnv) == "1" {
		return true
	}
	daemonBinary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		dhcpAcquireBackoffChildEnv+"=1",
		dhcpAcquireBackoffBinaryEnv+"="+daemonBinary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
	return false
}

func TestOwnedDHCPv4UnrelatedLinkFlapKeepsAcquisitionBackoff(t *testing.T) {
	const initialBackoff = 5 * time.Second
	// Compare wall time because the daemon and receiver use separate processes.
	const timestampTolerance = 250 * time.Millisecond

	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
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
	setRuntimeLoopback(t)
	downstream := newRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.namespace.Close()
	provider := newDHCPv4WANProvider(t, gateway, dhcpReconnectServerAddress+"/24")
	defer provider.namespace.Close()
	unrelatedName := "unrelated0"
	if err := netlink.LinkAdd(&netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{Name: unrelatedName},
	}); err != nil {
		t.Fatal(err)
	}

	configPath := prepareDHCPv4WAN(t, root, true, false)
	binaryPath := os.Getenv(dhcpAcquireBackoffBinaryEnv)
	setup := startRuntimeDaemon(t, binaryPath, configPath, root, "dhcpv4-setup")
	defer stopRuntimeDaemon(t, setup)
	waitDHCPRecoveryMainLoop(t, setup)
	stopRuntimeDaemon(t, setup)
	wan, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if wan.Attrs().OperState != netlink.OperUp {
		t.Fatalf(
			"Expected the existing WAN VLAN to be up. Its operational state is %s.",
			wan.Attrs().OperState,
		)
	}

	var transactionsMu sync.Mutex
	transactions := map[uint32]struct{}{}
	var transactionTimes []time.Time
	snapshotTransactionTimes := func() []time.Time {
		transactionsMu.Lock()
		defer transactionsMu.Unlock()
		return slices.Clone(transactionTimes)
	}
	setRuntimeNamespace(t, provider.namespace)
	server, err := server4.NewServer(
		"wan-vlan",
		&net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(_ net.PacketConn, _ net.Addr, packet *dhcpv4.DHCPv4) {
			transactionsMu.Lock()
			defer transactionsMu.Unlock()
			transaction := binary.BigEndian.Uint32(packet.TransactionID[:])
			if _, exists := transactions[transaction]; exists {
				return
			}
			transactions[transaction] = struct{}{}
			transactionTimes = append(transactionTimes, time.Now())
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	defer func() {
		_ = server.Close()
		if err := <-serveResult; !errors.Is(err, net.ErrClosed) {
			t.Errorf("server4.Server.Serve: %v", err)
		}
	}()
	setRuntimeNamespace(t, gateway)
	daemon := startRuntimeDaemon(t, binaryPath, configPath, root, "dhcpv4")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	failedAt := waitDHCPAcquireFailureTime(t, daemon, initialBackoff)
	initialTransactions := snapshotTransactionTimes()
	initialCount := len(initialTransactions)
	if initialCount != 1 || initialTransactions[0].After(failedAt) {
		t.Fatalf(
			"Expected one DHCP transaction before the failure at %s. Received %v.",
			failedAt,
			initialTransactions,
		)
	}
	earliestRetry := failedAt.Add(initialBackoff - timestampTolerance)
	for _, name := range []string{unrelatedName, "enmwanbr0", unrelatedName, "enmwanbr0"} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetDown(link); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMTU(link, 1400); err != nil {
		t.Fatal(err)
	}
	if !time.Now().Before(earliestRetry) {
		t.Fatal("The link and MTU updates did not finish within the first DHCP backoff.")
	}

	retryDeadline := failedAt.Add(initialBackoff + 5*time.Second)
	for time.Now().Before(retryDeadline) {
		observed := snapshotTransactionTimes()
		if len(observed) > initialCount {
			retryAt := observed[initialCount]
			if retryAt.Before(earliestRetry) {
				t.Fatalf(
					"DHCP retried %s after failure. Expected at least %s. Initial packet at %s, failure at %s, retry at %s.",
					retryAt.Sub(failedAt),
					initialBackoff-timestampTolerance,
					initialTransactions[0],
					failedAt,
					retryAt,
				)
			}
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"The server did not receive the scheduled DHCP retry: %s",
		runtimeLogTail(t, daemon, 30),
	)
}

func waitDHCPAcquireFailureTime(
	t *testing.T,
	daemon *runtimeDaemon,
	backoff time.Duration,
) time.Time {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		for line := range strings.SplitSeq(runtimeDaemonLog(t, daemon), "\n") {
			if !strings.Contains(line, `"msg":"dhcp: acquire failed; will retry"`) {
				continue
			}
			var failure struct {
				Time    time.Time `json:"time"`
				Backoff string    `json:"backoff"`
			}
			if err := json.Unmarshal([]byte(line), &failure); err != nil {
				t.Fatalf("Could not read the DHCP failure timestamp: %v", err)
			}
			if failure.Time.IsZero() || failure.Backoff != backoff.String() {
				t.Fatalf(
					"DHCP failure time = %s, backoff = %q. Expected a timestamp and %s backoff.",
					failure.Time,
					failure.Backoff,
					backoff,
				)
			}
			return failure.Time
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"The daemon did not report a DHCP acquisition failure: %s",
		runtimeLogTail(t, daemon, 30),
	)
	return time.Time{}
}

func TestOwnedDHCPv4LinkUpWakesAcquisitionBackoff(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
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
	setRuntimeLoopback(t)
	downstream := newRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29"},
		[]string{"192.0.2.2/29"},
		"",
	)
	defer downstream.namespace.Close()
	provider := newDHCPv4WANProvider(t, gateway, dhcpReconnectServerAddress+"/24")
	defer provider.namespace.Close()
	setRuntimeNamespace(t, provider.namespace)
	service := startDHCPv4RuntimeKea(t, root, false, 600, "198.51.100.2", "198.51.100.100", true)
	defer func() {
		setRuntimeNamespace(t, provider.namespace)
		stopProtocolServices(t, []protocolService{service})
	}()
	setRuntimeNamespace(t, gateway)
	setRuntimeNamespace(t, provider.namespace)
	link, err := netlink.LinkByName("wan-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	configPath := prepareDHCPv4WAN(t, root, true, false)
	daemon := startRuntimeDaemon(
		t,
		os.Getenv(dhcpAcquireBackoffBinaryEnv),
		configPath,
		root,
		"dhcpv4",
	)
	defer func() { stopRuntimeDaemon(t, daemon) }()
	backoffDeadline := time.Now().Add(40 * time.Second)
	for !strings.Contains(runtimeDaemonLog(t, daemon), "dhcp: acquire failed; will retry") {
		if !time.Now().Before(backoffDeadline) {
			t.Fatalf("acquire failure absent: %s", runtimeLogTail(t, daemon, 30))
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	setRuntimeNamespace(t, provider.namespace)
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	linkUpAt := time.Now()
	waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.100/24", true)
	if elapsed := time.Since(linkUpAt); elapsed > dhcpLinkWakeTimeout {
		t.Fatalf("198.51.100.100/24 elapsed = %s, want at most %s", elapsed, dhcpLinkWakeTimeout)
	}
	t.Logf("198.51.100.100/24 elapsed = %s", time.Since(linkUpAt))
}

func TestOwnedDHCPv4DisabledRuntime(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}

	gateway := dhcpReconnectGateway(t)
	setRuntimeLoopback(t)
	root := t.TempDir()
	configPath := prepareDHCPv4WAN(t, root, false, false)
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", nil, nil, "")
	t.Cleanup(func() { _ = downstream.namespace.Close() })
	provider := newDHCPv4WANProvider(t, gateway, dhcpReconnectServerAddress+"/24")
	t.Cleanup(func() { _ = provider.namespace.Close() })
	replies := &dhcpReconnectRuntime{}
	defer startDHCPRuntimeServer(
		t,
		"wan-vlan",
		provider.namespace,
		gateway,
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			replies.answer(conn, peer, packet, dhcpReconnectLongT1, dhcpReconnectLongT2)
		},
	)()
	daemon := startRuntimeDaemon(
		t,
		os.Getenv(dhcpAcquireBackoffBinaryEnv),
		configPath,
		root,
		"dhcpv4",
	)
	defer stopRuntimeDaemon(t, daemon)
	waitDHCPRecoveryMainLoop(t, daemon)
	if _, err := netlink.LinkByName("enatt0"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)
	assertRuntimeDaemonRunning(t, daemon)
	if count := len(replies.snapshot()); count != 0 {
		t.Fatalf("DHCP sent %d packets for the disabled connection", count)
	}

	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpReconnectOriginalAddress+"/24", false)
	waitStaticRuntimeRoute(t, daemon, "ipv4", dhcpReconnectServerAddress, 20, false)
}
