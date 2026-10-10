//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type bgpRuntimeSession struct {
	Name        string   `json:"name"`
	State       string   `json:"state"`
	Established bool     `json:"established"`
	Accepted    []string `json:"accepted-prefix"`
	Advertised  []string `json:"advertised-prefix"`
	Rejections  []struct {
		Prefix string `json:"prefix"`
		Reason string `json:"reason"`
	} `json:"rejection"`
}

type bgpRuntimeInterface struct {
	Name string `json:"name"`
	V6   struct {
		Ownership struct {
			Routing   string              `json:"routing"`
			Readiness string              `json:"readiness"`
			Sessions  []bgpRuntimeSession `json:"bgp-session"`
		} `json:"goodkind-mwan-steering:ownership-family-state"`
	} `json:"ietf-ip:ipv6"`
	Ownership struct {
		Transitions []tunnelRuntimeTransition `json:"recent-transition"`
	} `json:"goodkind-mwan-steering:ownership-state"`
}

func readBGPRuntimeInterface(read func() string, name string) (bgpRuntimeInterface, bool) {
	var document struct {
		Interfaces struct {
			Entries []bgpRuntimeInterface `json:"interface"`
		} `json:"ietf-interfaces:interfaces"`
	}
	if json.Unmarshal([]byte(read()), &document) != nil {
		return bgpRuntimeInterface{}, false
	}
	for _, entry := range document.Interfaces.Entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return bgpRuntimeInterface{}, false
}

func waitBGPRuntime(t *testing.T, daemon *runtimeDaemon, read func() string, description string, window time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		if daemon != nil {
			assertRuntimeDaemonRunning(t, daemon)
		}
		time.Sleep(50 * time.Millisecond)
	}
	daemonLog := ""
	if daemon != nil {
		daemonLog = tunnelRuntimeLog(t, daemon)
	}
	t.Fatalf("%s absent after %s; published state=%s; daemon=%s", description, window, read(), daemonLog)
}

func waitBGPRuntimeSession(t *testing.T, daemon *runtimeDaemon, read func() string, device, description string, accept func(bgpRuntimeSession) bool) bgpRuntimeSession {
	t.Helper()
	var matched bgpRuntimeSession
	waitBGPRuntime(t, daemon, read, device+" session with "+description, tunnelRuntimeRecoverWindow, func() bool {
		entry, found := readBGPRuntimeInterface(read, device)
		if !found || len(entry.V6.Ownership.Sessions) != 1 {
			return false
		}
		matched = entry.V6.Ownership.Sessions[0]
		return accept(matched)
	})
	return matched
}

func waitBGPRuntimeReason(t *testing.T, daemon *runtimeDaemon, read func() string, device string, since time.Time, reason string) {
	t.Helper()
	waitBGPRuntime(t, daemon, read, device+" IPv6 routing transition with reason "+reason, tunnelRuntimeRecoverWindow, func() bool {
		entry, _ := readBGPRuntimeInterface(read, device)
		for _, transition := range entry.Ownership.Transitions {
			if transition.Family == "ipv6" && transition.Operation == "evaluate-routing" && transition.Current == "not-ready" &&
				transition.Reason == reason && !transition.At.Before(since) {
				return true
			}
		}
		return false
	})
}

func requireBGPRuntimeFamily(t *testing.T, read func() string, device, routing, readiness string) {
	t.Helper()
	entry, found := readBGPRuntimeInterface(read, device)
	state := entry.V6.Ownership
	if !found || state.Routing != routing || state.Readiness != readiness {
		t.Fatalf("%s ipv6 routing=%s readiness=%s, want routing=%s readiness=%s", device, state.Routing, state.Readiness, routing, readiness)
	}
}

func waitBGPRuntimeReceived(t *testing.T, daemon *runtimeDaemon, read func() string, router *bgpRuntimeRouter, want ...netip.Prefix) {
	t.Helper()
	waitBGPRuntime(t, daemon, read, "router received prefixes", tunnelRuntimeRecoverWindow, func() bool {
		return slices.Equal(router.received(), want)
	})
}

func bgpRuntimeGuardedRead(t *testing.T, read func() string, device string, router *bgpRuntimeRouter) func() string {
	t.Helper()
	return func() string {
		received := router.received()
		published := read()
		entry, found := readBGPRuntimeInterface(func() string { return published }, device)
		if !found || entry.V6.Ownership.Readiness == "ready" {
			return published
		}
		if len(received) != 0 {
			t.Fatalf("router received %v while the published IPv6 readiness was %s", received, entry.V6.Ownership.Readiness)
		}
		for _, session := range entry.V6.Ownership.Sessions {
			if len(session.Advertised) != 0 {
				t.Fatalf("session published the advertised prefixes %v while the IPv6 readiness was %s",
					session.Advertised, entry.V6.Ownership.Readiness)
			}
		}
		return published
	}
}

func bgpRuntimeGatewayRoute(t *testing.T, device string, prefix netip.Prefix) bool {
	t.Helper()
	filter := &netlink.Route{Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_BGP}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, filter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		t.Fatalf("list the gateway BGP routes: %v", err)
	}
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		destination := "::/0"
		if route.Dst != nil {
			destination = route.Dst.String()
		}
		if route.LinkIndex == link.Attrs().Index && destination == prefix.String() {
			return true
		}
	}
	return false
}

func waitBGPRuntimeGatewayRoute(t *testing.T, daemon *runtimeDaemon, read func() string, device string, prefix netip.Prefix, present bool) {
	t.Helper()
	waitBGPRuntime(t, daemon, read, "gateway BGP route "+prefix.String(), tunnelRuntimeRecoverWindow, func() bool {
		return bgpRuntimeGatewayRoute(t, device, prefix) == present
	})
}
