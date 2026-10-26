//go:build linux && netns

package reporting_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/reporting"
)

func privateReportNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv("MWAN_REPORTING_NETNS_CHILD") == "1" {
		return true
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "-test.run=^"+t.Name()+"$", "-test.v")
	command.Env = append(os.Environ(), "MWAN_REPORTING_NETNS_CHILD=1")
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated reporting test: %v: %s", err, output)
	}
	t.Log(string(output))
	return false
}

func reportHTTPServer(t *testing.T, address string) {
	t.Helper()
	var configuration net.ListenConfig
	listener, err := configuration.Listen(t.Context(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			t.Error(err)
			return
		}
		if _, err := io.WriteString(writer, "accepted"); err != nil {
			t.Error(err)
		}
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.Errorf("HTTP receiver: %v", err)
		}
	}()
}

func reportUpload(t *testing.T, address string) {
	t.Helper()
	var dialer net.Dialer
	connection, err := dialer.DialContext(t.Context(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+address+"/", bytes.NewReader(bytes.Repeat([]byte("report-data"), 100000)))
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Write(connection); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "accepted" {
		t.Fatalf("HTTP receiver did not accept the upload: %q", data)
	}
}

func TestCollectorAttributesClientsToActualWAN(t *testing.T) {
	testCollectorAccounting(t, true)
}

func TestCollectorMarksMissingCounterAttributesIncomplete(t *testing.T) {
	testCollectorAccounting(t, false)
}

func testCollectorAccounting(t *testing.T, accountingInitiallyEnabled bool) {
	t.Helper()
	if !privateReportNamespace(t) {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	reportLink(t, "lo")
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReportNFT(t, "testdata/accounting.nft")
	initialAccounting := "0"
	if accountingInitiallyEnabled {
		initialAccounting = "1"
	}
	if err := os.WriteFile("/proc/sys/net/netfilter/nf_conntrack_acct", []byte(initialAccounting), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/netfilter/nf_conntrack_events", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	network := &networkjson.Config{InternalNetV4: "10.77.0.0/24", WAN: make(map[string]config.IfMgrWANEntry)}
	var peers []netns.NsHandle
	for index := range 2 {
		name := fmt.Sprintf("wan%d", index)
		prefix := fmt.Sprintf("192.0.%d", index+2)
		peer := reportPeer(t, gateway, name, []string{prefix + ".2/24"}, []string{prefix + ".1/24"})
		peers = append(peers, peer)
		setReportNS(t, peer)
		reportLink(t, "lo", "203.0.113.9/32")
		reportHTTPServer(t, "203.0.113.9:45678")
		setReportNS(t, gateway)
		addReportDefault(t, name, prefix+".1", prefix+".2/32", 100+index)
		network.WAN[name] = config.IfMgrWANEntry{Iface: name, TableID: 100 + index, FwMark: 11 + index, TranslationV4: &config.IPv4Translation{Mode: config.TranslationNAPT44}}
	}
	client := reportPeer(t, gateway, "lan0", []string{"10.77.0.1/24"}, []string{"10.77.0.2/24"})
	setReportNS(t, client)
	if err := netlink.RouteAdd(&netlink.Route{Gw: net.ParseIP("10.77.0.1")}); err != nil {
		t.Fatal(err)
	}
	reportHTTPServer(t, "10.77.0.2:45680")
	setReportNS(t, gateway)
	if !accountingInitiallyEnabled {
		link, err := netlink.LinkByName("wan0")
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP("192.0.2.1"), Table: 254}); err != nil {
			t.Fatal(err)
		}
		setReportNS(t, client)
		reportUpload(t, "203.0.113.9:45678")
		setReportNS(t, gateway)
		if err := os.WriteFile("/proc/sys/net/netfilter/nf_conntrack_acct", []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	settings := reportSettings(t)
	settings.ProbeInterval = "100ms"
	settings.ProbeTimeout = "100ms"
	collector, err := reporting.NewCollector(settings, network, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	for index := range 2 {
		name := fmt.Sprintf("wan%d", index)
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(fmt.Sprintf("192.0.%d.1", index+2)), Table: 254}); err != nil {
			t.Fatal(err)
		}
		setReportNS(t, client)
		reportUpload(t, "203.0.113.9:45678")
		setReportNS(t, gateway)
		start := time.Now()
		minute, err := collector.Sample(t.Context(), start, start.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		var uploaded uint64
		for _, usage := range minute.Talkers {
			if usage.WAN == connectionid.ID(name) && usage.Client == netip.MustParseAddr("10.77.0.2") {
				uploaded += usage.Uploaded
			}
		}
		for _, coverage := range minute.Attribution {
			if coverage.WAN != connectionid.ID("wan0") || index != 0 {
				continue
			}
			if !accountingInitiallyEnabled && (coverage.Complete || !strings.Contains(coverage.Reason, "byte-counter attributes")) {
				t.Fatalf("a preexisting uncounted connection appeared complete: %+v", coverage)
			}
			if accountingInitiallyEnabled && !coverage.Complete {
				t.Fatalf("normal client accounting lost coverage: %+v", coverage)
			}
		}
		if uploaded < 1000000 {
			t.Fatalf("%s did not attribute client TCP upload: %+v; coverage: %+v", name, minute.Talkers, minute.Attribution)
		}
	}
	setReportNS(t, peers[0])
	if err := netlink.RouteAdd(&netlink.Route{Dst: &net.IPNet{IP: net.ParseIP("10.77.0.0"), Mask: net.CIDRMask(24, 32)}, Gw: net.ParseIP("192.0.2.2")}); err != nil {
		t.Fatal(err)
	}
	reportUpload(t, "192.0.2.2:45679")
	setReportNS(t, gateway)
	start := time.Now()
	minute, err := collector.Sample(t.Context(), start, start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var downloaded uint64
	for _, usage := range minute.Talkers {
		if usage.WAN == connectionid.ID("wan0") && usage.Client == netip.MustParseAddr("10.77.0.2") {
			downloaded += usage.Downloaded
		}
	}
	if downloaded < 1000000 {
		t.Fatalf("inbound port forwarding did not identify the LAN client: %+v; coverage: %+v", minute.Talkers, minute.Attribution)
	}
}

func reportLink(t *testing.T, name string, addresses ...string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range addresses {
		address, err := netlink.ParseAddr(text)
		if err != nil {
			t.Fatal(err)
		}
		address.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(link, address); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}

func setReportNS(t *testing.T, namespace netns.NsHandle) {
	t.Helper()
	if err := netns.Set(namespace); err != nil {
		t.Fatal(err)
	}
}

func reportPeer(t *testing.T, gateway netns.NsHandle, name string, rootAddresses, peerAddresses []string) netns.NsHandle {
	t.Helper()
	peer, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	setReportNS(t, gateway)
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: name + "peer"}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(name + "peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(link, int(peer)); err != nil {
		t.Fatal(err)
	}
	reportLink(t, name, rootAddresses...)
	setReportNS(t, peer)
	reportLink(t, "lo")
	reportLink(t, name+"peer", peerAddresses...)
	setReportNS(t, gateway)
	t.Cleanup(func() { _ = peer.Close() })
	return peer
}

func serveReportDNS(t *testing.T, address string) {
	t.Helper()
	var configuration net.ListenConfig
	connection, err := configuration.ListenPacket(t.Context(), "udp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	go func() {
		buffer := make([]byte, 1500)
		for {
			count, peer, err := connection.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buffer[:count]); err != nil || len(query.Questions) != 1 {
				continue
			}
			question := query.Questions[0]
			reply := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true}, Questions: query.Questions}
			header := dnsmessage.ResourceHeader{Name: question.Name, Type: question.Type, Class: dnsmessage.ClassINET, TTL: 60}
			if question.Type == dnsmessage.TypeA {
				reply.Answers = []dnsmessage.Resource{{Header: header, Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 9}}}}
			}
			if question.Type == dnsmessage.TypeAAAA {
				reply.Answers = []dnsmessage.Resource{{Header: header, Body: &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr("2001:db8:ffff::9").As16()}}}
			}
			data, err := reply.Pack()
			if err != nil {
				continue
			}
			if _, err := connection.WriteTo(data, peer); err != nil {
				return
			}
		}
	}()
}

