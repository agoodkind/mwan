//go:build linux && firewallnetns

package main

import (
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"
)

func requireTunnelRuntimeDialFails(t *testing.T, topology tunnelRuntimeTopology, network, destination, source, phase string) {
	t.Helper()
	setRuntimeNamespace(t, topology.client)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	connection, err := dialer.Dial(network, destination)
	if connection != nil {
		_ = connection.Close()
	}
	setRuntimeNamespace(t, topology.gateway)
	if err == nil {
		t.Fatalf("%s connection to %s succeeded %s", network, destination, phase)
	}
}

func requireTunnelRuntimeExchange(t *testing.T, daemon *runtimeDaemon, topology tunnelRuntimeTopology, port int, window time.Duration) {
	t.Helper()
	client, remoteClient := netip.MustParseAddr(tunnelRuntimeClientV6), netip.MustParseAddr(tunnelRuntimeRemoteClient)
	outbound := waitTunnelRuntimeTransfer(t, daemon, topology, topology.remote, topology.client, "tcp6",
		net.JoinHostPort(tunnelRuntimeRemoteClient, fmt.Sprint(port)), net.JoinHostPort(tunnelRuntimeClientV6, "0"), window)
	if outbound != client {
		t.Fatalf("the remote client observed source %s, want the unchanged client address %s", outbound, client)
	}
	inbound := waitTunnelRuntimeTransfer(t, daemon, topology, topology.client, topology.remote, "tcp6",
		net.JoinHostPort(tunnelRuntimeClientV6, fmt.Sprint(port+1)), net.JoinHostPort(tunnelRuntimeRemoteClient, "0"), window)
	if inbound != remoteClient {
		t.Fatalf("the internal client observed source %s, want %s", inbound, remoteClient)
	}
}

func requireTunnelRuntimeNotForwarded(t *testing.T, topology tunnelRuntimeTopology, port int, phase string) {
	t.Helper()
	capture := openTunnelRuntimeCapture(t, topology)
	requireTunnelRuntimeDialFails(t, topology, "tcp6", net.JoinHostPort(tunnelRuntimeRemoteClient, fmt.Sprint(port)),
		net.JoinHostPort(tunnelRuntimeClientV6, "0"), phase)
	client := netip.MustParseAddr(tunnelRuntimeClientV6)
	for _, frame := range readTunnelRuntimeCapture(t, capture) {
		if frame.protocol == tunnelRuntimeProtocol41 && frame.innerSource == client {
			t.Fatalf("the gateway forwarded a client IPv6 packet into the tunnel %s", phase)
		}
	}
}

func requireTunnelRuntimeEncapsulation(t *testing.T, frames []tunnelRuntimeFrame) {
	t.Helper()
	local, remote := netip.MustParseAddr(tunnelRuntimeLocal), netip.MustParseAddr(tunnelRuntimeRemote)
	client, remoteClient := netip.MustParseAddr(tunnelRuntimeClientV6), netip.MustParseAddr(tunnelRuntimeRemoteClient)
	outbound, inbound := 0, 0
	for _, frame := range frames {
		if frame.etherType == tunnelRuntimeEtherIPv6 {
			t.Fatalf("the ISP link transported an IPv6 frame of %d bytes", frame.length)
		}
		if frame.etherType != tunnelRuntimeEtherIPv4 || frame.protocol != tunnelRuntimeProtocol41 {
			continue
		}
		switch {
		case frame.source == local && frame.destination == remote:
			if frame.innerSource == client && frame.innerDestination == remoteClient {
				outbound += frame.length
			}
		case frame.source == remote && frame.destination == local:
			if frame.innerSource == remoteClient && frame.innerDestination == client {
				inbound += frame.length
			}
		default:
			t.Fatalf("protocol 41 frame between %s and %s, want the endpoints %s and %s", frame.source, frame.destination, local, remote)
		}
	}
	if outbound < 3*tunnelRuntimeTransferSize || inbound < 3*tunnelRuntimeTransferSize {
		t.Fatalf("protocol 41 bytes between the clients: outbound=%d inbound=%d, want at least %d each", outbound, inbound, 3*tunnelRuntimeTransferSize)
	}
}
