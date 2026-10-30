//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/ti-mo/conntrack"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/firewall"
)

const nativeGSOPayloadBytes = 2 * 1024 * 1024

// Linux UAPI defines TCPI_OPT_TIMESTAMPS in linux/tcp.h; x/sys omits it.
const nativeTCPInfoTimestamps = 1

func TestNativeKernelGSOAdmissionIPv4(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	requireNativeKernelGSOAdmission(t, admissionClientV4, admissionRemoteV4, "192.0.2.1", 24)
}

func TestNativeKernelGSOAdmissionIPv6(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	requireNativeKernelGSOAdmission(t, admissionClientV6, admissionRemoteV6, "2001:db8:1::1", 64)
}

func requireNativeKernelGSOAdmission(t *testing.T, source, destination, gateway string, prefix int) {
	t.Helper()
	fixture := newAdmissionFixture(t, config.TranslationNative)
	capture, failures := nativeGSOCapture(t, fixture)
	address := nativeGSOServerAddress(t, fixture, destination)
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP(destination), Port: nativeCaptureDestinationPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(nativeCaptureTimeout)); err != nil {
		t.Fatal(err)
	}
	client := nativeGSOClient(t, fixture, source, destination, gateway, prefix)
	server, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	nativeGSOTransfer(t, client, server)
	awaitCaptureDiagnostic(t, fixture.ctx, capture, captureKernelAggregate, 1, failures)
	capture.diagnostics.mu.Lock()
	sample := *capture.diagnostics.samples[captureKernelAggregate]
	capture.diagnostics.mu.Unlock()
	observation := requireNativeGSOSample(t, fixture, sample, source, destination, nativeGSOTCPInfo(t, client))
	requireNativeGSOReport(t, fixture, capture, sample)
	link, err := netlink.LinkByIndex(fixture.input.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrDel(link, address); err != nil {
		t.Fatal(err)
	}
	fixture.publishFirewall(t)
	if err := fixture.tracking.Update(conntrack.Flow{TupleOrig: observation.Tuple, Mark: admissionMark}); err != nil {
		t.Fatal(err)
	}
	requireNativeGSOCandidate(t, fixture, observation)
}

func nativeGSOCapture(t *testing.T, fixture admissionFixture) (*packetCapture, <-chan error) {
	t.Helper()
	observations := make(chan packetObservation, nativeCaptureCapacity)
	failures := make(chan error, 1)
	capture, err := openPacketCapture(fixture.ctx, fixture.input, nativeCaptureSocketBytes, observations, failures, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := capture.close(); err != nil {
			t.Error(err)
		}
		<-capture.done
	})
	return capture, failures
}

func requireNativeGSOReport(t *testing.T, fixture admissionFixture, capture *packetCapture, sample captureSample) {
	t.Helper()
	var output bytes.Buffer
	module := &Module{captures: []*packetCapture{capture}}
	module.Log = slog.New(slog.NewJSONHandler(&output, nil))
	module.logCaptureDiagnostics(fixture.ctx)
	report := readCaptureDiagnosticReport(t, output.String(), fixture.input)
	aggregate := report.Samples["kernel_aggregate"]
	if !aggregate.Eligible || aggregate.GSOType != sample.kernel.GSOType || aggregate.GSOSize != sample.kernel.GSOSize || aggregate.NetworkLength != sample.decoded.NetworkLength || aggregate.EffectiveNetworkLength != sample.decoded.EffectiveNetworkLength || !aggregate.ObservedAt.Equal(sample.observedAt) {
		t.Fatalf("capture diagnostics changed the accepted kernel aggregate: %s", output.String())
	}
	if _, exists := report.Samples["checksum_pending"]; exists {
		t.Fatalf("capture reported an accepted deferred checksum as rejected: %s", output.String())
	}
}

func nativeGSOServerAddress(t *testing.T, fixture admissionFixture, destination string) *netlink.Addr {
	t.Helper()
	ip := netip.MustParseAddr(destination)
	prefix := netip.PrefixFrom(ip, ip.BitLen())
	admissionAddress(t, fixture.input, prefix.String())
	address, err := netlink.ParseAddr(prefix.String())
	if err != nil {
		t.Fatal(err)
	}
	configuration := fixture.module.cfg.Firewall
	family := firewall.IPv4
	if ip.Is6() {
		family = firewall.IPv6
	}
	configuration.LocalPermits = []firewall.TransportPermit{{
		InputInterface: fixture.input.Name, Family: family, Protocol: "tcp", DestinationPort: nativeCaptureDestinationPort,
	}}
	rules, err := firewall.Compile(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := firewall.Apply(fixture.ctx, rules); err != nil {
		t.Fatal(err)
	}
	return address
}

func nativeGSOClient(t *testing.T, fixture admissionFixture, source, destination, gateway string, prefix int) *net.TCPConn {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = original.Close() }()
	root, err := netlink.NewHandleAt(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	link, err := root.LinkByIndex(fixture.sender.Index)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := netns.Set(original); err != nil {
			t.Fatalf("restore capture test namespace: %v", err)
		}
		_ = peer.Close()
	}()
	if err := root.LinkSetNsFd(link, int(peer)); err != nil {
		t.Fatal(err)
	}
	nativeGSOClientNetwork(t, fixture, source, destination, gateway, prefix)
	return nativeGSODial(t, source, destination)
}

