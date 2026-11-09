//go:build linux && firewallnetns

package main_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/healthcheck"
)

type healthFixture struct {
	config, network, state, observations string
	document                             map[string]json.RawMessage
	interfaces                           map[string]json.RawMessage
	entries                              []map[string]json.RawMessage
}

type healthPolicyFixture struct {
	Enabled   bool     `json:"enabled"`
	PingCount *int     `json:"ping-count,omitempty"`
	Success   int      `json:"success-threshold"`
	Failure   int      `json:"failure-threshold"`
	Recovery  int      `json:"recovery-threshold"`
	Interval  int      `json:"check-interval"`
	TargetsV4 []string `json:"targets-v4"`
	TargetsV6 []string `json:"targets-v6"`
	HTTPURLs  []string `json:"http-urls"`
}

func newHealthFixture(t *testing.T) *healthFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &healthFixture{
		config:       filepath.Join(root, "config.toml"),
		network:      filepath.Join(root, "network.json"),
		state:        filepath.Join(root, "health.state"),
		observations: filepath.Join(root, "absent-observations.json"),
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture.document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(
		fixture.document["ietf-interfaces:interfaces"],
		&fixture.interfaces,
	); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixture.interfaces["interface"], &fixture.entries); err != nil {
		t.Fatal(err)
	}
	configuration := fmt.Sprintf(
		"[ifmgr]\nrole = 'wan'\n[ifmgr.iface.lo]\n[ifmgr.modules.health]\nobservation_settings_file = %q\nstate_file = %q\n[wanconfig]\npublish = true\n",
		fixture.observations,
		fixture.state,
	)
	if err := os.WriteFile(fixture.config, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, root, "/etc/mwan")
	schema, err := filepath.Abs(filepath.Join("..", "..", "internal", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, schema, "/usr/local/share/wanconfig/yang")
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *healthFixture) policy(
	t *testing.T,
	provider string,
	iface string,
	policy healthPolicyFixture,
) {
	t.Helper()
	for _, entry := range fixture.entries {
		data, present := entry["goodkind-mwan-steering:wan"]
		if !present {
			continue
		}
		var wan map[string]json.RawMessage
		if err := json.Unmarshal(data, &wan); err != nil {
			t.Fatal(err)
		}
		var name string
		if err := json.Unmarshal(wan["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name != provider {
			continue
		}
		var err error
		entry["name"], err = json.Marshal(iface)
		if err != nil {
			t.Fatal(err)
		}
		entry["goodkind-mwan-steering:connection-id"], err = json.Marshal("health-" + provider)
		if err != nil {
			t.Fatal(err)
		}
		encodedPolicy, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		var healthSection map[string]json.RawMessage
		if err := json.Unmarshal(encodedPolicy, &healthSection); err != nil {
			t.Fatal(err)
		}
		if policy.TargetsV4 == nil {
			delete(healthSection, "targets-v4")
		}
		if policy.TargetsV6 == nil {
			delete(healthSection, "targets-v6")
		}
		wan["health"], err = json.Marshal(healthSection)
		if err != nil {
			t.Fatal(err)
		}
		entry["goodkind-mwan-steering:wan"], err = json.Marshal(wan)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("fixture does not contain provider %s", provider)
}

func (fixture *healthFixture) save(t *testing.T, timeoutMillis int) {
	t.Helper()
	var group map[string]json.RawMessage
	if err := json.Unmarshal(
		fixture.interfaces["goodkind-mwan-steering:steering-group"],
		&group,
	); err != nil {
		t.Fatal(err)
	}
	var err error
	group["health"], err = json.Marshal(map[string]int{"probe-timeout": timeoutMillis})
	if err != nil {
		t.Fatal(err)
	}
	fixture.interfaces["goodkind-mwan-steering:steering-group"], err = json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	fixture.interfaces["interface"], err = json.Marshal(fixture.entries)
	if err != nil {
		t.Fatal(err)
	}
	fixture.document["ietf-interfaces:interfaces"], err = json.Marshal(fixture.interfaces)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(fixture.document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.network, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func healthPolicy(urls []string) healthPolicyFixture {
	return healthPolicyFixture{
		Enabled:   true,
		PingCount: new(2),
		Success:   1,
		Failure:   1,
		Recovery:  1,
		Interval:  1,
		TargetsV4: []string{"127.0.0.1", "127.0.0.2"},
		TargetsV6: []string{"::1"},
		HTTPURLs:  urls,
	}
}

func healthCommand(
	t *testing.T,
	binary string,
	fixture *healthFixture,
	arguments ...string,
) *exec.Cmd {
	t.Helper()
	arguments = append([]string{"health", "--config", fixture.config}, arguments...)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, arguments...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.Contains(strings.ToLower(key), "proxy") {
			command.Env = append(command.Env, entry)
		}
	}
	return command
}

func runHealthFixture(
	t *testing.T,
	binary string,
	fixture *healthFixture,
	arguments ...string,
) ([]healthcheck.Record, string) {
	t.Helper()
	command := healthCommand(t, binary, fixture, append(arguments, "--output-format", "json")...)
	var output, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostics
	if err := command.Run(); err != nil {
		t.Fatalf(
			"health exit=%v output=%s diagnostics=%s",
			err,
			output.String(),
			diagnostics.String(),
		)
	}
	var records []healthcheck.Record
	if err := json.Unmarshal(output.Bytes(), &records); err != nil ||
		strings.Count(output.String(), "\n") != 1 {
		t.Fatalf(
			"finite JSON decode=%v output=%s diagnostics=%s",
			err,
			output.String(),
			diagnostics.String(),
		)
	}
	return records, diagnostics.String()
}

func TestConfiguredHealthNamespaceRuntime(t *testing.T) {
	runDebugProbeNamespaceTest(t, runConfiguredHealthNamespaceRuntime)
}

func runConfiguredHealthNamespaceRuntime(t *testing.T, binary string) {
	fixture := newHealthFixture(t)
	var mutex sync.Mutex
	counts := make(map[string]int)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		counts[request.URL.Path]++
		mutex.Unlock()
		if request.URL.Path == "/close" {
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		status := http.StatusTeapot
		if request.URL.Path == "/503" {
			status = http.StatusServiceUnavailable
		}
		writer.WriteHeader(status)
		_, _ = fmt.Fprint(writer, "  health fixture ", request.URL.Path, "\n")
	})
	listener4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener4.Addr().(*net.TCPAddr).Port
	listener6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	for _, listener := range []net.Listener{listener4, listener6} {
		server := httptest.NewUnstartedServer(handler)
		server.Listener = listener
		server.Start()
		defer server.Close()
	}
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	urls := []string{baseURL + "/418", baseURL + "/503", baseURL + "/close"}
	policy := healthPolicy(urls)
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 250)
	started := time.Now()
	records, diagnostics := runHealthFixture(t, binary, fixture)
	if diagnostics != "" || len(records) != 12 {
		t.Fatalf("records=%+v diagnostics=%s", records, diagnostics)
	}
	pingCounts := make(map[string]int)
	for _, record := range records {
		if record.WAN.ID != "health-webpass" || record.WAN.Name != "webpass" ||
			record.WAN.Iface != "lo" ||
			record.Timestamp.Before(started) ||
			record.Timestamp.After(time.Now()) {
			t.Fatalf("raw identity=%+v", record)
		}
		if record.Ping != nil {
			attempt := record.Ping
			pingCounts[attempt.Target]++
			if attempt.Interface != "lo" || attempt.Attempt != pingCounts[attempt.Target] ||
				attempt.Timeout != 250*time.Millisecond ||
				attempt.Error != "" ||
				attempt.RTT <= 0 {
				t.Fatalf("raw ping=%+v", attempt)
			}
		} else if record.HTTP != nil {
			attempt := record.HTTP
			if attempt.Interface != "lo" || attempt.Timeout != 250*time.Millisecond ||
				attempt.Iteration < 1 ||
				attempt.Iteration > len(urls) ||
				attempt.Target != urls[attempt.Iteration-1] {
				t.Fatalf("raw HTTP=%+v", attempt)
			}
			if attempt.Iteration == 3 {
				if attempt.Error != "HTTPGet: HTTP GET: Get \""+attempt.Target+"\": EOF" ||
					attempt.StatusCode != 0 {
					t.Fatalf("raw HTTP failure=%+v", attempt)
				}
			} else {
				status := http.StatusTeapot
				path := "/418"
				if attempt.Iteration == 2 {
					status, path = http.StatusServiceUnavailable, "/503"
				}
				if attempt.StatusCode != status || attempt.Body != "health fixture "+path ||
					attempt.Error != "" {
					t.Fatalf("raw HTTP response=%+v", attempt)
				}
			}
		} else {
			t.Fatalf("record omitted its attempt: %+v", record)
		}
	}
	for _, target := range []string{"127.0.0.1", "127.0.0.2", "::1"} {
		if pingCounts[target] != 2 {
			t.Fatalf("target=%s attempts=%d", target, pingCounts[target])
		}
	}
	mutex.Lock()
	for _, path := range []string{"/418", "/503", "/close"} {
		if counts[path] != 2 {
			t.Errorf("path=%s requests=%d", path, counts[path])
		}
	}
	mutex.Unlock()
	command := healthCommand(t, binary, fixture)
	text, err := command.Output()
	if err != nil || strings.Count(string(text), " WAN webpass (health-webpass) via lo") != 12 ||
		!strings.Contains(string(text), "status 418") ||
		!strings.Contains(string(text), strconv.Quote(records[len(records)-1].HTTP.Error)) {
		t.Fatalf("text error=%v output=%s", err, text)
	}
	if _, err := os.Stat(fixture.state); !os.IsNotExist(err) {
		t.Fatalf("health created daemon state: %v", err)
	}
	for _, family := range []string{"inet", "inet6", "omitted"} {
		policy := healthPolicy(urls[:1])
		if family == "inet6" {
			policy.TargetsV4 = []string{}
		} else if family == "omitted" {
			policy.TargetsV6 = nil
		} else {
			policy.TargetsV6 = []string{}
		}
		fixture.policy(t, "webpass", "lo", policy)
		fixture.save(t, 250)
		records, _ := runHealthFixture(t, binary, fixture)
		wantFamily, wantCount := "inet", 5
		if family == "inet6" {
			wantFamily, wantCount = "inet6", 3
		}
		if len(records) != wantCount {
			t.Fatalf("family=%s records=%+v", family, records)
		}
		for _, record := range records {
			if record.Ping != nil && record.Ping.Family != wantFamily ||
				record.HTTP != nil && record.HTTP.Family != wantFamily {
				t.Fatalf("empty family triggered a probe: %+v", record)
			}
		}
	}
	policy = healthPolicy(urls[:1])
	policy.TargetsV6 = []string{}
	policy.TargetsV4 = []string{"192.0.2.99"}
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 100)
	records, _ = runHealthFixture(t, binary, fixture)
	if len(records) != 3 || records[0].Ping.Error == "" || records[1].Ping.Error == "" ||
		records[2].HTTP.StatusCode != http.StatusTeapot {
		t.Fatalf("failed ping attempts=%+v", records)
	}
	assertHealthOutputErrors(t, binary, fixture)
	policy.Enabled = false
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 100)
	mutex.Lock()
	requestCount := counts["/418"] + counts["/503"] + counts["/close"]
	mutex.Unlock()
	for _, arguments := range [][]string{nil, {"--watch"}} {
		records, _ := runHealthFixture(t, binary, fixture, arguments...)
		if len(records) != 0 || records == nil {
			t.Fatalf("disabled policies produced records=%+v", records)
		}
		command := healthCommand(t, binary, fixture, arguments...)
		output, err := command.Output()
		if err != nil || string(output) != "Health checks did not produce any probe records.\n" {
			t.Fatalf("empty text result error=%v output=%s", err, output)
		}
	}
	mutex.Lock()
	if current := counts["/418"] + counts["/503"] + counts["/close"]; current != requestCount {
		t.Errorf("disabled policy issued HTTP requests: before=%d after=%d", requestCount, current)
	}
	mutex.Unlock()
	policy.Enabled, policy.PingCount = true, new(0)
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 100)
	command = healthCommand(t, binary, fixture)
	var rejected bytes.Buffer
	command.Stderr = &rejected
	if err := command.Run(); err == nil ||
		!strings.Contains(rejected.String(), "ping-count must be > 0") {
		t.Fatalf("invalid policy error=%v diagnostics=%s", err, rejected.String())
	}
	policy.PingCount = new(2)
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 100)
	daemonContext, cancelDaemon := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelDaemon()
	command = exec.CommandContext(daemonContext, binary, "ifmgr", "--config", fixture.config)
	rejected.Reset()
	command.Stderr = &rejected
	if output, err := command.Output(); err == nil ||
		!strings.Contains(string(output)+rejected.String(), "load health observations") {
		t.Fatalf(
			"daemon observation validation error=%v output=%s diagnostics=%s",
			err,
			output,
			rejected.String(),
		)
	}
	assertHealthRejectedProvider(t, binary, fixture, urls, &mutex, counts)
}

