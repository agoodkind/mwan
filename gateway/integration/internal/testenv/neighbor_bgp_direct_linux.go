//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	BgpRuntimeNoImportRule    = "no import rule covers the prefix"
	BgpRuntimeLengthBounds    = "prefix length 48 is outside the bounds 0 to 0 of import rule ::/0"
	BgpRuntimeNotEstablished  = "bgp session not established"
	BgpRuntimeNoAcceptedRoute = "bgp no accepted route"
	BgpRuntimeProbeFailed     = "probe failed"
)

func BgpRuntimeDirectProvider(localASN, remoteASN uint32, learned string) BgpRuntimeProvider {
	return BgpRuntimeProvider{
		Id:           "transit",
		Device:       "enbgp0",
		RouterDevice: "rtr-a",
		Network:      "2001:db8:a",
		Table:        200,
		Mark:         2,
		Tier:         0,
		Metric:       300,
		LocalASN:     localASN,
		RemoteASN:    remoteASN,
		Learned:      netip.MustParsePrefix(learned),
		Exports:      []BgpRuntimeExport{{Prefix: BgpRuntimeHome, BackupFor: ""}},
		Session:      true,
	}
}

func RequireBGPRuntimeNoPermit(t *testing.T, provider BgpRuntimeProvider) {
	t.Helper()
	rules := TunnelRuntimeInputRules(t)
	for _, rule := range strings.Split(rules, "\n") {
		if strings.Contains(rule, provider.Device) && strings.Contains(rule, "dport 179") {
			t.Fatalf("input chain has the BGP port permit %q on the provider interface", rule)
		}
	}
	if !strings.Contains(rules, "ct state established,related accept") {
		t.Fatalf("input chain = %s, want the established rule", rules)
	}
}

func BgpRuntimeBecomesReady(
	t *testing.T,
	run *BgpRuntimeRun,
	daemon *RuntimeDaemon,
	provider BgpRuntimeProvider,
) {
	t.Helper()
	home := netip.MustParsePrefix(BgpRuntimeHome)
	WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		provider.Device,
		"no established state",
		func(session BgpRuntimeSession) bool {
			return !session.Established
		},
	)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		run.Read,
		provider.Device,
		"ipv6",
		"not-ready",
		"not-ready",
		TunnelRuntimeRecoverWindow,
	)

	since := time.Now()
	router := run.StartRouter(t, provider)
	read := BgpRuntimeGuardedRead(t, run.Read, provider.Device, router)
	WaitBGPRuntimeSession(
		t,
		daemon,
		read,
		provider.Device,
		"an established state",
		func(session BgpRuntimeSession) bool {
			return session.Established && len(session.Accepted) == 0
		},
	)
	WaitBGPRuntimeReason(t, daemon, read, provider.Device, since, BgpRuntimeNoAcceptedRoute)
	RequireBGPRuntimeNoPermit(t, provider)

	SetBGPRuntimeFault(t, run.Topology, provider, true)
	since = time.Now()
	router.Announce(t, provider.Learned, provider.Peer())
	router.Announce(t, netip.MustParsePrefix(BgpRuntimeUndeclared), provider.Peer())
	session := WaitBGPRuntimeSession(
		t,
		daemon,
		read,
		provider.Device,
		"one accepted and one rejected prefix",
		func(session BgpRuntimeSession) bool {
			return slices.Equal(session.Accepted, []string{provider.Learned.String()}) &&
				len(session.Rejections) == 1
		},
	)
	wantReason := BgpRuntimeNoImportRule
	if provider.Learned.Bits() == 0 {
		wantReason = BgpRuntimeLengthBounds
	}
	if rejection := session.Rejections[0]; rejection.Prefix != BgpRuntimeUndeclared ||
		rejection.Reason != wantReason {
		t.Fatalf(
			"published rejection = %+v, want %s with reason %q",
			rejection,
			BgpRuntimeUndeclared,
			wantReason,
		)
	}
	WaitBGPRuntimeGatewayRoute(t, daemon, read, provider.Device, provider.Learned, true)
	WaitBGPRuntimeReason(t, daemon, read, provider.Device, since, BgpRuntimeProbeFailed)
	RequireBGPRuntimeFamily(t, read, provider.Device, "not-ready", "not-ready")

	SetBGPRuntimeFault(t, run.Topology, provider, false)
	WaitTunnelRuntimeFamily(
		t,
		daemon,
		read,
		provider.Device,
		"ipv6",
		"ready",
		"ready",
		TunnelRuntimeRecoverWindow,
	)
	WaitBGPRuntimeReceived(t, daemon, run.Read, router, home)
	WaitBGPRuntimeSession(
		t,
		daemon,
		run.Read,
		provider.Device,
		"the advertised home prefix",
		func(session BgpRuntimeSession) bool {
			return slices.Equal(session.Advertised, []string{home.String()})
		},
	)
}