func nativeGSOClientNetwork(t *testing.T, fixture admissionFixture, source, destination, gateway string, prefix int) {
	t.Helper()
	link, err := netlink.LinkByName(fixture.sender.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	current, err := net.InterfaceByName(fixture.sender.Name)
	if err != nil {
		t.Fatal(err)
	}
	admissionAddress(t, current, netip.PrefixFrom(netip.MustParseAddr(source), prefix).String())
	ip := netip.MustParseAddr(destination)
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: current.Index, Gw: net.ParseIP(gateway),
		Dst: &net.IPNet{IP: ip.AsSlice(), Mask: net.CIDRMask(ip.BitLen(), ip.BitLen())},
	}); err != nil {
		t.Fatal(err)
	}
	if err := netlink.NeighSet(&netlink.Neigh{
		LinkIndex: current.Index, IP: net.ParseIP(gateway), HardwareAddr: fixture.input.HardwareAddr, State: unix.NUD_PERMANENT,
	}); err != nil {
		t.Fatal(err)
	}
}

func nativeGSODial(t *testing.T, source, destination string) *net.TCPConn {
	t.Helper()
	family, level, option := unix.AF_INET, unix.IPPROTO_IP, unix.IP_TOS
	var local, remote unix.Sockaddr
	if netip.MustParseAddr(source).Is4() {
		local = &unix.SockaddrInet4{Port: nativeCaptureSourcePort, Addr: netip.MustParseAddr(source).As4()}
		remote = &unix.SockaddrInet4{Port: nativeCaptureDestinationPort, Addr: netip.MustParseAddr(destination).As4()}
	} else {
		family, level, option = unix.AF_INET6, unix.IPPROTO_IPV6, unix.IPV6_TCLASS
		local = &unix.SockaddrInet6{Port: nativeCaptureSourcePort, Addr: netip.MustParseAddr(source).As16()}
		remote = &unix.SockaddrInet6{Port: nativeCaptureDestinationPort, Addr: netip.MustParseAddr(destination).As16()}
	}
	descriptor, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(descriptor), "native-gso-client")
	defer func() { _ = file.Close() }()
	if err := unix.SetsockoptInt(descriptor, level, option, admissionTrafficClass); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetsockoptTimeval(descriptor, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &unix.Timeval{Sec: int64(nativeCaptureTimeout / time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(descriptor, local); err != nil {
		t.Fatal(err)
	}
	if err := unix.Connect(descriptor, remote); err != nil {
		t.Fatal(err)
	}
	connection, err := net.FileConn(file)
	if err != nil {
		t.Fatal(err)
	}
	client := connection.(*net.TCPConn)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func nativeGSOTransfer(t *testing.T, client, server *net.TCPConn) {
	t.Helper()
	deadline := time.Now().Add(nativeCaptureTimeout)
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x5a}, nativeGSOPayloadBytes)
	written := make(chan error, 1)
	go func() {
		_, err := client.Write(payload)
		written <- err
	}()
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(server, received); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, received) {
		t.Fatal("kernel TCP delivery changed the aggregate payload")
	}
}

func nativeGSOTCPInfo(t *testing.T, client *net.TCPConn) *unix.TCPInfo {
	t.Helper()
	raw, err := client.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var information *unix.TCPInfo
	var queryErr error
	if err := raw.Control(func(descriptor uintptr) {
		information, queryErr = unix.GetsockoptTCPInfo(int(descriptor), unix.IPPROTO_TCP, unix.TCP_INFO)
	}); err != nil {
		t.Fatal(err)
	}
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	return information
}

