//go:build linux && firewallnetns

package main

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	dhcpReconnectOriginalAddress = "198.51.100.101"
	dhcpReconnectChangedAddress  = "198.51.100.102"
	dhcpReconnectServerAddress   = "198.51.100.2"
	dhcpReconnectVerifyWait      = 25 * time.Second
	dhcpReconnectDiscoverWait    = 90 * time.Second
	dhcpReconnectInFlightT1      = 20 * time.Second
	dhcpReconnectShortT1         = 120 * time.Second
	dhcpReconnectLongT1          = 1800 * time.Second
	dhcpReconnectLongT2          = 3000 * time.Second
	dhcpReconnectNearT1          = 15 * time.Second
	dhcpReconnectNearT2          = 35 * time.Second
	dhcpReconnectFarT2           = 100 * time.Second
	dhcpReconnectBeforeT1        = 10 * time.Second
	dhcpReconnectT1Tolerance     = 5 * time.Second
)

type dhcpReconnectBehavior int32

const (
	dhcpReconnectAnswerOriginal dhcpReconnectBehavior = iota
	dhcpReconnectNAKOriginal
	dhcpReconnectIgnoreOriginal
	dhcpReconnectSilent
)

type dhcpReconnectPacket struct {
	Type      dhcpv4.MessageType
	Client    net.IP
	Requested net.IP
	Server    net.IP
	At        time.Time
}

type dhcpReconnectCapturedRequest struct {
	At          time.Time
	Destination net.IP
}

type dhcpReconnectRuntime struct {
	behavior atomic.Int32
	mu       sync.Mutex
	packets  []dhcpReconnectPacket
	requests []dhcpReconnectCapturedRequest
	daemon   *runtimeDaemon
	link     netlink.Link
}

func (r *dhcpReconnectRuntime) snapshot() []dhcpReconnectPacket {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]dhcpReconnectPacket(nil), r.packets...)
}

func (r *dhcpReconnectRuntime) answer(
	conn net.PacketConn,
	peer net.Addr,
	packet *dhcpv4.DHCPv4,
	t1 time.Duration,
	t2 time.Duration,
) {
	requested := packet.RequestedIPAddress()
	r.mu.Lock()
	r.packets = append(
		r.packets,
		dhcpReconnectPacket{
			Type:      packet.MessageType(),
			Client:    packet.ClientIPAddr,
			Requested: requested,
			Server:    packet.ServerIdentifier(),
			At:        time.Now(),
		},
	)
	r.mu.Unlock()
	if requested == nil {
		requested = packet.ClientIPAddr
	}
	behavior := dhcpReconnectBehavior(r.behavior.Load())
	if behavior == dhcpReconnectSilent {
		return
	}
	assigned := dhcpReconnectOriginalAddress
	if behavior != dhcpReconnectAnswerOriginal {
		assigned = dhcpReconnectChangedAddress
	}
	responseType := dhcpv4.MessageTypeOffer
	if packet.MessageType() == dhcpv4.MessageTypeRequest {
		responseType = dhcpv4.MessageTypeAck
		if !requested.Equal(net.ParseIP(assigned)) {
			if behavior == dhcpReconnectIgnoreOriginal {
				return
			}
			responseType = dhcpv4.MessageTypeNak
		}
	}
	reply, err := dhcpv4.NewReplyFromRequest(
		packet,
		dhcpv4.WithMessageType(responseType),
		dhcpv4.WithYourIP(net.ParseIP(assigned)),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP(dhcpReconnectServerAddress))),
		dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
		dhcpv4.WithOption(dhcpv4.OptRouter(net.ParseIP(dhcpReconnectServerAddress))),
		dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(time.Hour)),
		dhcpv4.WithOption(dhcpv4.OptRenewTimeValue(t1)),
		dhcpv4.WithOption(dhcpv4.OptRebindingTimeValue(t2)),
	)
	if err == nil {
		_, _ = conn.WriteTo(reply.ToBytes(), peer)
	}
}

func startDHCPReconnectRuntime(
	t *testing.T,
	gateway netns.NsHandle,
	t1 time.Duration,
) *dhcpReconnectRuntime {
	t.Helper()
	return startDHCPReconnectRuntimeWithT2(t, gateway, t1, dhcpReconnectLongT2)
}

