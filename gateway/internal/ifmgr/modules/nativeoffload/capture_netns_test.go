//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math/bits"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	nativeCaptureChild           = "MWAN_NATIVE_CAPTURE_CHILD"
	nativeCaptureTimeout         = 5 * time.Second
	nativeCaptureCapacity        = 8
	nativeCaptureSocketBytes     = 65536
	nativeCaptureSourcePort      = 23456
	nativeCaptureDestinationPort = 443
	nativeCaptureTrafficClass    = 0x28
	nativeCaptureFlowLabel       = 0x12345
)

func TestNativePacketObservation(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), nativeCaptureTimeout)
	defer cancel()
	input, sender := nativeCaptureLinks(t)
	observations := make(chan packetObservation, nativeCaptureCapacity)
	failures := make(chan error, 1)
	capture, err := openPacketCapture(ctx, input, nativeCaptureSocketBytes, observations, failures, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if capture.diagnostics != nil {
		t.Fatal("disabled capture allocated diagnostics")
	}
	t.Cleanup(func() {
		cancel()
		if err := capture.close(); err != nil {
			t.Error(err)
		}
		<-capture.done
	})
	for _, addresses := range [][2]string{
		{"192.0.2.10", "198.51.100.20"},
		{"2001:db8:1::10", "2001:db8:2::20"},
	} {
		for _, protocol := range []uint8{unix.IPPROTO_TCP, unix.IPPROTO_UDP} {
			frame := nativeCaptureFrame(t, sender, input, addresses, protocol)
			nativeCaptureSend(t, sender, frame)
			select {
			case observation := <-observations:
				if observation.ifindex != input.Index || observation.at.IsZero() || !observation.Eligible || observation.DecodeError != nil {
					t.Fatalf("kernel capture did not provide an eligible Rust observation: %+v", observation)
				}
				if observation.Tuple.IP.SourceAddress != netip.MustParseAddr(addresses[0]) || observation.Tuple.IP.DestinationAddress != netip.MustParseAddr(addresses[1]) || observation.Tuple.Proto.Protocol != protocol || observation.Tuple.Proto.SourcePort != nativeCaptureSourcePort || observation.Tuple.Proto.DestinationPort != nativeCaptureDestinationPort {
					t.Fatalf("Rust observation changed the transmitted tuple: %+v", observation)
				}
				if observation.SourceMAC != [6]byte(sender.HardwareAddr) || observation.DestinationMAC != [6]byte(input.HardwareAddr) || observation.TrafficClass != nativeCaptureTrafficClass || observation.TTL != 64 {
					t.Fatalf("Rust observation changed Ethernet or route selectors: %+v", observation)
				}
				if observation.Tuple.IP.SourceAddress.Is6() && observation.FlowLabel != nativeCaptureFlowLabel || observation.Tuple.IP.SourceAddress.Is4() && observation.FlowLabel != 0 {
					t.Fatalf("Rust observation changed the IP flow label: %+v", observation)
				}
			case err := <-failures:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal("native observation did not capture the transmitted frame")
			}
		}
	}
	var output bytes.Buffer
	module := &Module{captures: []*packetCapture{capture}}
	module.Log = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	module.maintain(ctx)
	if strings.Contains(output.String(), "native capture diagnostic") {
		t.Fatalf("disabled capture emitted diagnostics: %s", output.String())
	}
}

