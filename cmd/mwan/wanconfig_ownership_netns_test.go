//go:build linux && netns

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

const ownershipChildEnv = "MWAN_OWNERSHIP_TEST_CHILD"

func TestOwnershipOperationalReadNetNS(t *testing.T) {
	if os.Getenv(ownershipChildEnv) == "1" {
		runOwnershipOperationalChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace requires root")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestOwnershipOperationalReadNetNS$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), ownershipChildEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("private namespace operational read: %v: %s", err, output)
	}
}

func runOwnershipOperationalChild(t *testing.T) {
	const hardwareAddress = "02:00:5e:00:53:61"
	link := ownershipLink(t, hardwareAddress)
	address, err := netlink.ParseAddr("192.0.2.61/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logPath := filepath.Join(t.TempDir(), "ifmgr.jsonl")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	log := slog.New(slog.NewJSONHandler(logFile, nil))
	gateway := selftestGateway()
	gateway.Connections[2].ID = "stable-example"
	gateway.Connections[2].Link = &interfaceintent.Link{Kind: interfaceintent.KindPhysical, Match: interfaceintent.Match{HardwareAddress: hardwareAddress}}
	modelsDir := selftestModelsDir(t)
	reader, closeRepository, err := openPrivateRepository(ctx, log, selftestFlags{repository: filepath.Join(t.TempDir(), "repository"), modelsDir: modelsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepository()
	provider, err := yangpub.New(log)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	if err := wanconfig.Publish(ctx, log, runningReplacer{pub: provider}, gateway); err != nil {
		t.Fatal(err)
	}
	for _, module := range publishedModules {
		if err := provider.OwnModule(ctx, module); err != nil {
			t.Fatal(err)
		}
	}
	store := wanstate.New()
	store.SetTransitionLogger("first-run", log)
	monitorCtx, stopMonitor := context.WithCancelCause(ctx)
	startOwnershipObservers(monitorCtx, log, store, gateway.Connections, stopMonitor)
	defer stopMonitor(nil)
	store.SetRouting(0, map[string]wanstate.MemberRouting{"stable-example": {Carrying: true, V4Ready: true, V6Ready: false, OwnedAddresses: nil}})
	if err := registerLiveStateProviders(ctx, log, provider, store, gateway, nil); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		return state.IfIndex == link.Attrs().Index && len(state.IPv4.Addresses) > 0
	})
	read := func() string {
		t.Helper()
		value, found, readErr := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-interfaces:*")
		if readErr != nil || !found {
			t.Fatalf("second operational read: found=%v err=%v", found, readErr)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(value)); err != nil {
			t.Fatal(err)
		}
		return compact.String()
	}
	initial := read()
	for _, field := range []string{`"connection-id":"stable-example"`, `"configured-owner":"external"`, `"cidr":"192.0.2.61/32"`, `"acquisition":"unknown"`, `"assignment-validity":"unknown"`, `"readiness":"unknown"`, `"routing":"ready"`} {
		if !strings.Contains(initial, field) {
			t.Fatalf("operational state lacks %s: %s", field, initial)
		}
	}
	if strings.Contains(initial, `"assignment":[`) {
		t.Fatalf("kernel address invented acquisition: %s", initial)
	}
	verifyIPv6OperationalObservation(t, store, link, read)
	secondAddress, err := netlink.ParseAddr("192.0.2.62/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, secondAddress); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool { return len(state.IPv4.Addresses) == 2 })
	secondID := operationalIDFor(t, read(), "cidr", "192.0.2.62/32")
	if err := netlink.AddrDel(link, address); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		return state.IfIndex == link.Attrs().Index && len(state.IPv4.Addresses) == 1
	})
	if current := read(); strings.Contains(current, `"cidr":"192.0.2.61/32"`) || operationalIDFor(t, current, "cidr", "192.0.2.62/32") != secondID {
		t.Fatalf("remaining address changed identity: %s", current)
	}
	secondAddress.PreferedLft = 0
	secondAddress.ValidLft = 3600
	if err := netlink.AddrReplace(link, secondAddress); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv4.Addresses {
			if observed.CIDR == "192.0.2.62/32" && observed.PreferredLifetime == 0 {
				return true
			}
		}
		return false
	})
	if current := read(); operationalIDFor(t, current, "cidr", "192.0.2.62/32") != secondID || !strings.Contains(current, `"preferred-lifetime":"0"`) || !strings.Contains(current, `flags or validity changed"`) {
		t.Fatalf("deprecated address changed identity or validity: %s", current)
	}
	_, destination, err := net.ParseCIDR("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: destination, Family: netlink.FAMILY_V4, Table: 123, Protocol: 42, Scope: netlink.SCOPE_LINK, Priority: 77}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv4.Routes {
			if observed.Dest == destination.String() && observed.TableID == 123 && observed.Protocol == 42 {
				return true
			}
		}
		return false
	})
	if current := read(); !strings.Contains(current, `"destination":"203.0.113.0/24"`) || !strings.Contains(current, `"table-id":123`) {
		t.Fatalf("route identity absent: %s", current)
	}
	_, secondDestination, err := net.ParseCIDR("203.0.114.0/24")
	if err != nil {
		t.Fatal(err)
	}
	secondRoute := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: secondDestination, Family: netlink.FAMILY_V4, Table: 124, Protocol: 43, Scope: netlink.SCOPE_LINK, Priority: 88}
	if err := netlink.RouteAdd(secondRoute); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv4.Routes {
			if observed.Dest == secondDestination.String() && observed.TableID == 124 {
				return true
			}
		}
		return false
	})
	secondRouteID := operationalIDFor(t, read(), "destination", secondDestination.String())
	if err := netlink.RouteDel(route); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv4.Routes {
			if observed.Dest == destination.String() && observed.TableID == 123 {
				return false
			}
		}
		return true
	})
	if current := read(); operationalIDFor(t, current, "destination", secondDestination.String()) != secondRouteID {
		t.Fatalf("remaining route changed identity: %s", current)
	}
	oldIndex := link.Attrs().Index
	if err := netlink.LinkDel(link); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool { return state.IfIndex == 0 && state.LinkState == "absent" })
	replacement := ownershipLink(t, hardwareAddress)
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		return state.IfIndex == replacement.Attrs().Index && state.LinkState == "up"
	})
	if replacement.Attrs().Index == oldIndex {
		t.Fatalf("replacement reused index %d", oldIndex)
	}
	if current := read(); !strings.Contains(current, fmt.Sprintf(`"actual-index":%d`, replacement.Attrs().Index)) {
		t.Fatalf("replacement index absent: %s", current)
	}
	stopMonitor(nil)
	verifyOverflowOperationalRead(t, ctx, log, logFile, store, &gateway.Connections[2], replacement, read)
	firstAssignment := ownershipTestAssignment("192.0.2.10/32")
	secondAssignment := ownershipTestAssignment("192.0.2.11/32")
	store.SetAssignment("stable-example", "ipv4", "acquired", "valid", []interfaceintent.Assignment{firstAssignment, secondAssignment})
	secondAssignmentID := operationalIDFor(t, read(), "value", secondAssignment.Value.String())
	store.SetAssignment("stable-example", "ipv4", "acquired", "valid", []interfaceintent.Assignment{secondAssignment})
	if current := read(); operationalIDFor(t, current, "value", secondAssignment.Value.String()) != secondAssignmentID {
		t.Fatalf("remaining assignment changed identity: %s", current)
	}
	secondStore := wanstate.New()
	secondStore.SetTransitionLogger("second-run", log)
	secondCtx, stopSecond := context.WithCancelCause(ctx)
	defer stopSecond(nil)
	startOwnershipObservers(secondCtx, log, secondStore, gateway.Connections, stopSecond)
	waitOwnershipState(t, secondStore, func(state wanstate.ConnectionState) bool { return state.IfIndex == replacement.Attrs().Index })
	if len(secondStore.Snapshot().Connections["stable-example"].Recent) != 0 {
		t.Fatal("startup snapshot was treated as a transition")
	}
	if err := logFile.Sync(); err != nil {
		t.Fatal(err)
	}
	history, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(history), `first-run:`) {
		t.Fatalf("link replacement missing from persistent history: %s", history)
	}
	for _, operation := range []string{`"Operation":"observe-address"`, `"Operation":"observe-route"`, `"Operation":"observe-link"`} {
		if !strings.Contains(string(history), operation) {
			t.Fatalf("persistent history lacks %s: %s", operation, history)
		}
	}
	if strings.Contains(string(history), `second-run:`) {
		t.Fatalf("startup generated a transition: %s", history)
	}
}

