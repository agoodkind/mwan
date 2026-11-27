//go:build cli

package commandline

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gateway/gen/mwan/v1"
	"goodkind.io/mwan/gateway/internal/wanconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
	"goodkind.io/mwan/gateway/internal/yangpub"
)

// Use two translating WAN members and one member without translation.
func liveTestGateway() wanconfig.Gateway {
	return wanconfig.Gateway{
		InternalIface: "eninternal0",
		Members: []wanconfig.Member{
			{
				TranslationIDV6: wanconfig.TranslationInstanceID("att", "ipv6"),
				Name:            "att",
				Iface:           "enatt0",
				Tier:            0,
				ProbePolicy:     "att",
				TranslationV6:   testNPTPolicy("2001:db8:a::/60"),
			},
			{Name: "monkeybrains", Iface: "enmbrains0", Tier: 1, ProbePolicy: "monkeybrains"},
			{
				TranslationIDV6: wanconfig.TranslationInstanceID("webpass", "ipv6"),
				Name:            "webpass",
				Iface:           "enwebpass0",
				Tier:            0,
				ProbePolicy:     "webpass",
				TranslationV6:   testNPTPolicy("2001:db8:b::/60"),
			},
		},
	}
}

func TestInterfacesLiveItems_ServesTheSnapshot(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	transition := time.Date(2026, 8, 20, 3, 4, 5, 0, time.UTC)
	store.SetHealth(map[string]wanstate.MemberHealth{
		"att": {
			Verdict:             wanstate.HealthHealthy,
			ConsecutiveFailures: 0,
			LastTransition:      transition,
			V4:                  wanstate.ProbePass,
			V6:                  wanstate.ProbePass,
		},
	})
	store.SetRouting(
		0,
		map[string]wanstate.MemberRouting{
			"att": {Carrying: true},
		},
	)
	store.SetTranslation(map[string]wanstate.MemberTranslation{
		"att": {
			V6: wanstate.FamilyTranslation{
				ExternalPrefix: netip.MustParsePrefix("2001:db8:a::/60"),
				Ready:          true,
			},
		},
	})
	now := time.Date(2026, 8, 20, 3, 4, 6, 0, time.UTC)
	store.SetBGP(wanstate.BGP{
		Peers:         []wanstate.BGPPeer{{Address: "3d06:bad:b01:201::2", Established: true}},
		ReadAt:        now,
		PollSucceeded: true,
	})
	store.SetIntendedRuleset("chain prerouting:\nchain postrouting:\n")
	store.SetOwnedIntendedRuleset(
		"firewall",
		"inet filter input:\n",
		errors.New("chain input was not installed"),
	)

	items := interfacesLiveItems(store.Snapshot(), liveTestGateway(), nil, now)

	attBase := "/ietf-interfaces:interfaces/interface[name='enatt0']/goodkind-mwan-steering:steering/state"
	peerBase := "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/bgp-peer[address='3d06:bad:b01:201::2']"
	want := map[string]string{
		attBase + "/health":                           "healthy",
		attBase + "/consecutive-failures":             "0",
		attBase + "/probe[family='ipv4']/last-result": "pass",
		attBase + "/probe[family='ipv6']/last-result": "pass",
		attBase + "/last-transition":                  "2026-08-20T03:04:05Z",
		attBase + "/carrying":                         "true",
		"/ietf-interfaces:interfaces/interface[name='enatt0']/ietf-ip:ipv6/goodkind-mwan-steering:translation/state/resolved-external-prefix": "2001:db8:a::/60",
		"/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/active-tier":                                                 "0",
		"/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/intended-ruleset":                                            "firewall:\ninet filter input:\nerror: chain input was not installed\nnpt:\nchain prerouting:\nchain postrouting:\n",
		peerBase + "/established": "true",
		peerBase + "/stale":       "false",
	}
	served := map[string]string{}

	for _, item := range items {
		served[item.Path] = item.Value
	}

	for path, value := range want {
		if served[path] != value {
			t.Fatalf("path %s = %q, want %q (all: %v)", path, served[path], value, served)
		}
	}

	// Omit fields for members without a state record.
	for _, item := range items {
		if item.Path == "/ietf-interfaces:interfaces/interface[name='enwebpass0']/goodkind-mwan-steering:steering/state/health" {
			t.Fatalf("webpass has no snapshot but was served: %v", item)
		}
	}
}