func TestNativePacketObservationDiagnostics(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/default/disable_ipv6", []byte("1"), 0o644); err != nil {
		t.Fatalf("disable IPv6 for new capture links: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), nativeCaptureTimeout)
	defer cancel()
	input, sender := nativeCaptureLinks(t)
	for _, link := range []*net.Interface{input, sender} {
		value, err := os.ReadFile("/proc/sys/net/ipv6/conf/" + link.Name + "/disable_ipv6")
		if err != nil {
			t.Fatalf("read IPv6 setting for %s: %v", link.Name, err)
		}
		if strings.TrimSpace(string(value)) != "1" {
			t.Fatalf("IPv6 remains enabled on %s: %q", link.Name, value)
		}
	}
	var paused atomic.Bool
	paused.Store(true)
	observations := make(chan packetObservation, 1)
	failures := make(chan error, 1)
	capture, err := openPacketCapture(ctx, input, nativeCaptureSocketBytes, observations, failures, &paused, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := capture.close(); err != nil {
			t.Error(err)
		}
		<-capture.done
	})
	frame := nativeCaptureFrame(t, sender, input, [2]string{"192.0.2.10", "198.51.100.20"}, unix.IPPROTO_TCP)
	for range 3 {
		nativeCaptureSend(t, sender, frame)
	}
	awaitCaptureDiagnostic(t, ctx, capture, capturePaused, 3, failures)
	var output bytes.Buffer
	module := &Module{captures: []*packetCapture{capture}}
	module.Log = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	module.logCaptureDiagnostics(ctx)
	report := output.String()
	if strings.Count(report, `"stage":"paused"`) != 1 || !strings.Contains(report, `"paused":3`) || !strings.Contains(report, `"eligible":true`) || !strings.Contains(report, `"auxiliary_status":`) || !strings.Contains(report, `"192.0.2.10"`) {
		t.Fatalf("capture report did not include the paused frame and counter: %s", report)
	}
	output.Reset()
	module.logCaptureDiagnostics(ctx)
	if strings.Contains(output.String(), `"stage":"paused"`) {
		t.Fatalf("capture repeated a sample without a new frame: %s", output.String())
	}
	nativeCaptureSend(t, sender, frame)
	awaitCaptureDiagnostic(t, ctx, capture, capturePaused, 4, failures)
	output.Reset()
	module.logCaptureDiagnostics(ctx)
	if strings.Count(output.String(), `"stage":"paused"`) != 1 || !strings.Contains(output.String(), `"paused":4`) {
		t.Fatalf("capture did not report a fresh sample and monotonic counter: %s", output.String())
	}
	select {
	case observation := <-observations:
		t.Fatalf("paused capture admitted an observation: %+v", observation)
	default:
	}
	paused.Store(false)
	for range 2 {
		nativeCaptureSend(t, sender, frame)
	}
	awaitCaptureDiagnostic(t, ctx, capture, captureQueueFull, 1, failures)
	output.Reset()
	module.logCaptureDiagnostics(ctx)
	if !strings.Contains(output.String(), `"queued":1`) || !strings.Contains(output.String(), `"queue_full":1`) || !strings.Contains(output.String(), `"stage":"queue_full"`) {
		t.Fatalf("capture did not report queue admission and overflow: %s", output.String())
	}
	select {
	case observation := <-observations:
		if !observation.Eligible {
			t.Fatalf("capture admitted an ineligible observation: %+v", observation)
		}
	default:
		t.Fatal("capture did not admit the eligible frame")
	}
	ethernet := &layers.Ethernet{SrcMAC: sender.HardwareAddr, DstMAC: input.HardwareAddr, EthernetType: layers.EthernetTypeARP}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true}, ethernet, gopacket.Payload("native observation")); err != nil {
		t.Fatal(err)
	}
	nativeCaptureSend(t, sender, buffer.Bytes())
	awaitCaptureDiagnostic(t, ctx, capture, captureDecoderIneligible, 1, failures)
	output.Reset()
	module.logCaptureDiagnostics(ctx)
	if !strings.Contains(output.String(), `"decoder_ineligible":1`) || !strings.Contains(output.String(), `"stage":"decoder_ineligible"`) || !strings.Contains(output.String(), `"eligible":false`) {
		t.Fatalf("capture did not report the decoder rejection: %s", output.String())
	}
}

func awaitCaptureDiagnostic(t *testing.T, ctx context.Context, capture *packetCapture, stage captureStage, minimum uint64, failures <-chan error) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		capture.diagnostics.mu.Lock()
		count := capture.diagnostics.counts[stage]
		capture.diagnostics.mu.Unlock()
		if count >= minimum {
			return
		}
		select {
		case failure := <-failures:
			t.Fatal(failure)
		case <-ctx.Done():
			t.Fatal("capture did not report the expected frames")
		case <-ticker.C:
		}
	}
}