func assertHealthRejectedProvider(
	t *testing.T,
	binary string,
	fixture *healthFixture,
	urls []string,
	mutex *sync.Mutex,
	counts map[string]int,
) {
	t.Helper()
	policy := healthPolicy(urls)
	fixture.policy(t, "att", "lo", policy)
	policy.PingCount = nil
	fixture.policy(t, "webpass", "health-bad0", policy)
	fixture.save(t, 100)
	mutex.Lock()
	before := counts["/418"] + counts["/503"] + counts["/close"]
	mutex.Unlock()
	for _, watch := range []bool{false, true} {
		for _, format := range []string{"text", "json"} {
			arguments := []string{"--output-format", format}
			if watch {
				arguments = append(arguments, "--watch")
			}
			command := healthCommand(t, binary, fixture, arguments...)
			var output, diagnostics bytes.Buffer
			command.Stdout, command.Stderr = &output, &diagnostics
			err := command.Run()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 1 || output.Len() != 0 ||
				strings.Contains(diagnostics.String(), "Usage:") {
				t.Fatalf(
					"rejected provider watch=%t format=%s error=%v output=%s diagnostics=%s",
					watch,
					format,
					err,
					output.String(),
					diagnostics.String(),
				)
			}
			if !strings.Contains(diagnostics.String(), "webpass") ||
				!strings.Contains(diagnostics.String(), "health-bad0") ||
				!strings.Contains(diagnostics.String(), "health/ping-count is required") {
				t.Fatalf("provider rejection omitted configuration facts: %s", diagnostics.String())
			}
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	if after := counts["/418"] + counts["/503"] + counts["/close"]; after != before {
		t.Fatalf("rejected configuration issued HTTP probes: before=%d after=%d", before, after)
	}
}

func assertHealthOutputErrors(t *testing.T, binary string, fixture *healthFixture) {
	t.Helper()
	for _, watch := range []bool{false, true} {
		for _, format := range []string{"text", "json"} {
			arguments := []string{"--output-format", format}
			if watch {
				arguments = append(arguments, "--watch")
			}
			command := healthCommand(t, binary, fixture, arguments...)
			full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			command.Stdout, command.Stderr = full, &diagnostics
			err = command.Run()
			_ = full.Close()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 1 ||
				!strings.Contains(diagnostics.String(), "no space left on device") {
				t.Fatalf(
					"health output watch=%t format=%s error=%v diagnostics=%s",
					watch,
					format,
					err,
					diagnostics.String(),
				)
			}
		}
	}
}

type healthWatchProcess struct {
	command     *exec.Cmd
	records     chan healthcheck.Record
	decodeError chan error
	diagnostics bytes.Buffer
}

func startHealthWatch(t *testing.T, binary string, fixture *healthFixture) *healthWatchProcess {
	t.Helper()
	process := &healthWatchProcess{
		command:     healthCommand(t, binary, fixture, "--watch", "--output-format", "json"),
		records:     make(chan healthcheck.Record, 256),
		decodeError: make(chan error, 1),
	}
	process.command.Stderr = &process.diagnostics
	stdout, err := process.command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := process.command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if process.command.ProcessState == nil {
			_ = process.command.Process.Kill()
			_ = process.command.Wait()
		}
	})
	go func() {
		defer close(process.records)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var record healthcheck.Record
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				process.decodeError <- err
				return
			}
			process.records <- record
		}
		process.decodeError <- scanner.Err()
	}()
	return process
}

