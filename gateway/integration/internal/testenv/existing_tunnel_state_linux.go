//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"encoding/json"
	"testing"
	"time"
)

type TunnelRuntimeTransition struct {
	At        time.Time `json:"at"`
	Family    string    `json:"family"`
	Previous  string    `json:"previous"`
	Current   string    `json:"current"`
	Operation string    `json:"operation"`
	Reason    string    `json:"reason"`
}

type TunnelRuntimeFamily struct {
	Ownership struct {
		Routing    string `json:"routing"`
		Readiness  string `json:"readiness"`
		Protection string `json:"firewall-protection"`
	} `json:"goodkind-mwan-steering:ownership-family-state"`
}

type TunnelRuntimeInterface struct {
	Name      string              `json:"name"`
	V4        TunnelRuntimeFamily `json:"ietf-ip:ipv4"`
	V6        TunnelRuntimeFamily `json:"ietf-ip:ipv6"`
	Ownership struct {
		Transitions []TunnelRuntimeTransition `json:"recent-transition"`
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

func ReadTunnelRuntimeInterface(read func() string, name string) (TunnelRuntimeInterface, bool) {
	var document struct {
		Interfaces struct {
			Entries []TunnelRuntimeInterface `json:"interface"`
		} `json:"ietf-interfaces:interfaces"`
	}
	if json.Unmarshal([]byte(read()), &document) != nil {
		return TunnelRuntimeInterface{}, false
	}
	for _, entry := range document.Interfaces.Entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return TunnelRuntimeInterface{}, false
}

func (entry TunnelRuntimeInterface) Family(name string) TunnelRuntimeFamily {
	if name == "ipv4" {
		return entry.V4
	}
	return entry.V6
}

func WaitTunnelRuntimeFamily(
	t *testing.T,
	daemon *RuntimeDaemon,
	read func() string,
	name string,
	family string,
	routing string,
	readiness string,
	window time.Duration,
) time.Duration {
	t.Helper()
	started := time.Now()
	deadline := started.Add(window)
	var last TunnelRuntimeInterface
	for time.Now().Before(deadline) {
		entry, found := ReadTunnelRuntimeInterface(read, name)
		if found {
			last = entry
			state := entry.Family(family).Ownership
			if state.Routing == routing && state.Readiness == readiness {
				return time.Since(started)
			}
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"%s %s routing=%s readiness=%s absent after %s; published state=%+v; daemon=%s",
		name,
		family,
		routing,
		readiness,
		window,
		last,
		TunnelRuntimeLog(t, daemon),
	)
	return 0
}

func WaitTunnelRuntimeReason(
	t *testing.T,
	daemon *RuntimeDaemon,
	read func() string,
	since time.Time,
	reason string,
	window time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(window)
	var last TunnelRuntimeInterface
	for time.Now().Before(deadline) {
		entry, found := ReadTunnelRuntimeInterface(read, TunnelRuntimeDevice)
		if found {
			last = entry
			for _, transition := range entry.Ownership.Transitions {
				if transition.Family == "ipv6" && transition.Operation == "evaluate-routing" &&
					transition.Current == "not-ready" &&
					transition.Reason == reason &&
					!transition.At.Before(since) {
					return
				}
			}
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf(
		"no IPv6 routing transition with reason %q after %s; transitions=%+v; daemon=%s",
		reason,
		since.Format(time.RFC3339Nano),
		last.Ownership.Transitions,
		TunnelRuntimeLog(t, daemon),
	)
}
