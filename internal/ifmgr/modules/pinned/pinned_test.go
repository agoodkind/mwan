package pinned

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"goodkind.io/mwan/internal/ifmgr"
)

// testClock is the wall clock the refresh cadence reads, so a test moves time
// instead of waiting six hours.
type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time { return c.now }

const (
	nameV4 = "pinned-v4.test."
	nameV6 = "pinned-v6.test."

	// insideTheSeed is one of the resolved addresses and lies inside the
	// configured seed range, which is the overlap the kernel would refuse as
	// two elements.
	insideTheSeed  = "208.54.1.2"
	outsideTheSeed = "198.51.100.7"
)

// startFeedServer serves the captured published prefix list, byte for byte as
// the real feed returns it, so the parser is exercised against real content.
func startFeedServer(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "zoom-ipranges.txt"))
	if err != nil {
		t.Fatalf("read the captured feed: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := w.Write(body); err != nil {
			t.Errorf("serve the feed: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// startDNSServer answers A and AAAA questions for the test zone over UDP, and
// returns a resolver that asks it. The module runs its ordinary net.Resolver
// against a real server rather than a substitute lookup function.
func startDNSServer(t *testing.T, zone map[string][]netip.Addr) *net.Resolver {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for DNS: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go serveDNS(conn, zone)

	address := conn.LocalAddr().String()
	return &net.Resolver{
		PreferGo:     true,
		StrictErrors: false,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "udp", address)
		},
	}
}

func serveDNS(conn net.PacketConn, zone map[string][]netip.Addr) {
	buffer := make([]byte, 1232)
	for {
		read, from, err := conn.ReadFrom(buffer)
		if err != nil {
			return
		}
		response, err := dnsResponse(buffer[:read], zone)
		if err != nil {
			continue
		}
		if _, err := conn.WriteTo(response, from); err != nil {
			return
		}
	}
}

func dnsResponse(query []byte, zone map[string][]netip.Addr) ([]byte, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil {
		return nil, err
	}
	question, err := parser.Question()
	if err != nil {
		return nil, err
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		OpCode:             0,
		Authoritative:      true,
		Truncated:          false,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		AuthenticData:      false,
		CheckingDisabled:   false,
		RCode:              dnsmessage.RCodeSuccess,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(question); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	resourceHeader := dnsmessage.ResourceHeader{
		Name:   question.Name,
		Type:   0,
		Class:  question.Class,
		TTL:    60,
		Length: 0,
	}
	for _, addr := range zone[question.Name.String()] {
		switch {
		case question.Type == dnsmessage.TypeA && addr.Is4():
			if err := builder.AResource(resourceHeader, dnsmessage.AResource{A: addr.As4()}); err != nil {
				return nil, err
			}
		case question.Type == dnsmessage.TypeAAAA && addr.Is6() && !addr.Is4In6():
			if err := builder.AAAAResource(resourceHeader, dnsmessage.AAAAResource{AAAA: addr.As16()}); err != nil {
				return nil, err
			}
		}
	}
	return builder.Finish()
}

func testZone(t *testing.T) map[string][]netip.Addr {
	t.Helper()
	return map[string][]netip.Addr{
		nameV4: {netip.MustParseAddr(insideTheSeed), netip.MustParseAddr(outsideTheSeed)},
		nameV6: {netip.MustParseAddr("2001:db8::1")},
	}
}

func testConfig(feedURL string) Config {
	return Config{
		Enabled:         true,
		RefreshInterval: 6 * time.Hour,
		RefreshTimeout:  10 * time.Second,
		FeedURL:         feedURL,
		SeedCIDRsV4:     []string{"208.54.0.0/16"},
		SeedCIDRsV6:     []string{"2600:1000::/28"},
		FQDNsV4:         []string{nameV4},
		FQDNsV6:         []string{nameV6},
	}
}

// newTestModule builds the module the way the daemon does, with the netlink
// connection recorded, the wall clock driven by the test, and the resolver
// pointed at the test's own DNS server.
func newTestModule(t *testing.T, cfg Config, conn *recordedConn, clock *testClock) *Module {
	t.Helper()
	constructed, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	module, ok := constructed.(*Module)
	if !ok {
		t.Fatalf("New returned %T, want *Module", constructed)
	}
	module.apply = applierOn(conn)
	module.clock = clock
	module.resolver = startDNSServer(t, testZone(t))

	env := &ifmgr.Env{
		Iface: "", Sysctl: nil, Log: slog.Default(), Alerts: nil, Monitor: nil,
		DHCP: nil, RA: nil, RequestReconcile: nil, LiveState: nil,
	}
	if err := module.Init(context.Background(), env); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return module
}

// TestReconcileWritesEverySourceInOneTransaction drives the module's own entry
// point end to end: the seeds, the resolved names, and the published feed
// reach both sets in one committed batch.
func TestReconcileWritesEverySourceInOneTransaction(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	clock := &testClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	module := newTestModule(t, testConfig(startFeedServer(t)), conn, clock)

	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if conn.flushCount != 1 {
		t.Fatalf("Flush called %d times, want exactly 1", conn.flushCount)
	}
	assertSequence(t, conn.ops, []string{
		"getset:" + setV4Name, "addset:" + setV4Name, "flushset:" + setV4Name, "addelements:" + setV4Name,
		"getset:" + setV6Name, "addset:" + setV6Name, "flushset:" + setV6Name, "addelements:" + setV6Name,
		"flush",
	})

	keysV4 := elementKeys(t, conn.elements[setV4Name])
	for _, want := range []string{
		// The seed range.
		"208.54.0.0", "208.55.0.0 (end)",
		// The resolved name's address outside that range.
		outsideTheSeed, "198.51.100.8 (end)",
	} {
		if !slices.Contains(keysV4, want) {
			t.Errorf("IPv4 elements are missing %q: %v", want, keysV4)
		}
	}
	// The resolved address inside the seed range must not be its own element:
	// the kernel refuses an element that overlaps one in the same transaction.
	if slices.Contains(keysV4, insideTheSeed) {
		t.Errorf("IPv4 elements include %s, which the seed range already covers: %v",
			insideTheSeed, keysV4)
	}

	assertSequence(t, elementKeys(t, conn.elements[setV6Name]), []string{
		"2001:db8::1", "2001:db8::2 (end)",
		"2600:1000::", "2600:1010:: (end)",
	})
}

// TestReconcileWritesTheFeedPrefixes is the defect this module closes: the
// shell refresher's filter matched no line of this exact feed body, so no
// published prefix has ever reached the set. Here the first and last prefix of
// the captured feed are both in the batch.
func TestReconcileWritesTheFeedPrefixes(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	clock := &testClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	module := newTestModule(t, testConfig(startFeedServer(t)), conn, clock)

	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	keysV4 := elementKeys(t, conn.elements[setV4Name])
	for _, want := range []string{
		// 3.7.35.0/25, the first prefix of the captured feed.
		"3.7.35.0", "3.7.35.128 (end)",
		// 221.123.139.192/27, its last.
		"221.123.139.192", "221.123.139.224 (end)",
		// 134.224.0.0/16, one from the middle.
		"134.224.0.0", "134.225.0.0 (end)",
	} {
		if !slices.Contains(keysV4, want) {
			t.Errorf("IPv4 elements are missing the feed's %q: %v", want, keysV4)
		}
	}
	// 15.220.80.0/24 and 15.220.81.0/25 are adjacent in the captured feed, so
	// they are written as one range rather than two abutting ones.
	if slices.Contains(keysV4, "15.220.81.0") {
		t.Errorf("adjacent feed prefixes were not merged: %v", keysV4)
	}
	if !slices.Contains(keysV4, "15.220.81.128 (end)") {
		t.Errorf("the merged feed range does not end where both prefixes do: %v", keysV4)
	}
}

// TestReconcileKeepsTheCadence proves the module refreshes on its own six-hour
// cadence rather than on every daemon tick, and that a due refresh runs again.
func TestReconcileKeepsTheCadence(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	clock := &testClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	module := newTestModule(t, testConfig(startFeedServer(t)), conn, clock)

	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if conn.flushCount != 1 {
		t.Fatalf("first refresh committed %d transactions, want 1", conn.flushCount)
	}

	clock.now = clock.now.Add(time.Hour)
	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("early Reconcile: %v", err)
	}
	if conn.flushCount != 1 {
		t.Fatalf("a tick one hour in committed %d transactions, want 1", conn.flushCount)
	}

	clock.now = clock.now.Add(6 * time.Hour)
	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("due Reconcile: %v", err)
	}
	if conn.flushCount != 2 {
		t.Fatalf("the due refresh committed %d transactions in total, want 2", conn.flushCount)
	}
}

