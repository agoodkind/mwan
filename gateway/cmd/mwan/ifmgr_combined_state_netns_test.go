//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"
)

const (
	combinedStarting    = "starting"
	combinedReady       = "ready"
	combinedUnavailable = "unavailable"
	combinedPayload     = "path"
)

type combinedTransition struct {
	At         time.Time `json:"at"`
	Previous   string    `json:"previous"`
	Current    string    `json:"current"`
	Dependency string    `json:"dependency"`
	Reason     string    `json:"reason"`
}

type combinedPath struct {
	State       string               `json:"state"`
	Dependency  string               `json:"dependency"`
	Reason      string               `json:"reason"`
	Since       time.Time            `json:"since"`
	Transitions []combinedTransition `json:"transition"`
}

type combinedFamily struct {
	Ownership struct {
		Path     combinedPath        `json:"path-readiness"`
		Sessions []bgpRuntimeSession `json:"bgp-session"`
	} `json:"goodkind-mwan-steering:ownership-family-state"`
}

type combinedInterface struct {
	Name string         `json:"name"`
	V4   combinedFamily `json:"ietf-ip:ipv4"`
	V6   combinedFamily `json:"ietf-ip:ipv6"`
}

func readCombinedPath(read func() string, device, family string) combinedPath {
	var document struct {
		Interfaces struct {
			Entries []combinedInterface `json:"interface"`
		} `json:"ietf-interfaces:interfaces"`
	}
	if json.Unmarshal([]byte(read()), &document) != nil {
		return combinedPath{}
	}
	for _, entry := range document.Interfaces.Entries {
		if entry.Name != device {
			continue
		}
		path := entry.V6.Ownership.Path
		if family == "ipv4" {
			path = entry.V4.Ownership.Path
		}
		slices.SortFunc(path.Transitions, func(left, right combinedTransition) int { return left.At.Compare(right.At) })
		return path
	}
	return combinedPath{}
}

func waitCombinedTransition(t *testing.T, daemon *runtimeDaemon, read func() string, device, family string, since time.Time, description string, accept func(combinedTransition) bool) combinedTransition {
	t.Helper()
	var matched combinedTransition
	waitBGPRuntime(t, daemon, read, device+" "+family+" path "+description, tunnelRuntimeRecoverWindow, func() bool {
		path := readCombinedPath(read, device, family)
		for _, transition := range path.Transitions {
			if path.State == transition.Current && !transition.At.Before(since) && accept(transition) {
				matched = transition
				return true
			}
		}
		return false
	})
	return matched
}

func waitCombinedReady(t *testing.T, daemon *runtimeDaemon, read func() string, device, family string, since time.Time) time.Duration {
	t.Helper()
	transition := waitCombinedTransition(t, daemon, read, device, family, since, "ready", func(transition combinedTransition) bool {
		return transition.Current == combinedReady && transition.Dependency == "" && transition.Reason == ""
	})
	return transition.At.Sub(since)
}

func waitCombinedUnavailable(t *testing.T, daemon *runtimeDaemon, read func() string, device string, since time.Time, dependencies ...string) time.Duration {
	t.Helper()
	description := fmt.Sprintf("unavailable with one dependency of %v", dependencies)
	transition := waitCombinedTransition(t, daemon, read, device, "ipv6", since, description, func(transition combinedTransition) bool {
		return transition.Current == combinedUnavailable && slices.Contains(dependencies, transition.Dependency) && transition.Reason != ""
	})
	return transition.At.Sub(since)
}

func requireCombinedBound(t *testing.T, description string, measured, bound time.Duration) {
	t.Helper()
	t.Logf("measured %s: %s (bound %s)", description, measured.Round(time.Millisecond), bound)
	if measured > bound {
		t.Fatalf("%s took %s, want at most %s", description, measured, bound)
	}
}

func requireCombinedUnchanged(t *testing.T, read func() string, since time.Time, family string, devices ...string) {
	t.Helper()
	for _, device := range devices {
		path := readCombinedPath(read, device, family)
		if path.State != combinedReady {
			t.Fatalf("%s %s path = %+v, want ready", device, family, path)
		}
		for _, transition := range path.Transitions {
			if !transition.At.Before(since) {
				t.Fatalf("%s %s path changed during the fault: %+v", device, family, transition)
			}
		}
	}
}

func combinedTransferPath(t *testing.T, topology combinedTopology, source string) (string, error) {
	t.Helper()
	defer setRuntimeNamespace(t, topology.tunnel.gateway)
	destination := net.JoinHostPort(combinedDestination, strconv.Itoa(combinedPort))
	accepted := make(chan string, len(topology.destinations))
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for device, namespace := range topology.destinations {
		setRuntimeNamespace(t, namespace)
		listener, err := net.Listen("tcp6", destination)
		if err != nil {
			t.Fatalf("listen on %s behind %s: %v", destination, device, err)
		}
		listeners = append(listeners, listener)
		go func() {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			defer func() { _ = connection.Close() }()
			if _, copyErr := io.CopyN(connection, connection, int64(len(combinedPayload))); copyErr == nil {
				accepted <- device
			}
		}()
	}
	setRuntimeNamespace(t, topology.tunnel.client)
	local, err := net.ResolveTCPAddr("tcp6", net.JoinHostPort(source, "0"))
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: time.Second}
	connection, err := dialer.Dial("tcp6", destination)
	if err != nil {
		return "", fmt.Errorf("dial %s from %s: %w", destination, source, err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write([]byte(combinedPayload)); err != nil {
		return "", fmt.Errorf("write to %s from %s: %w", destination, source, err)
	}
	if _, err := io.ReadFull(connection, make([]byte, len(combinedPayload))); err != nil {
		return "", fmt.Errorf("read from %s as %s: %w", destination, source, err)
	}
	return <-accepted, nil
}

func requireCombinedPaths(t *testing.T, daemon *runtimeDaemon, read func() string, topology combinedTopology, ready ...string) {
	t.Helper()
	for device := range topology.destinations {
		if state := readCombinedPath(read, device, "ipv6").State; (state == combinedReady) != slices.Contains(ready, device) {
			t.Fatalf("%s ipv6 path state = %s, want the ready paths %v", device, state, ready)
		}
	}
	var used string
	waitBGPRuntime(t, daemon, read, "transfer to "+combinedDestination, tunnelRuntimeFailureWindow, func() bool {
		path, err := combinedTransferPath(t, topology, tunnelRuntimeClientV6)
		if err != nil {
			t.Logf("transfer failed and repeats: %v", err)
		}
		used = path
		return err == nil
	})
	if !slices.Contains(ready, used) {
		t.Fatalf("transfer used %s, want one of the ready paths %v", used, ready)
	}
	t.Logf("transfer used %s with the ready paths %v", used, ready)
}
