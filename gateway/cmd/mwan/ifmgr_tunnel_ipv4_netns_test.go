//go:build linux && firewallnetns

package main

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"
)

type tunnelRuntimeIPv4 struct {
	// Providers maps each client address to the gateway interface of the provider link.
	// The provider link transmitted the transfer from the client address.
	Providers map[string]string
	// Selection is the kernel's IPv4 assignment of new connections to provider marks.
	Selection string
	State     map[string]tunnelRuntimeIPv4State
}

func tunnelRuntimeIPv4Link(t *testing.T, run tunnelRuntimeRun, port int, client string) (string, error) {
	t.Helper()
	topology := run.topology
	captures := map[string]int{
		tunnelRuntimeUnderlay:  openTunnelRuntimeLinkCapture(t, topology, topology.isp, tunnelRuntimeISPLink),
		tunnelRuntimeAlternate: openTunnelRuntimeLinkCapture(t, topology, topology.alternate, tunnelRuntimeAltLink),
	}
	destination := net.JoinHostPort(tunnelRuntimeRemote, fmt.Sprint(port))
	peer, transferErr := tunnelRuntimeTransfer(t, topology, topology.endpoint, topology.client, "tcp4", destination, client+":0", tunnelRuntimeTransferSize)
	remote := netip.MustParseAddr(tunnelRuntimeRemote)
	segments := make(map[string]int)
	for provider, capture := range captures {
		for _, frame := range readTunnelRuntimeCapture(t, capture) {
			if frame.etherType == tunnelRuntimeEtherIPv4 && frame.protocol == tunnelRuntimeProtocolTCP && frame.destination == remote {
				segments[provider]++
			}
		}
	}
	if transferErr != nil {
		return "", transferErr
	}
	selected := ""
	for provider, translated := range tunnelRuntimeIPv4Providers() {
		if segments[provider] == 0 {
			continue
		}
		if selected != "" || peer != netip.MustParseAddr(translated) {
			return "", fmt.Errorf("client %s: TCP segments per provider link %v with translated source %s", client, segments, peer)
		}
		selected = provider
	}
	if selected == "" {
		return "", fmt.Errorf("client %s: no provider link transported the transfer: %v", client, segments)
	}
	return selected, nil
}

func observeTunnelRuntimeIPv4(t *testing.T, run tunnelRuntimeRun, port int) (tunnelRuntimeIPv4, error) {
	t.Helper()
	selection, err := tunnelRuntimeIPv4Selection()
	observed := tunnelRuntimeIPv4{Providers: make(map[string]string), Selection: selection, State: readTunnelRuntimeIPv4State(run)}
	if err != nil {
		return observed, err
	}
	for _, client := range tunnelRuntimeClientsV4() {
		provider, err := tunnelRuntimeIPv4Link(t, run, port, client)
		if err != nil {
			return observed, err
		}
		observed.Providers[client] = provider
	}
	return observed, nil
}

// requireTunnelRuntimeIPv4 compares the IPv4 transfers with the transfers before the first tunnel fault.
// requireTunnelRuntimeIPv4 records the provider link of each client address and the steering assignment
// on the first call. requireTunnelRuntimeIPv4 records the published IPv4 state on the first call.
func requireTunnelRuntimeIPv4(t *testing.T, daemon *runtimeDaemon, run tunnelRuntimeRun, port int, phase string) {
	t.Helper()
	observed, err := observeTunnelRuntimeIPv4(t, run, port)
	if err != nil {
		t.Fatalf("IPv4 TCP %s: %v; daemon=%s", phase, err, tunnelRuntimeLog(t, daemon))
	}
	if len(run.ipv4.Providers) == 0 {
		ready := tunnelRuntimeIPv4State{Routing: "ready", Readiness: "ready", Carrying: true}
		for provider := range tunnelRuntimeIPv4Providers() {
			if observed.State[provider] != ready {
				t.Fatalf("published IPv4 state %s = %+v, want %+v for both IPv4 providers", phase, observed.State, ready)
			}
			if !slices.Contains(slices.Collect(maps.Values(observed.Providers)), provider) {
				t.Fatalf("no client address selects IPv4 provider %s %s: %+v", provider, phase, observed.Providers)
			}
		}
		*run.ipv4 = observed
		t.Logf("IPv4 observations %s: %+v", phase, observed)
		return
	}
	if !reflect.DeepEqual(observed, *run.ipv4) {
		t.Fatalf("IPv4 observations %s = %+v, want the observations before the first tunnel fault %+v; daemon=%s",
			phase, observed, *run.ipv4, tunnelRuntimeLog(t, daemon))
	}
}

func waitTunnelRuntimeIPv4Provider(t *testing.T, daemon *runtimeDaemon, run tunnelRuntimeRun, port int, provider, phase string) {
	t.Helper()
	deadline := time.Now().Add(tunnelRuntimeRecoverWindow)
	var observed tunnelRuntimeIPv4
	var err error
	for time.Now().Before(deadline) {
		observed, err = observeTunnelRuntimeIPv4(t, run, port)
		selected := slices.Collect(maps.Values(observed.Providers))
		if err == nil && !slices.ContainsFunc(selected, func(link string) bool { return link != provider }) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("IPv4 TCP %s = %+v, %v; want provider link %s; daemon=%s", phase, observed, err, provider, tunnelRuntimeLog(t, daemon))
}
