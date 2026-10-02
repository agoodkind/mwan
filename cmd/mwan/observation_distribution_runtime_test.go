//go:build linux && firewallnetns

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/observation"
)

func TestDistributionObservationDaemonRuntime(t *testing.T) {
	binary := protocolTestBinary(t)
	machineID, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	gateway, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	defer setRuntimeNamespace(t, original)
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "observebr"}}
	if err := netlink.LinkAdd(bridge); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, "observebr", []string{"192.0.2.1/24", "2001:db8:123::1/64"})
	setRuntimeLoopback(t)
	observer := observation.Endpoint{Kind: observation.EndpointLocal, MachineID: strings.TrimSpace(string(machineID))}
	var providers []observation.ProviderIngress
	var requests4, requests6 []observation.CheckSpec
	for index, addresses := range [][]string{{"192.0.2.2/24", "2001:db8:123::2/64"}, {"192.0.2.3/24", "2001:db8:123::3/64"}} {
		connectionID, portName := "first", "portfirst"
		if index == 1 {
			connectionID, portName = "second", "portsecond"
		}
		peer := newRuntimePeer(t, gateway, portName, "provider", nil, addresses, "")
		defer peer.namespace.Close()
		port, err := netlink.LinkByName(portName)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetMaster(port, bridge); err != nil {
			t.Fatal(err)
		}
		setRuntimeNamespace(t, peer.namespace)
		device, err := netlink.LinkByName("provider")
		if err != nil {
			t.Fatal(err)
		}
		mac := device.Attrs().HardwareAddr
		for _, family := range []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6} {
			address, network := strings.Split(addresses[0], "/")[0], "tcp4"
			if family == observation.FamilyIPv6 {
				address, network = strings.Split(addresses[1], "/")[0], "tcp6"
			}
			listener, err := net.Listen(network, net.JoinHostPort(address, "0"))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("provider-application-ok"))
			}))
			server.Listener = listener
			server.Start()
			defer server.Close()
			request := observation.CheckSpec{ID: connectionID + string(family), Dimension: observation.DimensionDownstreamApplication, Operation: observation.OperationHTTP, Observer: observer, Family: family, Target: server.URL, TimeoutSeconds: 1, MaxAgeSeconds: 30, ExpectedHTTPStatus: []int{200}, ExpectedBody: "provider-application-ok"}
			if family == observation.FamilyIPv4 {
				request.Source = netip.MustParseAddr("192.0.2.1")
				requests4 = append(requests4, request)
			} else {
				request.Source = netip.MustParseAddr("2001:db8:123::1")
				requests6 = append(requests6, request)
			}
		}
		setRuntimeNamespace(t, gateway)
		if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: port.Attrs().Index, Family: unix.AF_BRIDGE, State: unix.NUD_PERMANENT, Flags: unix.NTF_MASTER, HardwareAddr: mac}); err != nil {
			t.Fatal(err)
		}
		providers = append(providers, observation.ProviderIngress{ConnectionID: connectionID, Bridge: "observebr", PortInterface: portName, DestinationMAC: mac.String(), Tier: 1, Weight: index + 1, Eligible: true})
	}
	for _, family := range []observation.Family{observation.FamilyIPv4, observation.FamilyIPv6} {
		t.Run(string(family), func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			setRuntimeNamespace(t, gateway)
			defer setRuntimeNamespace(t, original)
			requests := requests4
			if family == observation.FamilyIPv6 {
				requests = requests6
			}
			now := time.Now().UTC()
			familyProviders := slices.Clone(providers)
			for index := range familyProviders {
				familyProviders[index].Family, familyProviders[index].ObservedAt = family, now
			}
			plan := &observation.DistributionPlan{HashMode: "source-destination", ActiveTier: 1, ObservedAt: now, MinimumSamplesPerProvider: 1, Providers: familyProviders, Requests: requests}
			spec := observation.CheckSpec{ID: "provider-distribution", Dimension: observation.DimensionConnectionDistribution, Operation: observation.OperationDistribution, Observer: observer, Family: family, Target: "configured-provider-ingress", TimeoutSeconds: 4, MaxAgeSeconds: 30, DistributionSamples: 2, DistributionPlan: plan}
			result := runObservationProcess(t, binary, spec)
			if !observation.RequiredPassed(spec, result, time.Now()) {
				t.Fatalf("actual HTTP and provider ingress did not pass: %+v", result)
			}
			for _, share := range result.DistributionProviders {
				if share.Samples != 1 {
					t.Fatalf("wrong measured provider count: %+v", share)
				}
			}
			plan.HashMode = "random"
			plan.Requests = requests[:1]
			missingProvider := runObservationProcess(t, binary, spec)
			if missingProvider.Availability != observation.AvailabilityComplete || missingProvider.Outcome != observation.OutcomeFail || observation.RequiredPassed(spec, missingProvider, time.Now()) {
				t.Fatalf("omitted eligible provider did not fail: %+v", missingProvider)
			}
			plan.HashMode = "source"
			insufficientDiversity := runObservationProcess(t, binary, spec)
			if insufficientDiversity.Availability != observation.AvailabilityMissing || insufficientDiversity.Outcome != observation.OutcomeUnknown {
				t.Fatalf("insufficient source diversity became a balancing failure: %+v", insufficientDiversity)
			}
			plan.Providers[0].DestinationMAC = "02:00:00:00:00:99"
			unverified := runObservationProcess(t, binary, spec)
			if unverified.Availability != observation.AvailabilityError || unverified.Outcome != observation.OutcomeUnknown {
				t.Fatalf("unverified kernel mapping became measured distribution: %+v", unverified)
			}
		})
	}
}