func stopHealthWatch(t *testing.T, process *healthWatchProcess) []healthcheck.Record {
	t.Helper()
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var records []healthcheck.Record
	finished := make(chan error, 1)
	recordsChannel := process.records
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case record, open := <-recordsChannel:
			if open {
				records = append(records, record)
				continue
			}
			recordsChannel = nil
			if err := <-process.decodeError; err != nil {
				t.Fatal(err)
			}
			go func() { finished <- process.command.Wait() }()
		case err := <-finished:
			if err != nil {
				t.Fatalf(
					"health cancellation error=%v diagnostics=%s",
					err,
					process.diagnostics.String(),
				)
			}
			return records
		case <-deadline.C:
			t.Fatal("health workers did not complete after SIGTERM")
		}
	}
}

func TestConfiguredHealthWatchNamespaceRuntime(t *testing.T) {
	runDebugProbeNamespaceTest(t, runConfiguredHealthWatchNamespaceRuntime)
}

func runConfiguredHealthWatchNamespaceRuntime(t *testing.T, binary string) {
	fixture := newHealthFixture(t)
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "healthslow0"}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr("192.0.2.1/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	type requestTime struct {
		path              string
		started, finished time.Time
	}
	requests := make(chan requestTime, 64)
	server := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			started := time.Now()
			delay := 200 * time.Millisecond
			if request.URL.Path == "/slow" {
				delay = 1200 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				_, _ = io.WriteString(writer, "health watch fixture")
				requests <- requestTime{
					path:     request.URL.Path,
					started:  started,
					finished: time.Now(),
				}
			case <-request.Context().Done():
			}
		}),
	)
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	// Accept both configured local interfaces through one real HTTP listener.
	server.Close()
	listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewUnstartedServer(server.Config.Handler)
	server.Listener = listener
	server.Start()
	defer server.Close()
	fast := healthPolicy([]string{fmt.Sprintf("http://127.0.0.1:%d/fast", port)})
	fast.TargetsV4, fast.TargetsV6 = []string{"127.0.0.1"}, []string{}
	slow := fast
	slow.Interval, slow.TargetsV4, slow.HTTPURLs = 3, []string{
		"192.0.2.1",
	}, []string{
		fmt.Sprintf("http://192.0.2.1:%d/slow", port),
	}
	fixture.policy(t, "webpass", "lo", fast)
	fixture.policy(t, "att", "healthslow0", slow)
	fixture.save(t, 3000)
	process := startHealthWatch(t, binary, fixture)
	times := make(map[string][]requestTime)
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	for len(times["/slow"]) < 2 || len(times["/fast"]) < 4 {
		select {
		case request := <-requests:
			times[request.path] = append(times[request.path], request)
		case <-deadline.C:
			t.Fatalf("independent WAN sweeps did not complete: %+v", times)
		}
	}
	records := stopHealthWatch(t, process)
	if len(records) < 18 {
		t.Fatalf("watch omitted completed records: %+v", records)
	}
	for path, requests := range times {
		interval := time.Second
		if path == "/slow" {
			interval = 3 * time.Second
		}
		for index := 1; index < len(requests); index++ {
			gap := requests[index].started.Sub(requests[index-1].finished)
			t.Logf(
				"WAN path=%s configured interval=%s observed delay after completion=%s",
				path,
				interval,
				gap,
			)
			if gap < interval-50*time.Millisecond || gap > interval+750*time.Millisecond {
				t.Fatalf("path=%s configured=%s measured post-sweep delay=%s", path, interval, gap)
			}
		}
	}
	if times["/fast"][1].finished.After(times["/slow"][1].started) {
		t.Fatal("slow WAN delayed independent fast sweeps")
	}
}