func verifyIPv6OperationalObservation(t *testing.T, store *wanstate.Store, link netlink.Link, read func() string) {
	t.Helper()
	const cidr = "2001:db8:517::61/64"
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+link.Attrs().Name+"/dad_transmits", []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	address.PreferedLft = 3600
	address.ValidLft = 7200
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv6.Addresses {
			if observed.CIDR == cidr && observed.Flags&netif.IFAFTentative != 0 {
				return true
			}
		}
		return false
	})
	if phase := operationalAddressField(t, read(), cidr, "phase"); phase != "tentative" {
		t.Fatalf("observed IPv6 phase = %q, want tentative", phase)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv6.Addresses {
			if observed.CIDR == cidr && observed.Flags&netif.IFAFTentative == 0 {
				return true
			}
		}
		return false
	})
	if phase := operationalAddressField(t, read(), cidr, "phase"); phase != "usable" {
		t.Fatalf("observed IPv6 phase = %q, want usable", phase)
	}
	if origin := operationalAddressField(t, read(), cidr, "origin"); origin != "unknown" {
		t.Fatalf("observed IPv6 origin = %q, want unknown", origin)
	}
	addressID := operationalIDFor(t, read(), "cidr", cidr)
	address.PreferedLft = 0
	if err := netlink.AddrReplace(link, address); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv6.Addresses {
			if observed.CIDR == cidr && observed.PreferredLifetime == 0 {
				return true
			}
		}
		return false
	})
	if phase := operationalAddressField(t, read(), cidr, "phase"); phase != "deprecated" {
		t.Fatalf("observed IPv6 phase = %q, want deprecated", phase)
	}
	if id := operationalIDFor(t, read(), "cidr", cidr); id != addressID {
		t.Fatalf("observed IPv6 address ID changed from %s to %s", addressID, id)
	}
	if current := read(); !strings.Contains(current, `"router-validity":"absent"`) {
		t.Fatalf("fresh observation lacks absent router validity: %s", current)
	}
	peer, err := netlink.LinkByName("ownership-peer")
	if err != nil {
		t.Fatal(err)
	}
	peerAddress, err := netlink.ParseAddr("fe80::1/64")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(peer, peerAddress); err != nil {
		t.Fatal(err)
	}
	_, destination, err := net.ParseCIDR("::/0")
	if err != nil {
		t.Fatal(err)
	}
	staticDefault := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: destination, Gw: net.ParseIP("fe80::1"), Family: netlink.FAMILY_V6, Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_STATIC, Priority: 4600}
	if err := netlink.RouteAdd(staticDefault); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, route := range state.IPv6.Routes {
			if route.TableID == unix.RT_TABLE_MAIN && route.Protocol == unix.RTPROT_STATIC && route.Dest == "default" {
				return true
			}
		}
		return false
	})
	if current := read(); !strings.Contains(current, `"router-validity":"absent"`) {
		t.Fatalf("static default route changed RA router validity: %s", current)
	}
	defaultRoute := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: destination, Gw: net.ParseIP("fe80::1"), Family: netlink.FAMILY_V6, Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_RA, Priority: 4700}
	if err := netlink.RouteAdd(defaultRoute); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, route := range state.IPv6.Routes {
			if route.TableID == unix.RT_TABLE_MAIN && route.Protocol == unix.RTPROT_RA && route.Dest == "default" {
				return true
			}
		}
		return false
	})
	if current := read(); !strings.Contains(current, `"router-validity":"present"`) {
		t.Fatalf("fresh RA route lacks present router validity: %s", current)
	}
	if err := netlink.RouteDel(defaultRoute); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, route := range state.IPv6.Routes {
			if route.TableID == unix.RT_TABLE_MAIN && route.Protocol == unix.RTPROT_RA && route.Dest == "default" {
				return false
			}
		}
		return true
	})
	if current := read(); !strings.Contains(current, `"router-validity":"absent"`) {
		t.Fatalf("removed RA route remains present in operational state: %s", current)
	}
	if err := netlink.RouteDel(staticDefault); err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrDel(link, address); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv6.Addresses {
			if observed.CIDR == cidr {
				return false
			}
		}
		return true
	})
	if current := read(); strings.Contains(current, `"cidr":"`+cidr+`"`) {
		t.Fatalf("removed IPv6 address remains in operational state: %s", current)
	}
	verifyIPv6DADFailure(t, store, link, peer, read)
}