// TestReconcileRetriesSoonerAfterAFailedCommit proves a rejected transaction
// is retried well before the next full interval, so a transient failure does
// not leave the sets stale for six hours.
func TestReconcileRetriesSoonerAfterAFailedCommit(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	conn.flushErr = errors.New("netlink refused the batch")
	clock := &testClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	module := newTestModule(t, testConfig(startFeedServer(t)), conn, clock)

	if err := module.Reconcile(context.Background(), slog.Default()); err == nil {
		t.Fatal("Reconcile reported success for a rejected transaction")
	}

	conn.flushErr = nil
	clock.now = clock.now.Add(refreshRetryInterval)
	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("retry Reconcile: %v", err)
	}
	if conn.flushCount != 2 {
		t.Fatalf("committed %d transactions, want the failed one and the retry", conn.flushCount)
	}
}

// TestReconcileKeepsTheOtherSourcesWhenTheFeedIsDown proves an unreachable
// feed degrades the pin to the sources that did answer rather than failing the
// refresh, which is the behaviour the shell refresher had.
func TestReconcileKeepsTheOtherSourcesWhenTheFeedIsDown(t *testing.T) {
	t.Parallel()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	conn := newRecordedConn()
	clock := &testClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	module := newTestModule(t, testConfig(deadURL), conn, clock)

	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertSequence(t, elementKeys(t, conn.elements[setV4Name]), []string{
		outsideTheSeed, "198.51.100.8 (end)",
		"208.54.0.0", "208.55.0.0 (end)",
	})
}

