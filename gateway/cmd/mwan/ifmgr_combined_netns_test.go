//go:build linux && firewallnetns

package main

import (
	"net/netip"
	"testing"
	"time"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
)

const (
	combinedTunnelRouterID = "198.51.100.2"
	combinedAttemptTable   = "combinedattempt"
)

type combinedRun struct {
	run      *bgpRuntimeRun
	network  string
	topology combinedTopology
	provider bgpRuntimeProvider
	daemon   *runtimeDaemon
	transit  *bgpRuntimeRouter
	tunnel   *bgpRuntimeRouter
	ipv4     string
}

func combinedDefault() netip.Prefix { return netip.MustParsePrefix("::/0") }

func combinedHome() netip.Prefix { return netip.MustParsePrefix(bgpRuntimeHome) }

func (r *bgpRuntimeRouter) withdrawAll(t *testing.T) {
	t.Helper()
	if err := r.server.DeletePath(apiutil.DeletePathRequest{DeleteAll: true}); err != nil {
		t.Fatalf("router withdraw: %v", err)
	}
}

func (c *combinedRun) paths() []string {
	return []string{combinedOrdinary, c.provider.device, tunnelRuntimeDevice}
}

func (c *combinedRun) requireIPv4PathsUnchanged(t *testing.T, since time.Time, devices ...string) {
	t.Helper()
	requireCombinedUnchanged(t, c.run.read, since, "ipv4", devices...)
}

func (c *combinedRun) requireIPv4Unchanged(t *testing.T, since time.Time) {
	t.Helper()
	c.requireIPv4PathsUnchanged(t, since, c.ipv4Providers()...)
	selection, err := tunnelRuntimeIPv4Selection()
	if err != nil || selection != c.ipv4 {
		t.Fatalf("IPv4 steering assignment = %q (%v), want the assignment before the fault %q", selection, err, c.ipv4)
	}
}

func (c *combinedRun) requireOrdinaryUnchanged(t *testing.T, since time.Time) {
	t.Helper()
	c.requireIPv4Unchanged(t, since)
	requireCombinedUnchanged(t, c.run.read, since, "ipv6", combinedOrdinary)
}

func (c *combinedRun) ipv4Providers() []string {
	return []string{tunnelRuntimeUnderlay, tunnelRuntimeAlternate, combinedOrdinary}
}

func TestCombinedPathsDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runCombinedPathsDaemonRuntime)
}

func runCombinedPathsDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	provider := combinedDirectProvider()
	combined := &combinedRun{run: run, topology: buildCombinedTopology(t, run, provider), provider: provider}
	combined.network = writeCombinedNetwork(t, run.networkDir, provider)
	started := time.Now()
	combined.daemon = run.start(t, "combined-first")
	waitRuntimeTable(t, combined.daemon, "inet", "filter", 10*time.Second)
	combinedStartsWithoutPeers(t, combined, started)
	combinedPeersStart(t, combined)
	combinedRouteWithdrawal(t, combined)
	combinedSessionLoss(t, combined)
	combinedTunnelLoss(t, combined)
	combinedUnderlayLoss(t, combined)
	combinedSinglePaths(t, combined)
	combinedOrdinaryFamilyLoss(t, combined)
	combinedRestart(t, combined)
}

func combinedStartsWithoutPeers(t *testing.T, c *combinedRun, started time.Time) {
	t.Helper()
	read := c.run.read
	for _, device := range c.ipv4Providers() {
		waitCombinedReady(t, c.daemon, read, device, "ipv4", started)
	}
	waitCombinedReady(t, c.daemon, read, combinedOrdinary, "ipv6", started)
	for _, device := range []string{c.provider.device, tunnelRuntimeDevice} {
		waitCombinedUnavailable(t, c.daemon, read, device, started, "bgp-session")
	}
	for _, device := range c.paths() {
		first := readCombinedPath(read, device, "ipv6").Transitions[0]
		if first.Current != combinedStarting || first.Previous != "" || first.Reason == "" {
			t.Fatalf("%s first ipv6 path transition = %+v, want starting with a reason", device, first)
		}
	}
	requireCombinedPaths(t, c.daemon, read, c.topology, combinedOrdinary)
}

func combinedPeersStart(t *testing.T, c *combinedRun) {
	t.Helper()
	read := c.run.read
	tunnelPeer := netip.MustParseAddr(tunnelRuntimeInnerRemote)
	waitCombinedConnectionAttempt(t, c)
	c.transit = c.run.startRouter(t, c.provider)
	accepting := map[string]time.Time{c.provider.device: time.Now()}
	c.transit.announce(t, combinedDefault(), c.provider.peer())
	c.tunnel = startBGPRuntimeRouter(t, c.topology.tunnel.endpoint, "far6in4", combinedTunnelRouterID, bgpRuntimeRemoteASN, tunnelPeer,
		netip.MustParseAddr(tunnelRuntimeInnerLocal), bgpRuntimeLocalASN)
	accepting[tunnelRuntimeDevice] = time.Now()
	c.tunnel.announce(t, combinedDefault(), tunnelPeer)
	for _, device := range []string{c.provider.device, tunnelRuntimeDevice} {
		measured := waitCombinedReady(t, c.daemon, read, device, "ipv6", accepting[device])
		requireCombinedBound(t, device+" ready after the peer accepted connections", measured, c.connectBound(t, device))
	}
	c.requireAnnounced(t)
	requireCombinedPaths(t, c.daemon, read, c.topology, c.paths()...)
	selection, err := tunnelRuntimeIPv4Selection()
	if err != nil {
		t.Fatal(err)
	}
	c.ipv4 = selection
}

func (c *combinedRun) requireAnnounced(t *testing.T) {
	t.Helper()
	waitBGPRuntimeReceived(t, c.daemon, c.run.read, c.transit, combinedHome())
	waitBGPRuntimeReceived(t, c.daemon, c.run.read, c.tunnel, combinedHome())
}

func combinedRestart(t *testing.T, c *combinedRun) {
	t.Helper()
	killOwnedRuntimeDaemon(t, c.daemon)
	waitBGPRuntimeReceived(t, nil, c.run.read, c.transit)
	waitBGPRuntimeReceived(t, nil, c.run.read, c.tunnel)
	routers := map[string]*bgpRuntimeRouter{c.provider.device: c.transit, tunnelRuntimeDevice: c.tunnel}
	started := time.Now()
	c.daemon = c.run.start(t, "combined-second")
	for _, device := range c.paths() {
		measured := waitCombinedReady(t, c.daemon, c.run.read, device, "ipv6", started)
		requireCombinedBound(t, device+" ready after the daemon restart", measured, c.recoverBound(t, device))
		first := readCombinedPath(c.run.read, device, "ipv6").Transitions[0]
		if first.Current != combinedStarting {
			t.Fatalf("%s first ipv6 path transition after the restart = %+v, want starting", device, first)
		}
	}
	c.requireAnnounced(t)
	for device, router := range routers {
		receipt, received := router.firstReceipt(started)
		if ready := started.Add(waitCombinedReady(t, c.daemon, c.run.read, device, "ipv6", started)); !received || receipt.Before(ready) {
			t.Fatalf("router behind %s first received a route at %s (received=%t), before the ipv6 path became ready at %s", device, receipt, received, ready)
		}
	}
	for _, device := range c.ipv4Providers() {
		waitCombinedReady(t, c.daemon, c.run.read, device, "ipv4", started)
	}
	c.requireAnnounced(t)
	requireCombinedPaths(t, c.daemon, c.run.read, c.topology, c.paths()...)
}
