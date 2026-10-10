package bgp_test

import (
	"context"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/mwan/internal/bgp"
)

func TestSessionsWithSameASNsReportIndependentState(t *testing.T) {
	loopback := netip.IPv6Loopback()
	firstLearned := netip.MustParsePrefix("2001:db8:100::/48")
	secondLearned := netip.MustParsePrefix("2001:db8:200::/48")
	exported := netip.MustParsePrefix("3fff:a::/48")
	hop := netip.MustParseAddr("2001:db8:ffff::2")
	importRules := []bgp.ImportRule{
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48},
	}
	exportRules := []bgp.ExportRule{{Prefix: exported, Mode: bgp.ExportAlways, NextHop: hop}}

	firstPeer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	secondPeer := startTestPeer(t, externalPeerASN, "192.0.2.3", loopback, loopback, sessionASN)
	firstConfig := baseSessionConfig("first", externalPeerASN, firstPeer)
	firstConfig.Import = importRules
	firstConfig.Export = exportRules
	secondConfig := baseSessionConfig("second", externalPeerASN, secondPeer)
	secondConfig.Import = importRules
	secondConfig.Export = exportRules
	first := startSession(t, firstConfig)
	second := startSession(t, secondConfig)
	waitEstablished(t, first)
	waitEstablished(t, second)

	firstPeer.announce(t, firstLearned, hop)
	secondPeer.announce(t, secondLearned, hop)
	eventually(t, "each session to accept its own peer's prefix", func() bool {
		return slices.Equal(first.State().Accepted, []netip.Prefix{firstLearned}) &&
			slices.Equal(second.State().Accepted, []netip.Prefix{secondLearned})
	})

	if err := first.SetAdvertisement(true, nil); err != nil {
		t.Fatalf("first SetAdvertisement: %v", err)
	}
	waitReceived(t, firstPeer, exported)
	if got := secondPeer.receivedPrefixes(t); len(got) != 0 {
		t.Errorf("second peer received %v from a session that advertises no prefix", got)
	}
	if got := second.State().Advertised; len(got) != 0 {
		t.Errorf("second session advertised prefixes = %v, want none", got)
	}
	if got, want := first.State().Name, "first"; got != want {
		t.Errorf("first session name = %q, want %q", got, want)
	}

	firstPeer.server.Stop()
	eventually(t, "the first session to lose its peer", func() bool {
		return !first.State().Established
	})
	secondState := second.State()
	if !secondState.Established {
		t.Error("second session lost its peer when the first session's peer stopped")
	}
	if got, want := secondState.Accepted, []netip.Prefix{secondLearned}; !slices.Equal(got, want) {
		t.Errorf("second session accepted prefixes = %v, want %v", got, want)
	}
}

func TestSessionOutlivesStartContext(t *testing.T) {
	loopback := netip.IPv6Loopback()
	learned := netip.MustParsePrefix("2001:db8:100::/48")
	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	cfg := baseSessionConfig("start-context", externalPeerASN, peer)
	cfg.Import = []bgp.ImportRule{
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48},
	}
	session, err := bgp.NewSession(cfg, sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	startContext, cancelStart := context.WithCancel(t.Context())
	if err := session.Start(startContext); err != nil {
		t.Fatalf("start session: %v", err)
	}
	cancelStart()
	t.Cleanup(func() {
		if err := session.Stop(t.Context()); err != nil {
			t.Errorf("stop session: %v", err)
		}
	})

	waitEstablished(t, session)
	peer.announce(t, learned, netip.MustParseAddr("2001:db8:ffff::2"))
	eventually(t, "the session to accept a prefix after the Start context ended", func() bool {
		return slices.Equal(session.State().Accepted, []netip.Prefix{learned})
	})
}

// The test verifies that Stop does not run a second delivery while the test
// blocks one delivery. The OnChange contract excludes listeners that block.
func TestSessionDeliversChangesOneAtATimeInOrder(t *testing.T) {
	loopback := netip.IPv6Loopback()
	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	session, err := bgp.NewSession(baseSessionConfig("ordered", externalPeerASN, peer), sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var active atomic.Int32
	var overlapped atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	delivered := make(chan bgp.SessionState, 8)
	session.OnChange(func(state bgp.SessionState) {
		if active.Add(1) > 1 {
			overlapped.Store(true)
		}
		if state.Established {
			entered <- struct{}{}
			<-release
		}
		delivered <- state
		active.Add(-1)
	})
	if err := session.Start(t.Context()); err != nil {
		t.Fatalf("start session: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the established change")
	}
	if err := session.Stop(t.Context()); err != nil {
		t.Fatalf("stop session: %v", err)
	}
	close(release)

	wantEstablished := []bool{true, false}
	for index, want := range wantEstablished {
		select {
		case state := <-delivered:
			if state.Established != want {
				t.Fatalf("delivered state %d established = %t, want %t", index, state.Established, want)
			}
		case <-time.After(waitTimeout):
			t.Fatalf("timed out waiting for delivered state %d", index)
		}
	}
	if overlapped.Load() {
		t.Error("two change deliveries ran at the same time")
	}
}