func requireNativeGSOSample(t *testing.T, fixture admissionFixture, sample captureSample, source, destination string, information *unix.TCPInfo) packetObservation {
	t.Helper()
	observation := sample.decoded
	metadata := sample.kernel
	ipHeader, gsoType := uint32(20), uint8(1)
	if netip.MustParseAddr(source).Is6() {
		ipHeader, gsoType = 40, 4
	}
	if !sample.vnetValid || !sample.statusValid || !sample.observedAtValid || !sample.decodedValid || !observation.Eligible || observation.DecodeError != nil {
		t.Fatalf("kernel aggregate lacks valid capture evidence: %+v", sample)
	}
	if observation.NetworkLength <= uint32(fixture.output.MTU) || observation.EffectiveNetworkLength == 0 || observation.EffectiveNetworkLength > uint32(fixture.output.MTU) || metadata.GSOType&0x7f != gsoType || metadata.GSOSize == 0 {
		t.Fatalf("kernel TCP did not produce an eligible aggregate above MTU: observation=%+v metadata=%+v", observation, metadata)
	}
	if metadata.AuxiliaryStatus&0x108 != 0x108 || metadata.VnetFlags != 1 || uint32(metadata.ChecksumStart) != 14+ipHeader || metadata.ChecksumOffset != 16 || metadata.PacketLength != observation.NetworkLength+14 || metadata.CapturedLength != metadata.PacketLength || metadata.MACOffset != 0 || metadata.NetworkOffset != 14 {
		t.Fatalf("kernel aggregate changed checksum or capture offsets: %+v", metadata)
	}
	tcpHeader := uint32(20)
	if information.Options&nativeTCPInfoTimestamps != 0 {
		tcpHeader += 12
	}
	if uint32(metadata.GSOSize) != information.Snd_mss || observation.EffectiveNetworkLength != ipHeader+tcpHeader+information.Snd_mss || uint32(metadata.HeaderLength) < 14+ipHeader+tcpHeader || uint32(metadata.HeaderLength) > metadata.PacketLength {
		t.Fatalf("Rust segment length disagrees with TCP header bounds: observation=%+v metadata=%+v", observation, metadata)
	}
	if observation.Tuple.IP.SourceAddress.String() != source || observation.Tuple.IP.DestinationAddress.String() != destination || observation.Tuple.Proto.Protocol != unix.IPPROTO_TCP || observation.Tuple.Proto.SourcePort != nativeCaptureSourcePort || observation.Tuple.Proto.DestinationPort != nativeCaptureDestinationPort || observation.SourceMAC != [6]byte(fixture.sender.HardwareAddr) || observation.DestinationMAC != [6]byte(fixture.input.HardwareAddr) || observation.TrafficClass != admissionTrafficClass || observation.TTL != 64 || observation.TCPFlags&0x10 == 0 {
		t.Fatalf("Rust changed the kernel TCP headers: %+v", observation)
	}
	t.Logf("kernel aggregate: metadata=%+v observation=%+v timestamp=%s", metadata, observation, sample.observedAt)
	return packetObservation{NativeObservation: observation.NativeObservation, EffectiveNetworkLength: observation.EffectiveNetworkLength, ifindex: fixture.input.Index, at: sample.observedAt}
}

func requireNativeGSOCandidate(t *testing.T, fixture admissionFixture, observation packetObservation) {
	t.Helper()
	spec, guards, flow, wan, eligibility, err := fixture.module.candidate(fixture.ctx, observation)
	if err != nil || eligibility != EligibilityReady || wan.Key() != admissionWANID || flow.TupleOrig != observation.Tuple || spec.Directions[0].Original != observation.Tuple || spec.Directions[0].OutputMTU != uint16(fixture.output.MTU) || guards[0].FlowLabel != observation.FlowLabel {
		t.Fatalf("kernel aggregate did not pass current candidate evidence: spec=%+v flow=%+v eligibility=%d error=%v", spec, flow, eligibility, err)
	}
	if _, err := fixture.tracking.Get(conntrack.Flow{ID: flow.ID, TupleOrig: flow.TupleOrig}); err != nil {
		t.Fatal(err)
	}
	family, gateway := unix.AF_INET, "198.51.100.2"
	if observation.Tuple.IP.SourceAddress.Is6() {
		family, gateway = unix.AF_INET6, "2001:db8:2::2"
	}
	route := netlink.Route{Family: family, Table: admissionRouteTable, LinkIndex: fixture.output.Index, Gw: net.ParseIP(gateway), MTU: int(observation.EffectiveNetworkLength) - 1}
	if err := netlink.RouteReplace(&route); err != nil {
		t.Fatal(err)
	}
	fixture.requireRejected(t, observation, EligibilityUnsupported)
	route.MTU = 0
	if err := netlink.RouteReplace(&route); err != nil {
		t.Fatal(err)
	}
	observation.EffectiveNetworkLength = 0
	fixture.requireRejected(t, observation, EligibilityUnsupported)
	observation.EffectiveNetworkLength = observation.NetworkLength + 1
	fixture.requireRejected(t, observation, EligibilityUnsupported)
}
