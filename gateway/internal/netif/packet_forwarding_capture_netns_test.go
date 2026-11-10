//go:build linux && netns

package netif_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"sync"
	"testing"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

const (
	forwardPathCaptureBytes    = 2048
	forwardPathInitialHopLimit = 64
)

type forwardPathPacketResult struct {
	output   int
	hopLimit uint8
	protocol layers.EthernetType
	err      error
}

func requireForwardPathPacket(t *testing.T, fixture forwardPathFixture, endpoints forwardPathPacketEndpoints, expected int, phase string) {
	t.Helper()
	path, usable, queryErr := netif.ForwardPathLookup(t.Context(), slog.Default(), fixture.selector)
	t.Logf("forwarding lookup: phase=%s usable=%v route=%+v error=%v", phase, usable, path.Route, queryErr)
	observed := observeForwardPathPacket(t, fixture, endpoints, phase)
	t.Logf("forwarding wire: phase=%s result=%+v", phase, observed)
	if observed.output != expected || observed.hopLimit != forwardPathInitialHopLimit-1 {
		family := unix.AF_INET
		if fixture.selector.Source.Addr().Is6() {
			family = unix.AF_INET6
		}
		rules, ruleErr := netlink.RuleList(family)
		t.Fatalf("forwarded %s packet differs: result=%+v expected_output=%d query_usable=%v query_route=%+v query_error=%v input=%s/%d selector=%+v rules=%v rule_error=%v", phase,
			observed, expected, usable, path.Route, queryErr, fixture.input.Attrs().Name, fixture.input.Attrs().Index, fixture.selector, rules, ruleErr)
	}
	output := fixture.outputs[observed.output].Attrs().Index
	if queryErr != nil || !usable || path.Link == nil || path.Route.LinkIndex != output || path.Link.Attrs().Index != output ||
		path.Neighbor.LinkIndex != output || !path.Neighbor.IP.Equal(fixture.gateways[observed.output]) ||
		!bytes.Equal(path.Neighbor.HardwareAddr, fixture.peers[observed.output].Attrs().HardwareAddr) {
		t.Fatalf("lookup differs from forwarded %s packet: wire_output=%d query_usable=%v query_path=%+v query_error=%v", phase, output, usable, path, queryErr)
	}
	t.Logf("kernel packet and lookup agree: input=%s/%d phase=%s output=%s/%d protocol=%s hop_limit=%d", fixture.input.Attrs().Name,
		fixture.input.Attrs().Index, phase, fixture.outputs[observed.output].Attrs().Name, fixture.outputs[observed.output].Attrs().Index, observed.protocol, observed.hopLimit)
}

func observeForwardPathPacket(t *testing.T, fixture forwardPathFixture, endpoints forwardPathPacketEndpoints, phase string) forwardPathPacketResult {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var readers sync.WaitGroup
	var captures []*packet.Conn
	defer func() {
		cancel()
		for _, capture := range captures {
			if err := capture.Close(); err != nil {
				t.Errorf("close the forwarding capture: %v", err)
			}
		}
		readers.Wait()
	}()
	marker := []byte(t.Name() + "/" + phase)
	results := make(chan forwardPathPacketResult, len(fixture.peers))
	for index, peer := range fixture.peers {
		capture := openForwardPathReceiverSocket(t, endpoints.receiver, peer.Attrs().Name)
		captures = append(captures, capture)
		readers.Add(1)
		go func() {
			defer readers.Done()
			results <- readForwardPathPacket(ctx, capture, index, marker)
		}()
	}
	state := inspectForwardPathPacketSocket(t, endpoints.sender)
	if state.mark != int(fixture.selector.Mark) {
		t.Fatalf("sender socket mark %d does not match selector mark %d", state.mark, fixture.selector.Mark)
	}
	t.Logf("forwarding witness: sender=%s/%d sender_namespace=%s socket_mark=%d socket_owner_uid=%d input=%s/%d selector=%+v", endpoints.client.Name,
		state.index, endpoints.senderNamespace, state.mark, state.ownerUID, fixture.input.Attrs().Name, fixture.input.Attrs().Index, fixture.selector)
	frame := encodeForwardPathPacket(t, fixture, endpoints.client.HardwareAddr, marker)
	count, err := endpoints.sender.WriteTo(frame, &packet.Addr{HardwareAddr: fixture.input.Attrs().HardwareAddr})
	if err != nil || count != len(frame) {
		t.Fatalf("send the forwarding packet: bytes=%d expected=%d error=%v", count, len(frame), err)
	}
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("receive the forwarded packet on output %d: %v", result.output, result.err)
		}
		return result
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return forwardPathPacketResult{output: 0, hopLimit: 0, protocol: 0, err: ctx.Err()}
	}
}

type forwardPathPacketSocketState struct {
	index    int
	mark     int
	ownerUID uint32
}

