//go:build linux && firewallnetns

package main

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	bgpRuntimeNoImportRule    = "no import rule covers the prefix"
	bgpRuntimeLengthBounds    = "prefix length 48 is outside the bounds 0 to 0 of import rule ::/0"
	bgpRuntimeNotEstablished  = "bgp session not established"
	bgpRuntimeNoAcceptedRoute = "bgp no accepted route"
	bgpRuntimeProbeFailed     = "probe failed"
)

func bgpRuntimeDirectProvider(localASN, remoteASN uint32, learned string) bgpRuntimeProvider {
	return bgpRuntimeProvider{
		id: "transit", device: "enbgp0", routerDevice: "rtr-a", network: "2001:db8:a", table: 200, mark: 2, tier: 0, metric: 300,
		localASN: localASN, remoteASN: remoteASN, learned: netip.MustParsePrefix(learned),
		exports: []bgpRuntimeExport{{prefix: bgpRuntimeHome, backupFor: ""}}, session: true,
	}
}

func TestBGPDirectDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, func(t *testing.T, run *bgpRuntimeRun) {
		runBGPDirectDaemonRuntime(t, run, bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, "::/0"))
	})
}

func TestBGPDirectIBGPDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, func(t *testing.T, run *bgpRuntimeRun) {
		runBGPDirectDaemonRuntime(t, run, bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeLocalASN, "2001:db8:a:99::/64"))
	})
}

func runBGPDirectDaemonRuntime(t *testing.T, run *bgpRuntimeRun, provider bgpRuntimeProvider) {
	run.topology = buildBGPRuntimeTopology(t, run.gateway, provider)
	writeBGPRuntimeNetwork(t, run.networkDir, provider)
	daemon := run.start(t, "bgp-first")
	waitRuntimeTable(t, daemon, "inet", "filter", 10*time.Second)
	bgpRuntimeBecomesReady(t, run, daemon, provider)
	requireBGPRuntimeExchange(t, run, daemon, provider)
	bgpRuntimePeerLoss(t, run, daemon, provider)
	bgpRuntimeForwardingLoss(t, run, daemon, provider)
	second := bgpRuntimeRestart(t, run, daemon, provider)
	bgpRuntimeSessionRemoval(t, run, second, provider)
}

func requireBGPRuntimeNoPermit(t *testing.T, provider bgpRuntimeProvider) {
	t.Helper()
	rules := tunnelRuntimeInputRules(t)
	for _, rule := range strings.Split(rules, "\n") {
		if strings.Contains(rule, provider.device) && strings.Contains(rule, "dport 179") {
			t.Fatalf("input chain has the BGP port permit %q on the provider interface", rule)
		}
	}
	if !strings.Contains(rules, "ct state established,related accept") {
		t.Fatalf("input chain = %s, want the established rule", rules)
	}
}

func bgpRuntimeBecomesReady(t *testing.T, run *bgpRuntimeRun, daemon *runtimeDaemon, provider bgpRuntimeProvider) {
	t.Helper()
	home := netip.MustParsePrefix(bgpRuntimeHome)
	waitBGPRuntimeSession(t, daemon, run.read, provider.device, "no established state", func(session bgpRuntimeSession) bool {
		return !session.Established
	})
	waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)

	since := time.Now()
	router := run.startRouter(t, provider)
	read := bgpRuntimeGuardedRead(t, run.read, provider.device, router)
	waitBGPRuntimeSession(t, daemon, read, provider.device, "an established state", func(session bgpRuntimeSession) bool {
		return session.Established && len(session.Accepted) == 0
	})
	waitBGPRuntimeReason(t, daemon, read, provider.device, since, bgpRuntimeNoAcceptedRoute)
	requireBGPRuntimeNoPermit(t, provider)

	setBGPRuntimeFault(t, run.topology, provider, true)
	since = time.Now()
	router.announce(t, provider.learned, provider.peer())
	router.announce(t, netip.MustParsePrefix(bgpRuntimeUndeclared), provider.peer())
	session := waitBGPRuntimeSession(t, daemon, read, provider.device, "one accepted and one rejected prefix", func(session bgpRuntimeSession) bool {
		return slices.Equal(session.Accepted, []string{provider.learned.String()}) && len(session.Rejections) == 1
	})
	wantReason := bgpRuntimeNoImportRule
	if provider.learned.Bits() == 0 {
		wantReason = bgpRuntimeLengthBounds
	}
	if rejection := session.Rejections[0]; rejection.Prefix != bgpRuntimeUndeclared || rejection.Reason != wantReason {
		t.Fatalf("published rejection = %+v, want %s with reason %q", rejection, bgpRuntimeUndeclared, wantReason)
	}
	waitBGPRuntimeGatewayRoute(t, daemon, read, provider.device, provider.learned, true)
	waitBGPRuntimeReason(t, daemon, read, provider.device, since, bgpRuntimeProbeFailed)
	requireBGPRuntimeFamily(t, read, provider.device, "not-ready", "not-ready")

	setBGPRuntimeFault(t, run.topology, provider, false)
	waitTunnelRuntimeFamily(t, daemon, read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	waitBGPRuntimeReceived(t, daemon, run.read, router, home)
	waitBGPRuntimeSession(t, daemon, run.read, provider.device, "the advertised home prefix", func(session bgpRuntimeSession) bool {
		return slices.Equal(session.Advertised, []string{home.String()})
	})
}