func verifyIPv6DADFailure(t *testing.T, store *wanstate.Store, link, peer netlink.Link, read func() string) {
	t.Helper()
	const cidr = "2001:db8:517::d/128"
	peerAddress, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	peerAddress.Flags = netif.IFAFNoDAD
	if err := netlink.AddrAdd(peer, peerAddress); err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	waitOwnershipState(t, store, func(state wanstate.ConnectionState) bool {
		for _, observed := range state.IPv6.Addresses {
			if observed.CIDR == cidr && observed.Flags&netif.IFAFDADFailed != 0 {
				return true
			}
		}
		return false
	})
	if phase := operationalAddressField(t, read(), cidr, "phase"); phase != "dad-failed" {
		t.Fatalf("observed IPv6 phase = %q, want dad-failed", phase)
	}
}

func ownershipTestAssignment(prefix string) interfaceintent.Assignment {
	return interfaceintent.Assignment{
		ConnectionID: "stable-example", Family: "ipv4", Kind: interfaceintent.AssignmentStatic,
		Source: "networkd", Purpose: interfaceintent.PurposeLocal, Value: netip.MustParsePrefix(prefix),
		ClientID: "", DUID: "", IAID: nil, AcquiredAt: time.Time{}, RenewAt: nil, RebindAt: nil,
		PreferredUntil: nil, ValidUntil: nil, Valid: true,
	}
}