func inspectForwardPathPacketSocket(t *testing.T, connection *packet.Conn) forwardPathPacketSocketState {
	t.Helper()
	raw, err := connection.SyscallConn()
	if err != nil {
		t.Fatalf("inspect sender packet socket: %v", err)
	}
	var state forwardPathPacketSocketState
	var readErr error
	if err := raw.Control(func(descriptor uintptr) {
		if descriptor > math.MaxInt32 {
			readErr = fmt.Errorf("sender socket descriptor exceeds the kernel range")
			return
		}
		address, err := unix.Getsockname(int(descriptor))
		if err != nil {
			readErr = err
			return
		}
		bound, valid := address.(*unix.SockaddrLinklayer)
		if !valid {
			readErr = fmt.Errorf("sender socket returned address type %T", address)
			return
		}
		state.index = bound.Ifindex
		state.mark, readErr = unix.GetsockoptInt(int(descriptor), unix.SOL_SOCKET, unix.SO_MARK)
		if readErr != nil {
			return
		}
		var identity unix.Stat_t
		readErr = unix.Fstat(int(descriptor), &identity)
		state.ownerUID = identity.Uid
	}); err != nil {
		t.Fatalf("inspect sender socket control: %v", err)
	}
	if readErr != nil {
		t.Fatalf("inspect sender socket metadata: %v", readErr)
	}
	return state
}

func readForwardPathPacket(ctx context.Context, connection *packet.Conn, output int, marker []byte) forwardPathPacketResult {
	buffer := make([]byte, forwardPathCaptureBytes)
	for {
		count, _, err := connection.ReadFrom(buffer)
		if err != nil {
			return forwardPathPacketResult{output: output, hopLimit: 0, protocol: 0, err: err}
		}
		frame := gopacket.NewPacket(buffer[:count], layers.LayerTypeEthernet, gopacket.Default)
		udp, valid := frame.Layer(layers.LayerTypeUDP).(*layers.UDP)
		if valid && bytes.Equal(udp.Payload, marker) {
			var result forwardPathPacketResult
			result.output = output
			if ethernet, ok := frame.Layer(layers.LayerTypeEthernet).(*layers.Ethernet); ok {
				result.protocol = ethernet.EthernetType
			}
			if ipv4, ok := frame.Layer(layers.LayerTypeIPv4).(*layers.IPv4); ok {
				result.hopLimit = ipv4.TTL
			}
			if ipv6, ok := frame.Layer(layers.LayerTypeIPv6).(*layers.IPv6); ok {
				result.hopLimit = ipv6.HopLimit
			}
			return result
		}
		if err := ctx.Err(); err != nil {
			return forwardPathPacketResult{output: output, hopLimit: 0, protocol: 0, err: err}
		}
	}
}

func encodeForwardPathPacket(t *testing.T, fixture forwardPathFixture, sourceMAC net.HardwareAddr, payload []byte) []byte {
	t.Helper()
	selector := fixture.selector
	if selector.Protocol != unix.IPPROTO_UDP || selector.Mark != 0 {
		t.Fatal("forwarding capture requires an unmarked UDP selector")
	}
	var ethernet layers.Ethernet
	ethernet.SrcMAC, ethernet.DstMAC = sourceMAC, fixture.input.Attrs().HardwareAddr
	var network gopacket.NetworkLayer
	var serialNetwork gopacket.SerializableLayer
	if selector.Source.Addr().Is4() {
		ethernet.EthernetType = layers.EthernetTypeIPv4
		var ipv4 layers.IPv4
		ipv4.Version, ipv4.IHL, ipv4.TTL = 4, 5, forwardPathInitialHopLimit
		ipv4.TOS, ipv4.Protocol = selector.TrafficClass, layers.IPProtocolUDP
		ipv4.SrcIP, ipv4.DstIP = selector.Source.Addr().AsSlice(), selector.Destination.Addr().AsSlice()
		network, serialNetwork = &ipv4, &ipv4
	} else {
		ethernet.EthernetType = layers.EthernetTypeIPv6
		var ipv6 layers.IPv6
		ipv6.Version, ipv6.HopLimit = 6, forwardPathInitialHopLimit
		ipv6.TrafficClass, ipv6.FlowLabel = selector.TrafficClass, selector.FlowLabel
		ipv6.NextHeader = layers.IPProtocolUDP
		ipv6.SrcIP, ipv6.DstIP = selector.Source.Addr().AsSlice(), selector.Destination.Addr().AsSlice()
		network, serialNetwork = &ipv6, &ipv6
	}
	var udp layers.UDP
	udp.SrcPort, udp.DstPort = layers.UDPPort(selector.Source.Port()), layers.UDPPort(selector.Destination.Port())
	if err := udp.SetNetworkLayerForChecksum(network); err != nil {
		t.Fatalf("set the forwarding packet checksum input: %v", err)
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		&ethernet, serialNetwork, &udp, gopacket.Payload(payload)); err != nil {
		t.Fatalf("encode the forwarding packet: %v", err)
	}
	return buffer.Bytes()
}