func TestNativePacketObservationCapacityFallback(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), nativeCaptureTimeout)
	defer cancel()
	input, sender := nativeCaptureLinks(t)
	admissionAddress(t, input, "192.0.2.1/24")
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: nativeCaptureDestinationPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := server.SetReadDeadline(time.Now().Add(nativeCaptureTimeout)); err != nil {
		t.Fatal(err)
	}
	var paused atomic.Bool
	observations := make(chan packetObservation, 1)
	failures := make(chan error, 1)
	capture, err := openPacketCapture(ctx, input, nativeCaptureSocketBytes, observations, failures, &paused, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := capture.close(); err != nil {
			t.Error(err)
		}
		<-capture.done
	})
	frame := nativeCaptureFrame(t, sender, input, [2]string{"192.0.2.2", "192.0.2.1"}, unix.IPPROTO_UDP)
	for range 2 {
		nativeCaptureSend(t, sender, frame)
	}
	awaitObservationSkip(t, ctx, capture, 0, failures)
	first := <-observations
	for range 2 {
		requireNativeUDPDelivery(t, server)
	}
	nativeCaptureSend(t, sender, frame)
	select {
	case current := <-observations:
		if !current.Eligible || !current.at.After(first.at) {
			t.Fatalf("capacity fallback did not preserve a fresh observation: %+v", current)
		}
	case failure := <-failures:
		t.Fatal(failure)
	case <-ctx.Done():
		t.Fatal("capture did not continue after capacity fallback")
	}
	requireNativeUDPDelivery(t, server)
	paused.Store(true)
	before := capture.skipped.Load()
	nativeCaptureSend(t, sender, frame)
	awaitObservationSkip(t, ctx, capture, before, failures)
	requireNativeUDPDelivery(t, server)
	select {
	case observation := <-observations:
		t.Fatalf("paused admission accepted an observation: %+v", observation)
	default:
	}
	paused.Store(false)
	nativeCaptureSend(t, sender, frame)
	select {
	case observation := <-observations:
		if !observation.Eligible {
			t.Fatalf("capture did not resume eligible observations: %+v", observation)
		}
	case failure := <-failures:
		t.Fatal(failure)
	case <-ctx.Done():
		t.Fatal("capture did not resume after the forwarding pass")
	}
	requireNativeUDPDelivery(t, server)
}

func TestNativePacketObservationPanicPreservesLinuxDelivery(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), nativeCaptureTimeout)
	defer cancel()
	input, sender := nativeCaptureLinks(t)
	admissionAddress(t, input, "192.0.2.1/24")
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: nativeCaptureDestinationPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := server.SetReadDeadline(time.Now().Add(nativeCaptureTimeout)); err != nil {
		t.Fatal(err)
	}
	var paused atomic.Bool
	observations := make(chan packetObservation, 1)
	failures := make(chan error, 1)
	capture, err := openPacketCapture(ctx, input, nativeCaptureSocketBytes, observations, failures, &paused, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := capture.close(); err != nil {
			t.Error(err)
		}
		<-capture.done
	})
	close(observations)
	frame := nativeCaptureFrame(t, sender, input, [2]string{"192.0.2.2", "192.0.2.1"}, unix.IPPROTO_UDP)
	nativeCaptureSend(t, sender, frame)
	select {
	case failure := <-failures:
		var panicked *nativePanicError
		if !errors.As(failure, &panicked) || !paused.Load() {
			t.Fatalf("capture panic did not stop admission and publish failure: paused=%v failure=%v", paused.Load(), failure)
		}
	case <-ctx.Done():
		t.Fatal("capture panic did not publish failure")
	}
	select {
	case <-capture.done:
	case <-ctx.Done():
		t.Fatal("capture panic did not stop the failed reader")
	}
	requireNativeUDPDelivery(t, server)
	nativeCaptureSend(t, sender, frame)
	requireNativeUDPDelivery(t, server)
}

