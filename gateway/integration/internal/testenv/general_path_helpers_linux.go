//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"net/netip"
	"testing"
	"time"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
)

const (
	CombinedTunnelRouterID = "198.51.100.2"
	CombinedAttemptTable   = "combinedattempt"
)

type CombinedRun struct {
	Run      *BgpRuntimeRun
	Network  string
	Topology CombinedTopology
	Provider BgpRuntimeProvider
	Daemon   *RuntimeDaemon
	Transit  *BgpRuntimeRouter
	Tunnel   *BgpRuntimeRouter
	Ipv4     string
}

func CombinedDefault() netip.Prefix { return netip.MustParsePrefix("::/0") }

func CombinedHome() netip.Prefix { return netip.MustParsePrefix(BgpRuntimeHome) }

func (r *BgpRuntimeRouter) WithdrawAll(t *testing.T) {
	t.Helper()
	if err := r.Server.DeletePath(apiutil.DeletePathRequest{DeleteAll: true}); err != nil {
		t.Fatalf("router withdraw: %v", err)
	}
}

func (c *CombinedRun) Paths() []string {
	return []string{CombinedOrdinary, c.Provider.Device, TunnelRuntimeDevice}
}

func (c *CombinedRun) RequireIPv4PathsUnchanged(t *testing.T, since time.Time, devices ...string) {
	t.Helper()
	RequireCombinedUnchanged(t, c.Run.Read, since, "ipv4", devices...)
}

func (c *CombinedRun) RequireIPv4Unchanged(t *testing.T, since time.Time) {
	t.Helper()
	c.RequireIPv4PathsUnchanged(t, since, c.Ipv4Providers()...)
	selection, err := TunnelRuntimeIPv4Selection()
	if err != nil || selection != c.Ipv4 {
		t.Fatalf(
			"IPv4 steering assignment = %q (%v), want the assignment before the fault %q",
			selection,
			err,
			c.Ipv4,
		)
	}
}

func (c *CombinedRun) RequireOrdinaryUnchanged(t *testing.T, since time.Time) {
	t.Helper()
	c.RequireIPv4Unchanged(t, since)
	RequireCombinedUnchanged(t, c.Run.Read, since, "ipv6", CombinedOrdinary)
}

func (c *CombinedRun) Ipv4Providers() []string {
	return []string{TunnelRuntimeUnderlay, TunnelRuntimeAlternate, CombinedOrdinary}
}

func CombinedStartsWithoutPeers(t *testing.T, c *CombinedRun, started time.Time) {
	t.Helper()
	read := c.Run.Read
	for _, device := range c.Ipv4Providers() {
		WaitCombinedReady(t, c.Daemon, read, device, "ipv4", started)
	}
	WaitCombinedReady(t, c.Daemon, read, CombinedOrdinary, "ipv6", started)
	for _, device := range []string{c.Provider.Device, TunnelRuntimeDevice} {
		WaitCombinedUnavailable(t, c.Daemon, read, device, started, "bgp-session")
	}
	for _, device := range c.Paths() {
		first := ReadCombinedPath(read, device, "ipv6").Transitions[0]
		if first.Current != CombinedStarting || first.Previous != "" || first.Reason == "" {
			t.Fatalf(
				"%s first ipv6 path transition = %+v, want starting with a reason",
				device,
				first,
			)
		}
	}
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, CombinedOrdinary)
}

func CombinedPeersStart(t *testing.T, c *CombinedRun) {
	t.Helper()
	read := c.Run.Read
	tunnelPeer := netip.MustParseAddr(TunnelRuntimeInnerRemote)
	WaitCombinedConnectionAttempt(t, c)
	c.Transit = c.Run.StartRouter(t, c.Provider)
	accepting := map[string]time.Time{c.Provider.Device: time.Now()}
	c.Transit.Announce(t, CombinedDefault(), c.Provider.Peer())
	c.Tunnel = StartBGPRuntimeRouter(
		t,
		c.Topology.Tunnel.Endpoint,
		"far6in4",
		CombinedTunnelRouterID,
		BgpRuntimeRemoteASN,
		tunnelPeer,
		netip.MustParseAddr(TunnelRuntimeInnerLocal),
		BgpRuntimeLocalASN,
	)
	accepting[TunnelRuntimeDevice] = time.Now()
	c.Tunnel.Announce(t, CombinedDefault(), tunnelPeer)
	for _, device := range []string{c.Provider.Device, TunnelRuntimeDevice} {
		measured := WaitCombinedReady(t, c.Daemon, read, device, "ipv6", accepting[device])
		RequireCombinedBound(
			t,
			device+" ready after the peer accepted connections",
			measured,
			c.ConnectBound(t, device),
		)
	}
	c.RequireAnnounced(t)
	RequireCombinedPaths(t, c.Daemon, read, c.Topology, c.Paths()...)
	selection, err := TunnelRuntimeIPv4Selection()
	if err != nil {
		t.Fatal(err)
	}
	c.Ipv4 = selection
}

func (c *CombinedRun) RequireAnnounced(t *testing.T) {
	t.Helper()
	WaitBGPRuntimeReceived(t, c.Daemon, c.Run.Read, c.Transit, CombinedHome())
	WaitBGPRuntimeReceived(t, c.Daemon, c.Run.Read, c.Tunnel, CombinedHome())
}

func CombinedRestart(t *testing.T, c *CombinedRun) {
	t.Helper()
	KillOwnedRuntimeDaemon(t, c.Daemon)
	WaitBGPRuntimeReceived(t, nil, c.Run.Read, c.Transit)
	WaitBGPRuntimeReceived(t, nil, c.Run.Read, c.Tunnel)
	routers := map[string]*BgpRuntimeRouter{
		c.Provider.Device:   c.Transit,
		TunnelRuntimeDevice: c.Tunnel,
	}
	started := time.Now()
	c.Daemon = c.Run.Start(t, "combined-second")
	for _, device := range c.Paths() {
		measured := WaitCombinedReady(t, c.Daemon, c.Run.Read, device, "ipv6", started)
		RequireCombinedBound(
			t,
			device+" ready after the daemon restart",
			measured,
			c.RecoverBound(t, device),
		)
		first := ReadCombinedPath(c.Run.Read, device, "ipv6").Transitions[0]
		if first.Current != CombinedStarting {
			t.Fatalf(
				"%s first ipv6 path transition after the restart = %+v, want starting",
				device,
				first,
			)
		}
	}
	c.RequireAnnounced(t)
	for device, router := range routers {
		// Use wall time for readiness and receipt. The two processes do not share Go's monotonic
		// clock reading.
		receipt, received := router.FirstReceipt(started)
		receipt = receipt.Round(0)
		if ready := started.Round(0).Add(
			WaitCombinedReady(t, c.Daemon, c.Run.Read, device, "ipv6", started),
		); !received ||
			receipt.Before(ready) {
			t.Fatalf(
				"router behind %s first received a route at %s (received=%t), before the ipv6 path became ready at %s",
				device,
				receipt,
				received,
				ready,
			)
		}
	}
	for _, device := range c.Ipv4Providers() {
		WaitCombinedReady(t, c.Daemon, c.Run.Read, device, "ipv4", started)
	}
	c.RequireAnnounced(t)
	RequireCombinedPaths(t, c.Daemon, c.Run.Read, c.Topology, c.Paths()...)
}