func startDHCPReconnectRuntimeWithT2(
	t *testing.T,
	gateway netns.NsHandle,
	t1 time.Duration,
	t2 time.Duration,
) *dhcpReconnectRuntime {
	t.Helper()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "mwan-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, stateDir, "/var/lib/mwan")
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
	t.Cleanup(func() { _ = downstream.namespace.Close() })
	provider := newRuntimePeer(
		t,
		gateway,
		"enatt0",
		"wan-vlan",
		nil,
		[]string{dhcpReconnectServerAddress + "/24"},
		"",
	)
	t.Cleanup(func() { _ = provider.namespace.Close() })
	reconnect := &dhcpReconnectRuntime{}
	setRuntimeNamespace(t, provider.namespace)
	reconnect.captureLeaseRequests(t, "wan-vlan")
	server, err := server4.NewServer(
		"wan-vlan",
		&net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			reconnect.answer(conn, peer, packet, t1, t2)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveResult; !errors.Is(err, net.ErrClosed) {
			t.Errorf("server4.Server.Serve: %v", err)
		}
	})
	setRuntimeNamespace(t, gateway)
	configPath := writeDHCPAcquireBackoffConfig(t, root)
	reconnect.daemon = startRoleDHCPv4RuntimeDaemon(
		t,
		os.Getenv(dhcpAcquireBackoffBinaryEnv),
		configPath,
		root,
		"oob",
	)
	t.Cleanup(func() { stopRuntimeDaemon(t, reconnect.daemon) })
	waitStaticRuntimeAddress(
		t,
		reconnect.daemon,
		"enatt0",
		dhcpReconnectOriginalAddress+"/24",
		true,
	)
	reconnect.link, err = netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	return reconnect
}

func (r *dhcpReconnectRuntime) flap(t *testing.T) time.Time {
	t.Helper()
	flappedAt := time.Now()
	if err := netlink.LinkSetDown(r.link); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(r.link); err != nil {
		t.Fatal(err)
	}
	return flappedAt
}

