//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netns"
)

const (
	CombinedStarting    = "starting"
	CombinedReady       = "ready"
	CombinedUnavailable = "unavailable"
	CombinedPayload     = "path"
)

type CombinedTransition struct {
	At         time.Time `json:"at"`
	Previous   string    `json:"previous"`
	Current    string    `json:"current"`
	Dependency string    `json:"dependency"`
	Reason     string    `json:"reason"`
}

type CombinedPath struct {
	State       string               `json:"state"`
	Dependency  string               `json:"dependency"`
	Reason      string               `json:"reason"`
	Since       time.Time            `json:"since"`
	Transitions []CombinedTransition `json:"transition"`
}

type CombinedFamily struct {
	Ownership struct {
		Path     CombinedPath        `json:"path-readiness"`
		Sessions []BgpRuntimeSession `json:"bgp-session"`
	} `json:"goodkind-mwan-steering:ownership-family-state"`
}

type CombinedInterface struct {
	Name string         `json:"name"`
	V4   CombinedFamily `json:"ietf-ip:ipv4"`
	V6   CombinedFamily `json:"ietf-ip:ipv6"`
}

func ReadCombinedPath(read func() string, device, family string) CombinedPath {
	var document struct {
		Interfaces struct {
			Entries []CombinedInterface `json:"interface"`
		} `json:"ietf-interfaces:interfaces"`
	}
	if json.Unmarshal([]byte(read()), &document) != nil {
		return CombinedPath{}
	}
	for _, entry := range document.Interfaces.Entries {
		if entry.Name != device {
			continue
		}
		path := entry.V6.Ownership.Path
		if family == "ipv4" {
			path = entry.V4.Ownership.Path
		}
		slices.SortFunc(
			path.Transitions,
			func(left, right CombinedTransition) int { return left.At.Compare(right.At) },
		)
		return path
	}
	return CombinedPath{}
}

func WaitCombinedTransition(
	t *testing.T,
	daemon *RuntimeDaemon,
	read func() string,
	device string,
	family string,
	since time.Time,
	description string,
	accept func(CombinedTransition) bool,
) CombinedTransition {
	t.Helper()
	var matched CombinedTransition
	WaitBGPRuntime(
		t,
		daemon,
		read,
		device+" "+family+" path "+description,
		TunnelRuntimeRecoverWindow,
		func() bool {
			path := ReadCombinedPath(read, device, family)
			for _, transition := range path.Transitions {
				if path.State == transition.Current && !transition.At.Before(since) &&
					accept(transition) {
					matched = transition
					return true
				}
			}
			return false
		},
	)
	return matched
}

func WaitCombinedReady(
	t *testing.T,
	daemon *RuntimeDaemon,
	read func() string,
	device string,
	family string,
	since time.Time,
) time.Duration {
	t.Helper()
	transition := WaitCombinedTransition(
		t,
		daemon,
		read,
		device,
		family,
		since,
		"ready",
		func(transition CombinedTransition) bool {
			return transition.Current == CombinedReady && transition.Dependency == "" &&
				transition.Reason == ""
		},
	)
	return transition.At.Sub(since)
}

func WaitCombinedUnavailable(
	t *testing.T,
	daemon *RuntimeDaemon,
	read func() string,
	device string,
	since time.Time,
	dependencies ...string,
) time.Duration {
	t.Helper()
	description := fmt.Sprintf("unavailable with one dependency of %v", dependencies)
	transition := WaitCombinedTransition(
		t,
		daemon,
		read,
		device,
		"ipv6",
		since,
		description,
		func(transition CombinedTransition) bool {
			return transition.Current == CombinedUnavailable &&
				slices.Contains(dependencies, transition.Dependency) &&
				transition.Reason != ""
		},
	)
	return transition.At.Sub(since)
}

func RequireCombinedBound(t *testing.T, description string, measured, bound time.Duration) {
	t.Helper()
	t.Logf("measured %s: %s (bound %s)", description, measured.Round(time.Millisecond), bound)
	if measured > bound {
		t.Fatalf("%s took %s, want at most %s", description, measured, bound)
	}
}

func RequireCombinedUnchanged(
	t *testing.T,
	read func() string,
	since time.Time,
	family string,
	devices ...string,
) {
	t.Helper()
	for _, device := range devices {
		path := ReadCombinedPath(read, device, family)
		if path.State != CombinedReady {
			t.Fatalf("%s %s path = %+v, want ready", device, family, path)
		}
		for _, transition := range path.Transitions {
			if !transition.At.Before(since) {
				t.Fatalf("%s %s path changed during the fault: %+v", device, family, transition)
			}
		}
	}
}

func CombinedTransferPath(t *testing.T, topology CombinedTopology, source string) (string, error) {
	t.Helper()
	destination := net.JoinHostPort(CombinedDestination, strconv.Itoa(CombinedPort))
	return RuntimeTransferPath(
		t,
		topology.Tunnel,
		topology.Destinations,
		"tcp6",
		destination,
		source,
		nil,
	)
}

func RuntimeTransferPath(
	t *testing.T,
	topology TunnelRuntimeTopology,
	destinations map[string]netns.NsHandle,
	network string,
	destination string,
	source string,
	control func(string, string, syscall.RawConn) error,
) (string, error) {
	t.Helper()
	defer SetRuntimeNamespace(t, topology.Gateway)
	accepted := make(chan string, len(destinations))
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for device, namespace := range destinations {
		SetRuntimeNamespace(t, namespace)
		listener, err := net.Listen(network, destination)
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
			if _, copyErr := io.CopyN(
				connection,
				connection,
				int64(len(CombinedPayload)),
			); copyErr == nil {
				accepted <- device
			}
		}()
	}
	SetRuntimeNamespace(t, topology.Client)
	local, err := net.ResolveTCPAddr(network, net.JoinHostPort(source, "0"))
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: time.Second, Control: control}
	connection, err := dialer.Dial(network, destination)
	if err != nil {
		return "", fmt.Errorf("dial %s from %s: %w", destination, source, err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write([]byte(CombinedPayload)); err != nil {
		return "", fmt.Errorf("write to %s from %s: %w", destination, source, err)
	}
	if _, err := io.ReadFull(connection, make([]byte, len(CombinedPayload))); err != nil {
		return "", fmt.Errorf("read from %s as %s: %w", destination, source, err)
	}
	return <-accepted, nil
}

func RequireCombinedPaths(
	t *testing.T,
	daemon *RuntimeDaemon,
	read func() string,
	topology CombinedTopology,
	ready ...string,
) {
	t.Helper()
	for device := range topology.Destinations {
		if state := ReadCombinedPath(
			read,
			device,
			"ipv6",
		).State; (state == CombinedReady) != slices.Contains(ready, device) {
			t.Fatalf("%s ipv6 path state = %s, want the ready paths %v", device, state, ready)
		}
	}
	var used string
	WaitBGPRuntime(
		t,
		daemon,
		read,
		"transfer to "+CombinedDestination,
		TunnelRuntimeFailureWindow,
		func() bool {
			path, err := CombinedTransferPath(t, topology, TunnelRuntimeClientV6)
			if err != nil {
				t.Logf("transfer failed and repeats: %v", err)
			}
			used = path
			return err == nil
		},
	)
	if !slices.Contains(ready, used) {
		t.Fatalf("transfer used %s, want one of the ready paths %v", used, ready)
	}
	t.Logf("transfer used %s with the ready paths %v", used, ready)
}
