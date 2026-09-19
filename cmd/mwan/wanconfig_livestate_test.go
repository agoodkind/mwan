package main

import (
	"net/netip"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

// liveTestGateway mirrors the testbed shape: three members, two of them
// translating, the same projection the configuration publish uses.
func liveTestGateway() wanconfig.Gateway {
	internal := netip.MustParsePrefix("3d06:bad:b01:210::/60")
	return wanconfig.Gateway{
		InternalIface: "eninternal0",
		Members: []wanconfig.Member{
			{
				Name: "att", Iface: "enatt0", Tier: 0, ProbePolicy: "att",
				NPTInternal: internal, NPTExternal: netip.MustParsePrefix("2001:db8:a::/60"),
			},
			{Name: "monkeybrains", Iface: "enmbrains0", Tier: 1, ProbePolicy: "monkeybrains"},
			{
				Name: "webpass", Iface: "enwebpass0", Tier: 0, ProbePolicy: "webpass",
				NPTInternal: internal, NPTExternal: netip.MustParsePrefix("2001:db8:b::/60"),
			},
		},
	}
}

// TestInterfacesLiveItems_ServesTheSnapshot pins the served paths for a
// full snapshot: health with its evidence, carrying, the delegated
// prefix on the ipv6 container, the active tier, and the peer list.
// These paths are the RESTCONF surface; a change here changes what an
// operator reads.
func TestInterfacesLiveItems_ServesTheSnapshot(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	transition := time.Date(2026, 8, 20, 3, 4, 5, 0, time.UTC)
	store.SetHealth(map[string]wanstate.MemberHealth{
		"att": {
			Verdict: wanstate.HealthHealthy, ConsecutiveFailures: 0,
			LastTransition: transition, V4: wanstate.ProbePass, V6: wanstate.ProbePass,
		},
	})
	store.SetRouting(0, map[string]wanstate.MemberRouting{
		"att": {Carrying: true},
	})
	store.SetTranslation(map[string]wanstate.MemberTranslation{
		"att": {Delegated: netip.MustParsePrefix("2001:db8:a::/60"), KernelPresent: true},
	})
	store.SetBGP(wanstate.BGP{
		Peers:   []wanstate.BGPPeer{{Address: "3d06:bad:b01:201::2", Established: true}},
		ReadAt:  time.Now(),
		Reached: true,
	})
	store.SetIntendedRuleset("chain prerouting:\nchain postrouting:\n")

	items := interfacesLiveItems(store.Snapshot(), liveTestGateway())

	attBase := "/ietf-interfaces:interfaces/interface[name='enatt0']/goodkind-mwan-steering:steering/state"
	peerBase := "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/bgp-peer[address='3d06:bad:b01:201::2']"
	want := map[string]string{
		attBase + "/health":                           "healthy",
		attBase + "/consecutive-failures":             "0",
		attBase + "/probe[family='ipv4']/last-result": "pass",
		attBase + "/probe[family='ipv6']/last-result": "pass",
		attBase + "/last-transition":                  "2026-08-20T03:04:05Z",
		attBase + "/carrying":                         "true",
		"/ietf-interfaces:interfaces/interface[name='enatt0']/ietf-ip:ipv6/goodkind-mwan-steering:delegated-prefix": "2001:db8:a::/60",
		"/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/active-tier":                       "0",
		"/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/intended-ruleset":                  "chain prerouting:\nchain postrouting:\n",
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
	// Members with no recorded state serve nothing rather than empties.
	for _, item := range items {
		if item.Path == "/ietf-interfaces:interfaces/interface[name='enwebpass0']/goodkind-mwan-steering:steering/state/health" {
			t.Fatalf("webpass has no snapshot but was served: %v", item)
		}
	}
}

// TestInterfacesLiveItems_ServesOwnedAddresses pins where the addresses a
// provider link answers for are served: a leaf-list under that provider's wan
// container, beside the provider's name, and nothing for a provider that owns
// none.
func TestInterfacesLiveItems_ServesOwnedAddresses(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	store.SetRouting(0, map[string]wanstate.MemberRouting{
		"att": {Carrying: true, OwnedAddresses: nil},
		"webpass": {
			Carrying: true,
			OwnedAddresses: []netip.Addr{
				netip.MustParseAddr("203.0.113.3"),
				netip.MustParseAddr("203.0.113.4"),
			},
		},
	})

	items := interfacesLiveItems(store.Snapshot(), liveTestGateway())

	served := map[string][]string{}
	for _, item := range items {
		served[item.Path] = append(served[item.Path], item.Value)
	}
	webpassWAN := "/ietf-interfaces:interfaces/interface[name='enwebpass0']/goodkind-mwan-steering:wan"
	if got := served[webpassWAN+"/owned-address"]; len(got) != 2 || got[0] != "203.0.113.3" || got[1] != "203.0.113.4" {
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

// TestInterfacesLiveItems_MarksAStaleAgentAnswer pins the stale rule: an
// unreached agent or an expired read serves stale=true on every peer.
func TestInterfacesLiveItems_MarksAStaleAgentAnswer(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	store.SetBGP(wanstate.BGP{
		Peers:   []wanstate.BGPPeer{{Address: "3d06:bad:b01:201::2", Established: true}},
		ReadAt:  time.Now().Add(-time.Hour),
		Reached: true,
	})
	items := interfacesLiveItems(store.Snapshot(), liveTestGateway())
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

// TestNatLiveItems_NumbersInstancesLikeTheConfigPublish pins that kernel
// presence lands on the same instance ids the configuration publish
// assigned: sequential over translating members in member order, so att
// is instance 1 and webpass instance 2 while monkeybrains has none.
func TestNatLiveItems_NumbersInstancesLikeTheConfigPublish(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	store.SetTranslation(map[string]wanstate.MemberTranslation{
		"att":     {Delegated: netip.MustParsePrefix("2001:db8:a::/60"), KernelPresent: true},
		"webpass": {Delegated: netip.Prefix{}, KernelPresent: false},
	})
	items := natLiveItems(store.Snapshot(), liveTestGateway())
	want := []yangpub.Item{
		{Path: "/ietf-nat:nat/instances/instance[id='1']/goodkind-mwan-steering:kernel-present", Value: "true"},
		{Path: "/ietf-nat:nat/instances/instance[id='2']/goodkind-mwan-steering:kernel-present", Value: "false"},
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

// TestUsablePeers_DropsPeersWithoutAnAddress pins the guard that keeps a
// peer reported with an unparseable address (the zero address prints as
// "invalid IP") out of the store. That address is the bgp-peer list key;
// libyang rejects it and the whole ietf-interfaces operational read
// fails, which is what the testbed gateway did on the first live read
// before the agent reported session addresses.
func TestUsablePeers_DropsPeersWithoutAnAddress(t *testing.T) {
	t.Parallel()
	reported := []*mwanv1.BGPPeerStatus{
		{Address: "3d06:bad:b01:201::2", Established: true},
		{Address: netip.Addr{}.String(), Established: false},
		{Address: "", Established: false},
		{Address: "10.240.240.2", Established: false},
	}
	peers, dropped := usablePeers(reported)
	if len(peers) != 2 || peers[0].Address != "3d06:bad:b01:201::2" || peers[1].Address != "10.240.240.2" {
		t.Fatalf("peers = %+v", peers)
	}
	if !peers[0].Established || peers[1].Established {
		t.Fatalf("established flags lost: %+v", peers)
	}
	if len(dropped) != 2 || dropped[0] != "invalid IP" || dropped[1] != "" {
		t.Fatalf("dropped = %q", dropped)
	}
}

// TestAgentDialTarget_MakesListenAddressesDialable pins the wildcard and
// empty-host rewrites that let the poller reach the local agent.
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