func verifyOverflowOperationalRead(t *testing.T, ctx context.Context, log *slog.Logger, logFile *os.File, store *wanstate.Store, connection *interfaceintent.Connection, link netlink.Link, read func() string) {
	t.Helper()
	base, err := netlink.ParseAddr("192.0.2.99/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, base); err != nil {
		t.Fatal(err)
	}
	const ipv6CIDR = "2001:db8:517::99/64"
	ipv6Address, err := netlink.ParseAddr(ipv6CIDR)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, ipv6Address); err != nil {
		t.Fatal(err)
	}
	monitorCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	monitor := netif.NewMonitor(monitorCtx, log, netif.MonitorConfig{Iface: connection.Name, ConnectionID: connection.ID.String(), Connection: connection})
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for baseline := false; !baseline; {
		select {
		case event := <-monitor.Events:
			if event.Kind == netif.EvResync {
				store.RecordObservation(event)
				baseline = true
			}
		case <-deadline.C:
			t.Fatal("monitor did not publish an initial snapshot")
		}
	}
	if current := read(); !strings.Contains(current, `"cidr":"192.0.2.99/32"`) {
		t.Fatalf("initial snapshot lacks known address: %s", current)
	}
	for i := 1; i <= 160; i++ {
		address, parseErr := netlink.ParseAddr(fmt.Sprintf("198.18.2.%d/32", i))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if addErr := netlink.AddrAdd(link, address); addErr != nil {
			t.Fatal(addErr)
		}
	}
	for {
		if err := logFile.Sync(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(logFile.Name())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("monitor: Events channel full; requesting snapshot")) {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("real kernel address burst did not overflow monitor queue")
		case <-time.After(20 * time.Millisecond):
		}
	}
	var staleAt time.Time
	select {
	case event := <-monitor.Events:
		if event.Kind != netif.EvObservationStale || event.Reason != "event queue overflow" {
			t.Fatalf("first queued event after overflow = %+v", event)
		}
		staleAt = event.ObservedAt
		store.RecordObservation(event)
	case <-deadline.C:
		t.Fatal("monitor did not publish stale state")
	}
	stale := read()
	if staleAt.IsZero() || !strings.Contains(stale, `"observation":"stale"`) || !strings.Contains(stale, `"observation-reason":"event queue overflow"`) || !strings.Contains(stale, staleAt.UTC().Format(time.RFC3339Nano)) || !strings.Contains(stale, `"cidr":"192.0.2.99/32"`) || !strings.Contains(stale, `"acquisition":"unknown"`) || !strings.Contains(stale, `"router-validity":"unknown"`) {
		t.Fatalf("stale operational read changed known state: %s", stale)
	}
	if phase := operationalAddressField(t, stale, ipv6CIDR, "phase"); phase != "unknown" {
		t.Fatalf("stale IPv6 address phase = %q, want unknown", phase)
	}
	for {
		select {
		case event := <-monitor.Events:
			store.RecordObservation(event)
			if event.Kind == netif.EvResync && event.Snapshot != nil {
				fresh := read()
				if !strings.Contains(fresh, `"observation":"fresh"`) || !strings.Contains(fresh, `"cidr":"198.18.2.160/32"`) {
					t.Fatalf("recovery snapshot absent from operational read: %s", fresh)
				}
				return
			}
		case <-deadline.C:
			t.Fatal("monitor did not recover with a full snapshot")
		}
	}
}