func (r *dhcpReconnectRuntime) requireVerification(t *testing.T, flappedAt time.Time) {
	t.Helper()
	deadline := flappedAt.Add(dhcpReconnectVerifyWait)
	for time.Now().Before(deadline) {
		for _, packet := range r.snapshot() {
			if packet.At.Before(flappedAt) || packet.Type != dhcpv4.MessageTypeRequest {
				continue
			}
			if !packet.Client.IsUnspecified() ||
				!packet.Requested.Equal(net.ParseIP(dhcpReconnectOriginalAddress)) ||
				packet.Server != nil {
				t.Fatalf("The verification request does not have the INIT-REBOOT form: %+v", packet)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(
		"The server did not receive a verification request after the interface returned. Daemon log: %s",
		runtimeLogTail(t, r.daemon, 30),
	)
}

func (r *dhcpReconnectRuntime) hasAddress(t *testing.T, cidr string) bool {
	t.Helper()
	addresses, err := netlink.AddrList(r.link, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if address.IPNet.String() == cidr {
			return true
		}
	}
	return false
}

func (r *dhcpReconnectRuntime) requireReplacement(t *testing.T, wait time.Duration) {
	t.Helper()
	cidr := dhcpReconnectChangedAddress + "/24"
	deadline := time.Now().Add(wait)
	for !r.hasAddress(t, cidr) {
		if !time.Now().Before(deadline) {
			t.Fatalf(
				"Address %s was absent after %s. Daemon log: %s",
				cidr,
				wait,
				runtimeLogTail(t, r.daemon, 30),
			)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r.hasAddress(t, dhcpReconnectOriginalAddress+"/24") {
		t.Fatalf(
			"The replaced address %s remained on the interface. Daemon log: %s",
			dhcpReconnectOriginalAddress+"/24",
			runtimeLogTail(t, r.daemon, 30),
		)
	}
}

func dhcpReconnectGateway(t *testing.T) netns.NsHandle {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	return gateway
}

func TestOOBDHCPv4ReconnectNAKReplacesLease(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPReconnectRuntime(t, dhcpReconnectGateway(t), dhcpReconnectLongT1)
	reconnect.behavior.Store(int32(dhcpReconnectNAKOriginal))
	reconnect.requireVerification(t, reconnect.flap(t))
	reconnect.requireReplacement(t, dhcpReconnectVerifyWait)
}

func TestOOBDHCPv4ReconnectDiscoversUpstreamThatIgnoresRequest(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPReconnectRuntime(t, dhcpReconnectGateway(t), dhcpReconnectLongT1)
	reconnect.behavior.Store(int32(dhcpReconnectIgnoreOriginal))
	reconnect.requireVerification(t, reconnect.flap(t))
	reconnect.requireReplacement(t, dhcpReconnectDiscoverWait)
}

func (r *dhcpReconnectRuntime) requireUnrelatedChangesIgnored(t *testing.T, mtu int) {
	t.Helper()
	if _, err := netlink.LinkByName("unrelated0"); err != nil {
		if err := netlink.LinkAdd(&netlink.Dummy{
			LinkAttrs: netlink.LinkAttrs{Name: "unrelated0"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(r.snapshot())
	for _, name := range []string{"unrelated0", "enmwanbr0"} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetDown(link); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.LinkSetMTU(r.link, mtu); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if count := len(r.snapshot()) - before; count != 0 {
		t.Fatalf(
			"Changes to unrelated interfaces and the interface MTU caused the client to send %d DHCP packets. Daemon log: %s",
			count,
			runtimeLogTail(t, r.daemon, 30),
		)
	}
}

func TestOOBDHCPv4ReconnectDuringRenewalVerifiesWithoutStaleRepeat(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPReconnectRuntime(t, dhcpReconnectGateway(t), dhcpReconnectInFlightT1)
	reconnect.behavior.Store(int32(dhcpReconnectSilent))
	renewalDeadline := time.Now().Add(dhcpReconnectInFlightT1 + dhcpReconnectT1Tolerance)
	for {
		renewing := false
		for _, packet := range reconnect.snapshot() {
			renewing = renewing ||
				(packet.Type == dhcpv4.MessageTypeRequest && !packet.Client.IsUnspecified())
		}
		if renewing {
			break
		}
		if !time.Now().Before(renewalDeadline) {
			t.Fatalf(
				"The T1 renewal request did not arrive within %s. Daemon log: %s",
				dhcpReconnectInFlightT1+dhcpReconnectT1Tolerance,
				runtimeLogTail(t, reconnect.daemon, 30),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
	reconnect.behavior.Store(int32(dhcpReconnectAnswerOriginal))
	reconnect.requireVerification(t, reconnect.flap(t))
	reconnect.requireUnrelatedChangesIgnored(t, 1400)
}

func TestOOBDHCPv4ReconnectSilentServerKeepsLeaseAndT1(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPReconnectRuntime(t, dhcpReconnectGateway(t), dhcpReconnectShortT1)
	boundAt := time.Now()
	reconnect.behavior.Store(int32(dhcpReconnectSilent))
	reconnect.requireUnrelatedChangesIgnored(t, 1400)
	reconnect.requireVerification(t, reconnect.flap(t))
	reconnect.requireScheduledRenewal(t, boundAt, dhcpReconnectShortT1)
}

func TestOOBDHCPv4ReconnectBeforeT1ReplacesLeaseBeforeT2(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPReconnectRuntimeWithT2(
		t,
		dhcpReconnectGateway(t),
		dhcpReconnectNearT1,
		dhcpReconnectFarT2,
	)
	boundAt := time.Now()
	reconnect.behavior.Store(int32(dhcpReconnectIgnoreOriginal))
	time.Sleep(dhcpReconnectBeforeT1)
	reconnect.requireVerification(t, reconnect.flap(t))
	reconnect.requireReplacement(t, time.Until(boundAt.Add(dhcpReconnectFarT2)))
	reconnect.requireTimedRenewal(t, boundAt, dhcpReconnectNearT1)
}

func TestOOBDHCPv4ReconnectBeforeT1SilentServerKeepsLeaseT1AndT2(t *testing.T) {
	if !dhcpAcquireBackoffIsolated(t) {
		return
	}
	reconnect := startDHCPReconnectRuntimeWithT2(
		t,
		dhcpReconnectGateway(t),
		dhcpReconnectNearT1,
		dhcpReconnectNearT2,
	)
	boundAt := time.Now()
	reconnect.behavior.Store(int32(dhcpReconnectSilent))
	time.Sleep(dhcpReconnectBeforeT1)
	reconnect.requireVerification(t, reconnect.flap(t))
	deadline := boundAt.Add(dhcpReconnectNearT2 + dhcpReconnectT1Tolerance)
	cidr := dhcpReconnectOriginalAddress + "/24"
	for time.Now().Before(deadline) {
		if !reconnect.hasAddress(t, cidr) {
			t.Fatalf(
				"The client ended the lease while the server stayed silent. Daemon log: %s",
				runtimeLogTail(t, reconnect.daemon, 30),
			)
		}
		if _, found := reconnect.firstRequestTo(net.IPv4bcast); found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	reconnect.requireTimedRenewal(t, boundAt, dhcpReconnectNearT1)
	rebinding, found := reconnect.firstRequestTo(net.IPv4bcast)
	if !found {
		t.Fatalf(
			"T2 rebinding request did not arrive %s after lease binding; daemon log: %s",
			dhcpReconnectNearT2,
			runtimeLogTail(t, reconnect.daemon, 30),
		)
	}
	if elapsed := rebinding.At.Sub(
		boundAt,
	); elapsed < dhcpReconnectNearT2-dhcpReconnectT1Tolerance ||
		elapsed > dhcpReconnectNearT2+dhcpReconnectT1Tolerance {
		t.Fatalf(
			"The T2 rebinding request arrived %s after the lease was bound; expected %s.",
			elapsed,
			dhcpReconnectNearT2,
		)
	}
}

func (r *dhcpReconnectRuntime) requireTimedRenewal(
	t *testing.T,
	boundAt time.Time,
	t1 time.Duration,
) {
	t.Helper()
	renewal, found := r.firstRequestTo(net.ParseIP(dhcpReconnectServerAddress))
	if !found {
		t.Fatalf(
			"The T1 renewal request did not arrive within %s. Daemon log: %s",
			t1+dhcpReconnectT1Tolerance,
			runtimeLogTail(t, r.daemon, 30),
		)
	}
	if elapsed := renewal.At.Sub(boundAt); elapsed < t1-dhcpReconnectT1Tolerance ||
		elapsed > t1+dhcpReconnectT1Tolerance {
		t.Fatalf(
			"The T1 renewal request arrived %s after the lease was bound; expected %s.",
			elapsed,
			t1,
		)
	}
}

func (r *dhcpReconnectRuntime) firstRequestTo(
	destination net.IP,
) (dhcpReconnectCapturedRequest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, request := range r.requests {
		if request.Destination.Equal(destination) {
			return request, true
		}
	}
	return dhcpReconnectCapturedRequest{}, false
}

func (r *dhcpReconnectRuntime) captureLeaseRequests(t *testing.T, iface string) {
	t.Helper()
	protocol := binary.NativeEndian.Uint16(binary.BigEndian.AppendUint16(nil, unix.ETH_P_IP))
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM, int(protocol))
	if err != nil {
		t.Fatal(err)
	}
	device, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(
		fd,
		&unix.SockaddrLinklayer{Protocol: protocol, Ifindex: device.Index},
	); err != nil {
		t.Fatal(err)
	}
	timeout := unix.NsecToTimeval((100 * time.Millisecond).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout); err != nil {
		t.Fatal(err)
	}
	var stopped atomic.Bool
	finished := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		for !stopped.Load() {
			size, _, err := unix.Recvfrom(fd, buffer, 0)
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				finished <- err
				return
			}
			r.recordLeaseRequest(buffer[:size])
		}
		finished <- nil
	}()
	t.Cleanup(func() {
		stopped.Store(true)
		if err := <-finished; err != nil {
			t.Error(err)
		}
		if err := unix.Close(fd); err != nil {
			t.Error(err)
		}
	})
}

func (r *dhcpReconnectRuntime) recordLeaseRequest(datagram []byte) {
	if len(datagram) < 20 || datagram[9] != unix.IPPROTO_UDP {
		return
	}
	headerLength := int(datagram[0]&0x0f) * 4
	if len(datagram) < headerLength+8 || binary.BigEndian.Uint16(datagram[headerLength+2:]) != 67 {
		return
	}
	packet, err := dhcpv4.FromBytes(datagram[headerLength+8:])
	if err != nil || packet.MessageType() != dhcpv4.MessageTypeRequest ||
		packet.ClientIPAddr.IsUnspecified() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(
		r.requests,
		dhcpReconnectCapturedRequest{
			At:          time.Now(),
			Destination: append(net.IP(nil), datagram[16:20]...),
		},
	)
}

func (r *dhcpReconnectRuntime) requireScheduledRenewal(
	t *testing.T,
	boundAt time.Time,
	t1 time.Duration,
) {
	t.Helper()
	cidr := dhcpReconnectOriginalAddress + "/24"
	deadline := boundAt.Add(t1 + dhcpReconnectT1Tolerance)
	for time.Now().Before(deadline) {
		if !r.hasAddress(t, cidr) {
			t.Fatalf(
				"The client ended the lease while the server stayed silent. Daemon log: %s",
				runtimeLogTail(t, r.daemon, 30),
			)
		}
		for _, packet := range r.snapshot() {
			if packet.Type != dhcpv4.MessageTypeRequest || packet.Client.IsUnspecified() {
				continue
			}
			elapsed := packet.At.Sub(boundAt)
			if elapsed < t1-dhcpReconnectT1Tolerance {
				t.Fatalf(
					"The T1 renewal request arrived %s after the lease was bound; expected %s.",
					elapsed,
					t1,
				)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf(
		"The T1 renewal request did not arrive within %s. Daemon log: %s",
		t1+dhcpReconnectT1Tolerance,
		runtimeLogTail(t, r.daemon, 30),
	)
}
