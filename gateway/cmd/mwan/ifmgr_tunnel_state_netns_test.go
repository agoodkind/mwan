//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"testing"
	"time"
)

type tunnelRuntimeTransition struct {
	At        time.Time `json:"at"`
	Family    string    `json:"family"`
	Previous  string    `json:"previous"`
	Current   string    `json:"current"`
	Operation string    `json:"operation"`
	Reason    string    `json:"reason"`
}

type tunnelRuntimeFamily struct {
	Ownership struct {
		Routing    string `json:"routing"`
		Readiness  string `json:"readiness"`
		Protection string `json:"firewall-protection"`
	} `json:"goodkind-mwan-steering:ownership-family-state"`
}

type tunnelRuntimeInterface struct {
	Name      string              `json:"name"`
	V4        tunnelRuntimeFamily `json:"ietf-ip:ipv4"`
	V6        tunnelRuntimeFamily `json:"ietf-ip:ipv6"`
	Ownership struct {
		Transitions []tunnelRuntimeTransition `json:"recent-transition"`
	} `json:"goodkind-mwan-steering:ownership-state"`
	Steering struct {
		State struct {
			Carrying bool `json:"carrying"`
			Probes   []struct {
				Family string `json:"family"`
				Result string `json:"last-result"`
			} `json:"probe"`
		} `json:"state"`
	} `json:"goodkind-mwan-steering:steering"`
}

func readTunnelRuntimeInterface(read func() string, name string) (tunnelRuntimeInterface, bool) {
	var document struct {
		Interfaces struct {
			Entries []tunnelRuntimeInterface `json:"interface"`
		} `json:"ietf-interfaces:interfaces"`
	}
	if json.Unmarshal([]byte(read()), &document) != nil {
		return tunnelRuntimeInterface{}, false
	}
	for _, entry := range document.Interfaces.Entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return tunnelRuntimeInterface{}, false
}

func (entry tunnelRuntimeInterface) family(name string) tunnelRuntimeFamily {
	if name == "ipv4" {
		return entry.V4
	}
	return entry.V6
}

func waitTunnelRuntimeFamily(t *testing.T, daemon *runtimeDaemon, read func() string, name, family, routing, readiness string, window time.Duration) time.Duration {
	t.Helper()
	started := time.Now()
	deadline := started.Add(window)
	var last tunnelRuntimeInterface
	for time.Now().Before(deadline) {
		entry, found := readTunnelRuntimeInterface(read, name)
		if found {
			last = entry
			state := entry.family(family).Ownership
			if state.Routing == routing && state.Readiness == readiness {
				return time.Since(started)
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s %s routing=%s readiness=%s absent after %s; published state=%+v; daemon=%s",
		name, family, routing, readiness, window, last, tunnelRuntimeLog(t, daemon))
	return 0
}

// waitTunnelRuntimeReason reads the published transition history until an IPv6 routing transition
// after since reports the reason.
func waitTunnelRuntimeReason(t *testing.T, daemon *runtimeDaemon, read func() string, since time.Time, reason string, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	var last tunnelRuntimeInterface
	for time.Now().Before(deadline) {
		entry, found := readTunnelRuntimeInterface(read, tunnelRuntimeDevice)
		if found {
			last = entry
			for _, transition := range entry.Ownership.Transitions {
				if transition.Family == "ipv6" && transition.Operation == "evaluate-routing" && transition.Current == "not-ready" &&
					transition.Reason == reason && !transition.At.Before(since) {
					return
				}
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no IPv6 routing transition with reason %q after %s; transitions=%+v; daemon=%s",
		reason, since.Format(time.RFC3339Nano), last.Ownership.Transitions, tunnelRuntimeLog(t, daemon))
}
