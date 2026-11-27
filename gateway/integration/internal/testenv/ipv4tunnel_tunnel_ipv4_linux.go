//go:build linux && (firewall || protocol || systemd)

package testenv

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

type TunnelRuntimeIPv4 struct {
	Providers map[string]string
	Selection string
	State     map[string]TunnelRuntimeIPv4State
}

func TunnelRuntimeIPv4Link(
	t *testing.T,
	run TunnelRuntimeRun,
	port int,
	client string,
) (string, error) {
	t.Helper()
	topology := run.Topology
	captures := map[string]int{
		TunnelRuntimeUnderlay: OpenTunnelRuntimeLinkCapture(
			t,
			topology,
			topology.Isp,
			TunnelRuntimeISPLink,
		),
		TunnelRuntimeAlternate: OpenTunnelRuntimeLinkCapture(
			t,
			topology,
			topology.Alternate,
			TunnelRuntimeAltLink,
		),
	}
	destination := net.JoinHostPort(TunnelRuntimeRemote, fmt.Sprint(port))
	peer, transferErr := TunnelRuntimeTransfer(
		t,
		topology,
		topology.Endpoint,
		topology.Client,
		"tcp4",
		destination,
		client+":0",
		TunnelRuntimeTransferSize,
	)
	remote := netip.MustParseAddr(TunnelRuntimeRemote)
	segments := make(map[string]int)
	for provider, capture := range captures {
		for _, frame := range ReadTunnelRuntimeCapture(t, capture) {
			if frame.EtherType == TunnelRuntimeEtherIPv4 &&
				frame.Protocol == TunnelRuntimeProtocolTCP &&
				frame.Destination == remote {
				segments[provider]++
			}
		}
	}
	if transferErr != nil {
		return "", transferErr
	}
	selected := ""
	for provider, translated := range TunnelRuntimeIPv4Providers() {
		if segments[provider] == 0 {
			continue
		}
		if selected != "" || peer != netip.MustParseAddr(translated) {
			return "", fmt.Errorf(
				"client %s: TCP segments per provider link %v with translated source %s",
				client,
				segments,
				peer,
			)
		}
		selected = provider
	}
	if selected == "" {
		return "", fmt.Errorf(
			"client %s: no provider link transported the transfer: %v",
			client,
			segments,
		)
	}
	return selected, nil
}

func ObserveTunnelRuntimeIPv4(
	t *testing.T,
	run TunnelRuntimeRun,
	port int,
) (TunnelRuntimeIPv4, error) {
	t.Helper()
	selection, err := TunnelRuntimeIPv4Selection()
	observed := TunnelRuntimeIPv4{
		Providers: make(map[string]string),
		Selection: selection,
		State:     ReadTunnelRuntimeIPv4State(run),
	}
	if err != nil {
		return observed, err
	}
	for _, client := range TunnelRuntimeClientsV4() {
		provider, err := TunnelRuntimeIPv4Link(t, run, port, client)
		if err != nil {
			return observed, err
		}
		observed.Providers[client] = provider
	}
	return observed, nil
}

func RequireTunnelRuntimeIPv4(
	t *testing.T,
	daemon *RuntimeDaemon,
	run TunnelRuntimeRun,
	port int,
	phase string,
) {
	t.Helper()
	observed, err := ObserveTunnelRuntimeIPv4(t, run, port)
	if err != nil {
		t.Fatalf("IPv4 TCP %s: %v; daemon=%s", phase, err, TunnelRuntimeLog(t, daemon))
	}
	if len(run.Ipv4.Providers) == 0 {
		ready := TunnelRuntimeIPv4State{Routing: "ready", Readiness: "ready", Carrying: true}
		for provider := range TunnelRuntimeIPv4Providers() {
			if observed.State[provider] != ready {
				t.Fatalf(
					"published IPv4 state %s = %+v, want %+v for both IPv4 providers",
					phase,
					observed.State,
					ready,
				)
			}
			if !slices.Contains(slices.Collect(maps.Values(observed.Providers)), provider) {
				t.Fatalf(
					"no client address selects IPv4 provider %s %s: %+v",
					provider,
					phase,
					observed.Providers,
				)
			}
		}
		*run.Ipv4 = observed
		t.Logf("IPv4 observations %s: %+v", phase, observed)
		return
	}
	if !reflect.DeepEqual(observed, *run.Ipv4) {
		t.Fatalf(
			"IPv4 observations %s = %+v, want the observations before the first tunnel fault %+v; daemon=%s",
			phase,
			observed,
			*run.Ipv4,
			TunnelRuntimeLog(t, daemon),
		)
	}
}

func WaitTunnelRuntimeIPv4Provider(
	t *testing.T,
	daemon *RuntimeDaemon,
	run TunnelRuntimeRun,
	port int,
	provider string,
	phase string,
) {
	t.Helper()
	deadline := time.Now().Add(TunnelRuntimeRecoverWindow)
	var observed TunnelRuntimeIPv4
	var err error
	for time.Now().Before(deadline) {
		observed, err = ObserveTunnelRuntimeIPv4(t, run, port)
		selected := slices.Collect(maps.Values(observed.Providers))
		if err == nil &&
			!slices.ContainsFunc(selected, func(link string) bool { return link != provider }) {
			return
		}
		AssertRuntimeDaemonRunning(t, daemon)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf(
		"IPv4 TCP %s = %+v, %v; want provider link %s; daemon=%s",
		phase,
		observed,
		err,
		provider,
		TunnelRuntimeLog(t, daemon),
	)
}