func TestConfiguredHealthCancellationNamespaceRuntime(t *testing.T) {
	runDebugProbeNamespaceTest(t, runConfiguredHealthCancellationNamespaceRuntime)
}

func runConfiguredHealthCancellationNamespaceRuntime(t *testing.T, binary string) {
	fixture := newHealthFixture(t)
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(
		http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(entered)
			<-request.Context().Done()
			close(canceled)
		}),
	)
	defer server.Close()
	policy := healthPolicy([]string{server.URL})
	policy.TargetsV4, policy.TargetsV6 = []string{"127.0.0.1"}, []string{}
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 10000)
	process := startHealthWatch(t, binary, fixture)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("health HTTP request did not start")
	}
	records := stopHealthWatch(t, process)
	if len(records) != 3 || records[2].HTTP == nil || records[2].HTTP.Error == "" {
		t.Fatalf("canceled probe records=%+v", records)
	}
	t.Logf("canceled HTTP request returned %q", records[2].HTTP.Error)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("health returned before its HTTP request canceled")
	}
}

func TestConfiguredHealthZeroIntervalNamespaceRuntime(t *testing.T) {
	runDebugProbeNamespaceTest(t, runConfiguredHealthZeroIntervalNamespaceRuntime)
}

func runConfiguredHealthZeroIntervalNamespaceRuntime(t *testing.T, binary string) {
	fixture := newHealthFixture(t)
	completed := make(chan time.Time, 64)
	server := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				_, _ = io.WriteString(writer, "zero interval fixture")
				completed <- time.Now()
			case <-request.Context().Done():
			}
		}),
	)
	defer server.Close()
	policy := healthPolicy([]string{server.URL})
	policy.Interval, policy.TargetsV4, policy.TargetsV6 = 0, []string{"127.0.0.1"}, []string{}
	fixture.policy(t, "webpass", "lo", policy)
	fixture.save(t, 3000)
	command := healthCommand(t, binary, fixture, "--watch")
	var output strings.Builder
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	var times []time.Time
	lines := make(chan string, 256)
	readError := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		readError <- scanner.Err()
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	responses := 0
	for responses < 2 {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatal("text watch stopped before cancellation")
			}
			output.WriteString(line + "\n")
			if strings.Contains(line, "status 200") {
				responses++
				times = append(times, <-completed)
			}
		case <-deadline.C:
			t.Fatal("zero-interval watch did not complete repeated HTTP requests")
		}
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	reading := true
	for reading {
		select {
		case line, open := <-lines:
			if !open {
				reading = false
				continue
			}
			output.WriteString(line + "\n")
		case <-deadline.C:
			t.Fatal("text watch did not stop after SIGTERM")
		}
	}
	if err := <-readError; err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("text watch cancellation=%v diagnostics=%s", err, diagnostics.String())
	}
	gap := times[1].Sub(times[0])
	if gap > 750*time.Millisecond || strings.Count(output.String(), "status 200") < 2 ||
		diagnostics.Len() != 0 {
		t.Fatalf(
			"zero interval completion gap=%s text=%s diagnostics=%s",
			gap,
			output.String(),
			diagnostics.String(),
		)
	}
	t.Logf(
		"zero configured interval produced HTTP completions %s apart with 100ms server delay",
		gap,
	)
}