func ownershipLink(t *testing.T, hardwareAddress string) netlink.Link {
	t.Helper()
	link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "enexample0"}, PeerName: "ownership-peer"}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	actual, err := netlink.LinkByName("enexample0")
	if err != nil {
		t.Fatal(err)
	}
	mac, err := net.ParseMAC(hardwareAddress)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetHardwareAddr(actual, mac); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(actual); err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.LinkByName("ownership-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(peer); err != nil {
		t.Fatal(err)
	}
	return actual
}

func waitOwnershipState(t *testing.T, store *wanstate.Store, accept func(wanstate.ConnectionState) bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if accept(store.Snapshot().Connections["stable-example"]) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("ownership state did not update: %+v", store.Snapshot().Connections["stable-example"])
		case <-ticker.C:
		}
	}
}

func operationalIDFor(t *testing.T, document, field, value string) string {
	t.Helper()
	var root any
	if err := json.Unmarshal([]byte(document), &root); err != nil {
		t.Fatal(err)
	}
	var find func(any) string
	find = func(node any) string {
		switch item := node.(type) {
		case map[string]any:
			if item[field] == value {
				if id, ok := item["id"].(string); ok {
					return id
				}
			}
			for _, child := range item {
				if id := find(child); id != "" {
					return id
				}
			}
		case []any:
			for _, child := range item {
				if id := find(child); id != "" {
					return id
				}
			}
		}
		return ""
	}
	id := find(root)
	if id == "" {
		t.Fatalf("operational state lacks %s=%s: %s", field, value, document)
	}
	return id
}

func operationalAddressField(t *testing.T, document, cidr, field string) string {
	t.Helper()
	var root any
	if err := json.Unmarshal([]byte(document), &root); err != nil {
		t.Fatal(err)
	}
	var find func(any) (string, bool)
	find = func(node any) (string, bool) {
		switch item := node.(type) {
		case map[string]any:
			if item["cidr"] == cidr {
				value, ok := item[field].(string)
				return value, ok
			}
			for _, child := range item {
				if value, ok := find(child); ok {
					return value, true
				}
			}
		case []any:
			for _, child := range item {
				if value, ok := find(child); ok {
					return value, true
				}
			}
		}
		return "", false
	}
	value, ok := find(root)
	if !ok {
		t.Fatalf("operational address %s lacks %s: %s", cidr, field, document)
	}
	return value
}