func TestInterfacesLiveItems_ServesOwnedAddresses(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	store.SetRouting(
		0,
		map[string]wanstate.MemberRouting{
			"att": {Carrying: true, OwnedAddresses: nil},
			"webpass": {
				Carrying: true,
				OwnedAddresses: []netip.Addr{
					netip.MustParseAddr("203.0.113.3"),
					netip.MustParseAddr("203.0.113.4"),
				},
			},
		},
	)

	items := interfacesLiveItems(store.Snapshot(), liveTestGateway(), nil, time.Now())

	served := map[string][]string{}

	for _, item := range items {
		served[item.Path] = append(served[item.Path], item.Value)
	}

	webpassWAN := "/ietf-interfaces:interfaces/interface[name='enwebpass0']/goodkind-mwan-steering:wan"
	if got := served[webpassWAN+"/owned-address"]; len(got) != 2 || got[0] != "203.0.113.3" ||
		got[1] != "203.0.113.4" {
		t.Fatalf("webpass owned-address = %v, want [203.0.113.3 203.0.113.4]", got)
	}

	if got := served[webpassWAN+"/name"]; len(got) != 1 || got[0] != "webpass" {
		t.Fatalf("webpass wan name = %v, want [webpass]", got)
	}

	attWAN := "/ietf-interfaces:interfaces/interface[name='enatt0']/goodkind-mwan-steering:wan"

	for path := range served {
		if len(path) >= len(attWAN) && path[:len(attWAN)] == attWAN {
			t.Fatalf("att owns no address but served %s", path)
		}
	}
}

func TestInterfacesLiveItems_MarksAStaleAgentAnswer(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	now := time.Date(2026, 8, 20, 3, 4, 5, 0, time.UTC)
	store.SetBGP(wanstate.BGP{
		Peers:         []wanstate.BGPPeer{{Address: "3d06:bad:b01:201::2", Established: true}},
		ReadAt:        now.Add(-time.Hour),
		PollSucceeded: true,
	})
	items := interfacesLiveItems(store.Snapshot(), liveTestGateway(), nil, now)
	stalePath := "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/bgp-peer[address='3d06:bad:b01:201::2']/stale"

	for _, item := range items {
		if item.Path == stalePath {
			if item.Value != "true" {
				t.Fatalf("stale = %q, want true", item.Value)
			}

			return
		}
	}

	t.Fatalf("stale leaf not served: %v", items)
}

func TestNatLiveItems_NumbersInstancesLikeTheConfigPublish(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	store.SetTranslation(map[string]wanstate.MemberTranslation{
		"att": {
			V6: wanstate.FamilyTranslation{
				ExternalPrefix: netip.MustParsePrefix("2001:db8:a::/60"),
				Ready:          true,
			},
		},
		"webpass": {V6: wanstate.FamilyTranslation{Ready: false}},
	})
	items := natLiveItems(store.Snapshot(), liveTestGateway())
	want := []yangpub.Item{
		{
			Path: fmt.Sprintf(
				"/ietf-nat:nat/instances/instance[id='%d']/goodkind-mwan-steering:kernel-present",
				wanconfig.TranslationInstanceID("att", "ipv6"),
			),
			Value: "true",
		},
		{
			Path: fmt.Sprintf(
				"/ietf-nat:nat/instances/instance[id='%d']/goodkind-mwan-steering:kernel-present",
				wanconfig.TranslationInstanceID("webpass", "ipv6"),
			),
			Value: "false",
		},
	}
	if len(items) != len(want) {
		t.Fatalf("items = %v, want %v", items, want)
	}

	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("item[%d] = %v, want %v", i, items[i], want[i])
		}
	}
}

// Libyang rejects a subtree with an invalid peer key.
func TestUsablePeers_DropsPeersWithoutAnAddress(t *testing.T) {
	t.Parallel()
	reported := []*mwanv1.BGPPeerStatus{
		{Address: "3d06:bad:b01:201::2", Established: true},
		{Address: netip.Addr{}.String(), Established: false},
		{Address: "", Established: false},
		{Address: "10.240.240.2", Established: false},
	}
	peers, dropped := usablePeers(reported)
	if len(peers) != 2 || peers[0].Address != "3d06:bad:b01:201::2" ||
		peers[1].Address != "10.240.240.2" {
		t.Fatalf("peers = %+v", peers)
	}

	if !peers[0].Established || peers[1].Established {
		t.Fatalf("established flags lost: %+v", peers)
	}

	if len(dropped) != 2 || dropped[0] != "invalid IP" || dropped[1] != "" {
		t.Fatalf("dropped = %q", dropped)
	}
}

func TestAgentDialTarget_MakesListenAddressesDialable(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"[::]:50052":      "[::1]:50052",
		"0.0.0.0:50052":   "[::1]:50052",
		":50052":          "[::1]:50052",
		"[::1]:50052":     "[::1]:50052",
		"127.0.0.1:50052": "127.0.0.1:50052",
	}
	for listen, wantTarget := range cases {
		target, err := agentDialTarget(listen)
		if err != nil {
			t.Fatalf("agentDialTarget(%q): %v", listen, err)
		}

		if target != wantTarget {
			t.Fatalf("agentDialTarget(%q) = %q, want %q", listen, target, wantTarget)
		}
	}

	if _, err := agentDialTarget("not-an-address"); err == nil {
		t.Fatal("malformed address accepted")
	}
}
