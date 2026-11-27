//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"
)

func RequireTunnelRuntimeDialFails(
	t *testing.T,
	topology TunnelRuntimeTopology,
	network string,
	destination string,
	source string,
	phase string,
) {
	t.Helper()
	SetRuntimeNamespace(t, topology.Client)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	connection, err := dialer.Dial(network, destination)
	if connection != nil {
		_ = connection.Close()
	}
	SetRuntimeNamespace(t, topology.Gateway)
	if err == nil {
		t.Fatalf("%s connection to %s succeeded %s", network, destination, phase)
	}
}

func RequireTunnelRuntimeExchange(
	t *testing.T,
	daemon *RuntimeDaemon,
	topology TunnelRuntimeTopology,
	port int,
	window time.Duration,
) {
	t.Helper()
	client, remoteClient := netip.MustParseAddr(TunnelRuntimeClientV6), netip.MustParseAddr(
		TunnelRuntimeRemoteClient,
	)
	outbound := WaitTunnelRuntimeTransfer(
		t,
		daemon,
		topology,
		topology.Remote,
		topology.Client,
		"tcp6",
		net.JoinHostPort(TunnelRuntimeRemoteClient, fmt.Sprint(port)),
		net.JoinHostPort(TunnelRuntimeClientV6, "0"),
		window,
	)
	if outbound != client {
		t.Fatalf(
			"the remote client observed source %s, want the unchanged client address %s",
			outbound,
			client,
		)
	}
	inbound := WaitTunnelRuntimeTransfer(
		t,
		daemon,
		topology,
		topology.Client,
		topology.Remote,
		"tcp6",
		net.JoinHostPort(TunnelRuntimeClientV6, fmt.Sprint(port+1)),
		net.JoinHostPort(TunnelRuntimeRemoteClient, "0"),
		window,
	)
	if inbound != remoteClient {
		t.Fatalf("the internal client observed source %s, want %s", inbound, remoteClient)
	}
}

func RequireTunnelRuntimeNotForwarded(
	t *testing.T,
	topology TunnelRuntimeTopology,
	port int,
	phase string,
) {
	t.Helper()
	capture := OpenTunnelRuntimeCapture(t, topology)
	RequireTunnelRuntimeDialFails(
		t,
		topology,
		"tcp6",
		net.JoinHostPort(TunnelRuntimeRemoteClient, fmt.Sprint(port)),
		net.JoinHostPort(TunnelRuntimeClientV6, "0"),
		phase,
	)
	client := netip.MustParseAddr(TunnelRuntimeClientV6)
	for _, frame := range ReadTunnelRuntimeCapture(t, capture) {
		if frame.Protocol == TunnelRuntimeProtocol41 && frame.InnerSource == client {
			t.Fatalf("the gateway forwarded a client IPv6 packet into the tunnel %s", phase)
		}
	}
}

func RequireTunnelRuntimeEncapsulation(t *testing.T, frames []TunnelRuntimeFrame) {
	t.Helper()
	local, remote := netip.MustParseAddr(TunnelRuntimeLocal), netip.MustParseAddr(
		TunnelRuntimeRemote,
	)
	client, remoteClient := netip.MustParseAddr(TunnelRuntimeClientV6), netip.MustParseAddr(
		TunnelRuntimeRemoteClient,
	)
	outbound, inbound := 0, 0
	for _, frame := range frames {
		if frame.EtherType == TunnelRuntimeEtherIPv6 {
			t.Fatalf("the ISP link transported an IPv6 frame of %d bytes", frame.Length)
		}
		if frame.EtherType != TunnelRuntimeEtherIPv4 || frame.Protocol != TunnelRuntimeProtocol41 {
			continue
		}
		switch {
		case frame.Source == local && frame.Destination == remote:
			if frame.InnerSource == client && frame.InnerDestination == remoteClient {
				outbound += frame.Length
			}
		case frame.Source == remote && frame.Destination == local:
			if frame.InnerSource == remoteClient && frame.InnerDestination == client {
				inbound += frame.Length
			}
		default:
			t.Fatalf(
				"protocol 41 frame between %s and %s, want the endpoints %s and %s",
				frame.Source,
				frame.Destination,
				local,
				remote,
			)
		}
	}
	if outbound < 3*TunnelRuntimeTransferSize || inbound < 3*TunnelRuntimeTransferSize {
		t.Fatalf(
			"protocol 41 bytes between the clients: outbound=%d inbound=%d, want at least %d each",
			outbound,
			inbound,
			3*TunnelRuntimeTransferSize,
		)
	}
}
