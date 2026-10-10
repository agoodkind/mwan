//go:build linux && firewallnetns

package main

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const bgpRuntimeOutsidePort = 18182

func TestBGPSpecificPrefixDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runBGPSpecificPrefixDaemonRuntime)
}

func runBGPSpecificPrefixDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	defaultRoute := netip.MustParsePrefix("::/0")
	specific := bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeLocalASN, "2001:db8:a:99::/64")
	other := bgpRuntimeProvider{
		id: "other", device: "enbgp1", routerDevice: "rtr-b", network: "2001:db8:c", table: 300, mark: 3, tier: 1, metric: 310,
		localASN: bgpRuntimeLocalASN, remoteASN: bgpRuntimeRemoteASN, learned: defaultRoute,
		exports: []bgpRuntimeExport{{prefix: bgpRuntimeHome, backupFor: ""}}, session: true,
	}
	run.topology = buildBGPRuntimeTopology(t, run.gateway, specific, other)
	writeBGPRuntimeNetwork(t, run.networkDir, specific, other)
	daemon := run.start(t, "bgp-specific")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	providers := []bgpRuntimeProvider{specific, other}
	for _, provider := range providers {
		run.startRouter(t, provider).announce(t, provider.learned, provider.peer())
	}
	for _, provider := range providers {
		waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	}
	waitBGPRuntimeReceived(t, daemon, run.read, run.routers[other.id], netip.MustParsePrefix(bgpRuntimeHome))
	requireBGPRuntimeExchange(t, run, daemon, specific)

	setRuntimeNamespace(t, run.topology.remotes[other.id])
	destination := net.JoinHostPort(other.remoteClient(), strconv.Itoa(bgpRuntimeOutsidePort))
	listener, err := net.Listen("tcp6", destination)
	if err != nil {
		t.Fatalf("listen on %s: %v", destination, err)
	}
	defer func() { _ = listener.Close() }()
	setRuntimeNamespace(t, run.topology.client)
	local, err := net.ResolveTCPAddr("tcp6", net.JoinHostPort(tunnelRuntimeClientV6, "0"))
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: tunnelRuntimeFailureWindow}
	connection, err := dialer.Dial("tcp6", destination)
	setRuntimeNamespace(t, run.gateway)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("the internal client connected to %s through another provider", destination)
	}
	if !errors.Is(err, syscall.ENETUNREACH) && !errors.Is(err, syscall.EHOSTUNREACH) {
		t.Fatalf("dial %s: %v, want an ICMPv6 unreachable error", destination, err)
	}
	if tcpListener, isTCP := listener.(*net.TCPListener); isTCP {
		_ = tcpListener.SetDeadline(time.Now().Add(time.Second))
	}
	if accepted, acceptErr := listener.Accept(); acceptErr == nil {
		_ = accepted.Close()
		t.Fatalf("a connection arrived at %s through another provider", destination)
	}
}
