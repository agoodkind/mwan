package bgp_test

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"

	"goodkind.io/mwan/internal/bgp"
)

func TestSessionImportAcceptsOnlyDeclaredPrefixes(t *testing.T) {
	loopback := netip.IPv6Loopback()
	allowed := netip.MustParsePrefix("2001:db8:100::/48")
	tooLong := netip.MustParsePrefix("2001:db8:200::/56")
	undeclared := netip.MustParsePrefix("3fff:100::/32")
	peerNextHop := netip.MustParseAddr("2001:db8:ffff::2")

	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	cfg := baseSessionConfig("import", externalPeerASN, peer)
	cfg.Import = []bgp.ImportRule{
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48},
	}
	session, err := bgp.NewSession(cfg, sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	changes := make(chan bgp.SessionState, 64)
	session.OnChange(func(state bgp.SessionState) {
		changes <- state
	})
	waitChange := func(description string, condition func(bgp.SessionState) bool) bgp.SessionState {
		t.Helper()
		timeout := time.After(waitTimeout)
		for {
			select {
			case state := <-changes:
				if condition(state) {
					return state
				}
			case <-timeout:
				t.Fatalf("timed out waiting for a change signal with %s", description)
			}
		}
	}
	if err := session.Start(t.Context()); err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Stop(); err != nil {
			t.Errorf("stop session: %v", err)
		}
	})

	established := waitChange("an established session", func(state bgp.SessionState) bool {
		return state.Established
	})
	if established.UpSince.IsZero() {
		t.Error("established state has a zero uptime start")
	}
	if got, want := established.FSMState, bgppkt.BGP_FSM_ESTABLISHED.String(); got != want {
		t.Errorf("established FSM state = %q, want %q", got, want)
	}

	peer.announce(t, allowed, peerNextHop)
	peer.announce(t, tooLong, peerNextHop)
	peer.announce(t, undeclared, peerNextHop)
	state := waitChange("one accepted and two rejected prefixes", func(state bgp.SessionState) bool {
		return len(state.Accepted) == 1 && len(state.Rejected) == 2
	})
	if got, want := state.Accepted, []netip.Prefix{allowed}; !slices.Equal(got, want) {
		t.Errorf("accepted prefixes = %v, want %v", got, want)
	}
	reasons := make(map[netip.Prefix]string, len(state.Rejected))
	for _, rejection := range state.Rejected {
		reasons[rejection.Prefix] = rejection.Reason
	}
	if got, want := reasons[undeclared], "no import rule covers the prefix"; got != want {
		t.Errorf("undeclared prefix rejection reason = %q, want %q", got, want)
	}
	if got, want := reasons[tooLong], "prefix length 56 is outside the bounds"; !strings.Contains(got, want) {
		t.Errorf("over-long prefix rejection reason = %q, want a reason containing %q", got, want)
	}

	peer.withdraw(t, allowed)
	waitChange("no accepted prefix after the peer withdrawal", func(state bgp.SessionState) bool {
		return len(state.Accepted) == 0
	})

	peer.announce(t, allowed, peerNextHop)
	waitChange("the prefix accepted again", func(state bgp.SessionState) bool {
		return slices.Equal(state.Accepted, []netip.Prefix{allowed})
	})
	peer.server.Stop()
	lost := waitChange("a lost session", func(state bgp.SessionState) bool {
		return !state.Established
	})
	if len(lost.Accepted) != 0 {
		t.Errorf("accepted prefixes after peer loss = %v, want none", lost.Accepted)
	}
	if !lost.UpSince.IsZero() {
		t.Errorf("uptime start after peer loss = %s, want the zero time", lost.UpSince)
	}
}

func TestSessionRejectsLearnedPrefixThatTheSessionExports(t *testing.T) {
	loopback := netip.IPv6Loopback()
	exported := netip.MustParsePrefix("2001:db8:a::/48")
	other := netip.MustParsePrefix("2001:db8:100::/48")
	hop := netip.MustParseAddr("2001:db8:ffff::2")
	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", loopback, loopback, sessionASN)
	cfg := baseSessionConfig("own-export", externalPeerASN, peer)
	cfg.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("::/0"), MinLength: 0, MaxLength: 48}}
	cfg.Export = []bgp.ExportRule{{Prefix: exported, Mode: bgp.ExportAlways, NextHop: hop}}
	session := startSession(t, cfg)
	waitEstablished(t, session)

	peer.announce(t, exported, hop)
	peer.announce(t, other, hop)
	eventually(t, "one accepted prefix and one rejected prefix", func() bool {
		state := session.State()
		return len(state.Accepted) == 1 && len(state.Rejected) == 1
	})
	state := session.State()
	if got, want := state.Accepted, []netip.Prefix{other}; !slices.Equal(got, want) {
		t.Errorf("accepted prefixes = %v, want %v", got, want)
	}
	wantRejected := []bgp.Rejection{{Prefix: exported, Reason: "the session exports the prefix"}}
	if got := state.Rejected; !slices.Equal(got, wantRejected) {
		t.Errorf("rejected prefixes = %v, want %v", got, wantRejected)
	}

	peer.withdraw(t, exported)
	peer.withdraw(t, other)
	eventually(t, "no accepted prefix after the peer withdrawals", func() bool {
		return len(session.State().Accepted) == 0
	})
}