func awaitObservationSkip(t *testing.T, ctx context.Context, capture *packetCapture, previous uint64, failures <-chan error) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for capture.skipped.Load() <= previous {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		case <-ctx.Done():
			t.Fatal("capture did not report the skipped observation")
		case <-ticker.C:
		}
	}
}

func requireNativeUDPDelivery(t *testing.T, server *net.UDPConn) {
	t.Helper()
	buffer := make([]byte, observationFrameBytes)
	count, _, err := server.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != "native observation" {
		t.Fatalf("Linux delivery changed the application payload: %q", buffer[:count])
	}
}

func nativeCaptureNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(nativeCaptureChild) == t.Name() {
		return true
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "-test.run=^"+t.Name()+"$", "-test.v")
	command.Env = append(os.Environ(), nativeCaptureChild+"="+t.Name())
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated native packet observations: %v: %s", err, output)
	}
	t.Log(string(output))
	return false
}

func nativeCaptureLinks(t *testing.T) (*net.Interface, *net.Interface) {
	t.Helper()
	link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "native-in", HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}}, PeerName: "native-peer", PeerHardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 2}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"native-in", "native-peer"} {
		current, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(current); err != nil {
			t.Fatal(err)
		}
	}
	input, err := net.InterfaceByName("native-in")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.InterfaceByName("native-peer")
	if err != nil {
		t.Fatal(err)
	}
	return input, peer
}

func nativeCaptureFrame(t *testing.T, sender, input *net.Interface, addresses [2]string, protocol uint8) []byte {
	t.Helper()
	return nativeCaptureFrameSelectors(t, sender, input, addresses, protocol, nativeCaptureTrafficClass)
}

func nativeCaptureFrameSelectors(t *testing.T, sender, input *net.Interface, addresses [2]string, protocol, trafficClass uint8) []byte {
	t.Helper()
	source, destination := net.ParseIP(addresses[0]), net.ParseIP(addresses[1])
	ethernet := &layers.Ethernet{SrcMAC: sender.HardwareAddr, DstMAC: input.HardwareAddr, EthernetType: layers.EthernetTypeIPv4}
	var network gopacket.NetworkLayer
	var serialNetwork gopacket.SerializableLayer
	if source.To4() != nil {
		ipv4 := &layers.IPv4{Version: 4, IHL: 5, TTL: 64, TOS: trafficClass, SrcIP: source, DstIP: destination, Protocol: layers.IPProtocol(protocol)}
		network, serialNetwork = ipv4, ipv4
	} else {
		ethernet.EthernetType = layers.EthernetTypeIPv6
		ipv6 := &layers.IPv6{Version: 6, HopLimit: 64, TrafficClass: trafficClass, FlowLabel: nativeCaptureFlowLabel, SrcIP: source, DstIP: destination, NextHeader: layers.IPProtocol(protocol)}
		network, serialNetwork = ipv6, ipv6
	}
	var transport gopacket.SerializableLayer
	if protocol == unix.IPPROTO_TCP {
		tcp := &layers.TCP{SrcPort: nativeCaptureSourcePort, DstPort: nativeCaptureDestinationPort, ACK: true, Seq: 1, Ack: 1, Window: 1024}
		if err := tcp.SetNetworkLayerForChecksum(network); err != nil {
			t.Fatal(err)
		}
		transport = tcp
	} else {
		udp := &layers.UDP{SrcPort: nativeCaptureSourcePort, DstPort: nativeCaptureDestinationPort}
		if err := udp.SetNetworkLayerForChecksum(network); err != nil {
			t.Fatal(err)
		}
		transport = udp
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, ethernet, serialNetwork, transport, gopacket.Payload("native observation")); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func nativeCaptureSend(t *testing.T, sender *net.Interface, frame []byte) {
	t.Helper()
	descriptor, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(bits.ReverseBytes16(unix.ETH_P_ALL)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Close(descriptor); err != nil {
			t.Error(err)
		}
	}()
	if err := unix.Sendto(descriptor, frame, 0, &unix.SockaddrLinklayer{Ifindex: sender.Index, Protocol: bits.ReverseBytes16(unix.ETH_P_ALL), Halen: 6}); err != nil {
		t.Fatal(err)
	}
}