// TestReconcileKeepsTheOtherSourcesWhenTheFeedErrors covers the other way a
// feed goes bad: it answers with a status that returns no prefix list.
func TestReconcileKeepsTheOtherSourcesWhenTheFeedErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	conn := newRecordedConn()
	clock := &testClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	module := newTestModule(t, testConfig(server.URL), conn, clock)

	if err := module.Reconcile(context.Background(), slog.Default()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertSequence(t, elementKeys(t, conn.elements[setV4Name]), []string{
		outsideTheSeed, "198.51.100.8 (end)",
		"208.54.0.0", "208.55.0.0 (end)",
	})
}

// TestInitDisabledWithoutTheGate proves the module is off where the
// configuration does not turn it on, which is what keeps it from writing sets
// the shell refresher still owns.
func TestInitDisabledWithoutTheGate(t *testing.T) {
	t.Parallel()

	constructed, err := New(Config{
		Enabled:         false,
		RefreshInterval: 0,
		RefreshTimeout:  0,
		FeedURL:         "",
		SeedCIDRsV4:     nil,
		SeedCIDRsV6:     nil,
		FQDNsV4:         nil,
		FQDNsV6:         nil,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &ifmgr.Env{
		Iface: "", Sysctl: nil, Log: slog.Default(), Alerts: nil, Monitor: nil,
		DHCP: nil, RA: nil, RequestReconcile: nil, LiveState: nil,
	}
	initErr := constructed.Init(context.Background(), env)
	if !errors.Is(initErr, ifmgr.ErrModuleDisabled) {
		t.Fatalf("Init error = %v, want it to wrap ErrModuleDisabled", initErr)
	}
}

// TestInitRefusesAnUnusableSeed proves a bad seed range fails startup instead
// of vanishing from every refresh.
func TestInitRefusesAnUnusableSeed(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	cfg.SeedCIDRsV4 = []string{"208.54.0.0/16", "2001:db8::/32"}
	constructed, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &ifmgr.Env{
		Iface: "", Sysctl: nil, Log: slog.Default(), Alerts: nil, Monitor: nil,
		DHCP: nil, RA: nil, RequestReconcile: nil, LiveState: nil,
	}
	if err := constructed.Init(context.Background(), env); err == nil {
		t.Fatal("Init accepted an IPv6 range in the IPv4 seed list")
	}
}