func addReportDefault(t *testing.T, iface, gateway, source string, table int) {
	t.Helper()
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(gateway), Table: table}); err != nil {
		t.Fatal(err)
	}
	_, prefix, err := net.ParseCIDR(source)
	if err != nil {
		t.Fatal(err)
	}
	rule := netlink.NewRule()
	rule.Table = table
	rule.Src = prefix
	if prefix.IP.To4() == nil {
		rule.Family = netlink.FAMILY_V6
	}
	if err := netlink.RuleAdd(rule); err != nil {
		t.Fatal(err)
	}
}

func runReportNFT(t *testing.T, path string) {
	t.Helper()
	if output, err := exec.CommandContext(t.Context(), "nft", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("nft: %v: %s", err, output)
	}
}

func TestCollectorBindsWANProbesAndDetectsBlackholes(t *testing.T) {
	if !privateReportNamespace(t) {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	reportLink(t, "lo")
	network := &networkjson.Config{WAN: make(map[string]config.IfMgrWANEntry)}
	for index := range 2 {
		name := fmt.Sprintf("wan%d", index)
		v4 := fmt.Sprintf("192.0.%d", index+2)
		v6 := fmt.Sprintf("2001:db8:%d", index+1)
		peer := reportPeer(t, gateway, name, []string{v4 + ".2/24", v6 + "::2/64"}, []string{v4 + ".1/24", v6 + "::1/64"})
		setReportNS(t, peer)
		reportLink(t, "lo", "203.0.113.9/32", "2001:db8:ffff::9/128")
		serveReportDNS(t, "203.0.113.9:53")
		serveReportDNS(t, "[2001:db8:ffff::9]:53")
		setReportNS(t, gateway)
		addReportDefault(t, name, v4+".1", v4+".2/32", 100+index)
		addReportDefault(t, name, v6+"::1", v6+"::2/128", 100+index)
		network.WAN[name] = config.IfMgrWANEntry{Iface: name, TableID: 100 + index, FwMark: 10 + index, TranslationV4: &config.IPv4Translation{Mode: config.TranslationNative}, TranslationV6: &config.IPv6Translation{Mode: config.TranslationNative}}
	}
	settings := reportSettings(t)
	settings.SampleInterval = "1s"
	settings.ProbeInterval = "100ms"
	settings.ProbeTimeout = "5s"
	settings.BreachWindow = "1s"
	settings.ReminderInterval = "1s"
	settings.RecoveryInterval = "1s"
	settings.MinProbeSeconds = 1
	settings.DNSv4 = "203.0.113.9:53"
	settings.DNSv6 = "[2001:db8:ffff::9]:53"
	collector, err := reporting.NewCollector(settings, network, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	start := time.Now()
	minute, err := collector.Sample(t.Context(), start, start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range minute.Probes {
		if !probe.Available || probe.Replies != 10 {
			t.Fatalf("WAN-bound probe failed: %+v", probe)
		}
	}
	runReportNFT(t, "testdata/blackhole.nft")
	start = time.Now()
	minute, err = collector.Sample(t.Context(), start, start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range minute.Probes {
		if probe.Sent != 10 {
			t.Fatalf("timeouts reduced probe cadence: %+v", probe)
		}
		if probe.Key.WAN == connectionid.ID("wan1") && probe.Lost != 10 {
			t.Fatalf("blackhole did not count every unanswered request: %+v", probe)
		}
		if probe.Key.WAN == connectionid.ID("wan0") && probe.Replies != 10 {
			t.Fatalf("one WAN failure affected another WAN: %+v", probe)
		}
	}
	session, err := netif.OpenEchoSession(t.Context(), "wan0", netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.1"), 47000)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			if _, err := session.Probe(t.Context(), time.Second); err != nil {
				t.Errorf("concurrent persistent request: %v", err)
			}
		})
	}
	workers.Wait()
	if err := collector.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatal(err)
	}
}
