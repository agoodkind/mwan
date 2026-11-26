package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gateway/gen/mwan/v1"
	"goodkind.io/mwan/gateway/internal/configstate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

func startTestServer(t *testing.T, srv *Server) mwanv1.MWANAgentClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	mwanv1.RegisterMWANAgentServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() { s.Stop() })
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return mwanv1.NewMWANAgentClient(conn)
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAgentServesOnTCP(t *testing.T) {
	dir := writeGetHealthBinDir(t, 0, 0, "", false)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	lis, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()

	grpcServer := grpc.NewServer()
	mwanv1.RegisterMWANAgentServer(
		grpcServer,
		NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), nil),
	)

	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(func() { grpcServer.GracefulStop() })

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cli := mwanv1.NewMWANAgentClient(conn)
	ctx := context.Background()
	_, err = cli.GetHealth(ctx, &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
}

// writeGetHealthBinDir writes ping, ping6, and systemctl into one directory for PATH.
func writeGetHealthBinDir(
	t *testing.T,
	pingExit int,
	ping6Exit int,
	systemctlBody string,
	systemctlFail bool,
) string {
	t.Helper()
	tmp := t.TempDir()
	write := func(name string, mode os.FileMode, content string) {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("ping", 0o755, fmt.Sprintf("#!/bin/sh\nexit %d\n", pingExit))
	write("ping6", 0o755, fmt.Sprintf("#!/bin/sh\nexit %d\n", ping6Exit))
	var sc string
	if systemctlFail {
		sc = "#!/bin/sh\nexit 1\n"
	} else {
		sc = fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\nexit 0\n", systemctlBody)
	}
	write("systemctl", 0o755, sc)
	return tmp
}

func TestGetHealth_HappyPath(t *testing.T) {
	dir := writeGetHealthBinDir(t, 0, 0, "", false)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	ctx := context.Background()
	res, err := cli.GetHealth(ctx, &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetIpv4Ok() || !res.GetIpv6Ok() {
		t.Fatalf("ipv4_ok=%v ipv6_ok=%v want both true", res.GetIpv4Ok(), res.GetIpv6Ok())
	}
}

func TestGetHealth_IPv4FailsIPv6OK(t *testing.T) {
	dir := writeGetHealthBinDir(t, 1, 0, "", false)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.GetHealth(context.Background(), &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetIpv4Ok() || !res.GetIpv6Ok() {
		t.Fatalf("ipv4_ok=%v ipv6_ok=%v want false,true", res.GetIpv4Ok(), res.GetIpv6Ok())
	}
}

func TestGetHealth_BothFail(t *testing.T) {
	dir := writeGetHealthBinDir(t, 1, 1, "", false)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.GetHealth(context.Background(), &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetIpv4Ok() || res.GetIpv6Ok() {
		t.Fatalf("ipv4_ok=%v ipv6_ok=%v want both false", res.GetIpv4Ok(), res.GetIpv6Ok())
	}
}

func TestGetHealth_FailedUnits(t *testing.T) {
	body := "failed-unit.service loaded failed failed\n"
	dir := writeGetHealthBinDir(t, 0, 0, body, false)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.GetHealth(context.Background(), &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range res.GetFailedUnits() {
		if u == "failed-unit.service" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("failed_units=%v want failed-unit.service", res.GetFailedUnits())
	}
}

func TestGetHealth_FailedUnitsSkipsNoiseLines(t *testing.T) {
	body := "failed-unit.service loaded failed failed\n" +
		"0 loaded units listed. Pass --all to see loaded but inactive units, too.\n" +
		"notadot\n" +
		"●\n" +
		"good-fail.service loaded failed failed\n"
	dir := writeGetHealthBinDir(t, 0, 0, body, false)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.GetHealth(context.Background(), &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	names := res.GetFailedUnits()
	if len(names) != 2 || names[0] != "failed-unit.service" || names[1] != "good-fail.service" {
		t.Fatalf("failed_units=%v want [failed-unit.service good-fail.service]", names)
	}
}

func TestGetHealth_SystemctlFails(t *testing.T) {
	dir := writeGetHealthBinDir(t, 0, 0, "", true)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.GetHealth(context.Background(), &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetFailedUnits()) != 0 {
		t.Fatalf("want empty failed_units on systemctl error, got %v", res.GetFailedUnits())
	}
}

func writeFakePingScript(t *testing.T, exitCode int, stdout string) string {
	t.Helper()
	tmp := t.TempDir()
	for _, name := range []string{"ping", "ping6"} {
		p := filepath.Join(tmp, name)
		script := fmt.Sprintf("#!/bin/sh\necho '%s'\nexit %d\n", stdout, exitCode)
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return tmp
}

func TestPing_IPv4UsesPingBinary(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "ping.out")
	pingPath := filepath.Join(tmp, "ping")
	script := fmt.Sprintf(
		"#!/bin/sh\necho \"$@\" >> %q\necho '2 packets transmitted, 2 received'\nexit 0\n",
		out,
	)
	if err := os.WriteFile(pingPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p6 := filepath.Join(tmp, "ping6")
	if err := os.WriteFile(p6, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	_, err := cli.Ping(
		context.Background(),
		&mwanv1.PingRequest{
			Target: "8.8.8.8",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	args := string(b)
	if !strings.Contains(args, "8.8.8.8") {
		t.Fatalf("want target in args: %q", args)
	}
	if strings.Contains(args, "2606:") {
		t.Fatalf("unexpected ipv6 in ping args: %q", args)
	}
}

func TestPing_IPv6UsesPing6Binary(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "ping6.out")
	p6 := filepath.Join(tmp, "ping6")
	script := fmt.Sprintf(
		"#!/bin/sh\necho \"$@\" >> %q\necho '2 packets transmitted, 2 received'\nexit 0\n",
		out,
	)
	if err := os.WriteFile(p6, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pingPath := filepath.Join(tmp, "ping")
	if err := os.WriteFile(pingPath, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	_, err := cli.Ping(
		context.Background(),
		&mwanv1.PingRequest{
			Target: "2606:4700:4700::1111",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "2606:4700:4700::1111") {
		t.Fatalf("args=%q", string(b))
	}
}

func TestPing_BindInterfaceAddsIFlag(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "args.log")
	pingPath := filepath.Join(tmp, "ping")
	script := fmt.Sprintf(
		"#!/bin/sh\necho \"$@\" >> %q\necho '2 packets transmitted, 2 received'\nexit 0\n",
		out,
	)
	if err := os.WriteFile(pingPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p6 := filepath.Join(tmp, "ping6")
	if err := os.WriteFile(p6, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	_, err := cli.Ping(
		context.Background(),
		&mwanv1.PingRequest{
			Target:         "1.1.1.1",
			BindInterface:  "eth0",
			Count:          2,
			TimeoutSeconds: 3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	s := string(b)
	if !strings.Contains(s, "-I") || !strings.Contains(s, "eth0") {
		t.Fatalf("want -I eth0 in args, got %q", s)
	}
}

func TestPing_CountAndTimeoutDefaults(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "args.log")
	pingPath := filepath.Join(tmp, "ping")
	script := fmt.Sprintf(
		"#!/bin/sh\necho \"$@\" >> %q\necho '2 packets transmitted, 2 received'\nexit 0\n",
		out,
	)
	if err := os.WriteFile(pingPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p6 := filepath.Join(tmp, "ping6")
	if err := os.WriteFile(p6, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	_, err := cli.Ping(
		context.Background(),
		&mwanv1.PingRequest{
			Target: "9.9.9.9",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	s := string(b)
	if !strings.Contains(s, "-c 2") || !strings.Contains(s, "-W 3") {
		t.Fatalf("want -c 2 and -W 3, got %q", s)
	}
}

func TestPing_FailsExitOne(t *testing.T) {
	dir := writeFakePingScript(t, 1, "2 packets transmitted, 0 received")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.Ping(context.Background(), &mwanv1.PingRequest{Target: "8.8.8.8"})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetSuccess() {
		t.Fatal("want success false")
	}
	if res.GetPacketsReceived() != 0 {
		t.Fatalf("packets_received=%d want 0", res.GetPacketsReceived())
	}
}

func TestPing_PacketsReceivedParsed(t *testing.T) {
	dir := writeFakePingScript(t, 0, "2 packets transmitted, 7 received")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.Ping(context.Background(), &mwanv1.PingRequest{Target: "8.8.8.8"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetSuccess() || res.GetPacketsReceived() != 7 {
		t.Fatalf("success=%v packets=%d", res.GetSuccess(), res.GetPacketsReceived())
	}
}

func TestPing_EmptyTarget(t *testing.T) {
	t.Parallel()
	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	_, err := cli.Ping(context.Background(), &mwanv1.PingRequest{Target: "  "})
	if err == nil {
		t.Fatal("expected error")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v want InvalidArgument", status.Code(err))
	}
}

func TestPing_MissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	_, err := cli.Ping(context.Background(), &mwanv1.PingRequest{Target: "8.8.8.8"})
	if err == nil {
		t.Fatal("expected error")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("code=%v want Internal", status.Code(err))
	}
}

func TestGetConfigStateWithoutDeploymentFile(t *testing.T) {
	t.Parallel()
	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	before := time.Now().Unix()
	res, err := cli.GetConfigState(context.Background(), &mwanv1.GetConfigStateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	expected := configstate.Read("")
	if res.GetConfigHash() != expected.ConfigHash || res.GetConfigManifest() != expected.ConfigManifest {
		t.Fatalf("configuration state differs: response=%v expected=%+v", res, expected)
	}
	after := time.Now().Unix()
	if res.GetLastChangeEpoch() < before || res.GetLastChangeEpoch() > after {
		t.Fatalf(
			"last_change_epoch=%d outside observation interval [%d, %d]",
			res.GetLastChangeEpoch(),
			before,
			after,
		)
	}
}

type fakeWatchStream struct {
	ctx context.Context
}

func (f *fakeWatchStream) Send(*mwanv1.AgentEvent) error { return nil }

func (f *fakeWatchStream) SetHeader(metadata.MD) error { return nil }

func (f *fakeWatchStream) SendHeader(metadata.MD) error { return nil }

func (f *fakeWatchStream) SetTrailer(metadata.MD) {}

func (f *fakeWatchStream) Context() context.Context { return f.ctx }

func (f *fakeWatchStream) SendMsg(m any) error { return nil }

func (f *fakeWatchStream) RecvMsg(m any) error { return nil }

func TestWatchEvents_DirectHandlerReturnsContextErr(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := NewServer(testLogger(t), nil)
	err := srv.WatchEvents(&mwanv1.WatchEventsRequest{}, &fakeWatchStream{ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want Canceled", err)
	}
}

func TestWatchEvents_ContextCancelled(t *testing.T) {
	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := cli.WatchEvents(ctx, &mwanv1.WatchEventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error after cancel")
	}
}

func TestParsePingReceivedCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int32
	}{
		{"", 0},
		{"no match here", 0},
		{"3 packets transmitted, 5 received", 5},
		{"bad transmitted, notreceived", 0},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(
			tc.in,
			func(t *testing.T) {
				t.Parallel()
				if got := parsePingReceivedCount(tc.in); got != tc.want {
					t.Fatalf("parsePingReceivedCount(%q)=%d want %d", tc.in, got, tc.want)
				}
			},
		)
	}
}

func TestParseMeminfoLineKB(t *testing.T) {
	t.Parallel()
	t.Run(
		"ok",
		func(t *testing.T) {
			t.Parallel()
			v, ok := parseMeminfoLineKB("MemTotal:       12345 kB")
			if !ok || v != 12345 {
				t.Fatalf("got %v %v", v, ok)
			}
		},
	)
	t.Run(
		"no_colon",
		func(t *testing.T) {
			t.Parallel()
			_, ok := parseMeminfoLineKB("MemTotal 123")
			if ok {
				t.Fatal("expected false")
			}
		},
	)
	t.Run(
		"empty_fields",
		func(t *testing.T) {
			t.Parallel()
			_, ok := parseMeminfoLineKB("MemTotal:")
			if ok {
				t.Fatal("expected false")
			}
		},
	)
	t.Run(
		"bad_number",
		func(t *testing.T) {
			t.Parallel()
			_, ok := parseMeminfoLineKB("MemTotal:       xyz kB")
			if ok {
				t.Fatal("expected false")
			}
		},
	)
}

func TestReadUptimeSecondsFrom(t *testing.T) {
	t.Parallel()
	t.Run(
		"ok",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "uptime")
			if err := os.WriteFile(p, []byte("3661.12 7200.00\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			v, err := readUptimeSecondsFrom(p)
			if err != nil || v != 3661 {
				t.Fatalf("got %d %v", v, err)
			}
		},
	)
	t.Run(
		"missing",
		func(t *testing.T) {
			t.Parallel()
			_, err := readUptimeSecondsFrom(filepath.Join(t.TempDir(), "nope"))
			if err == nil {
				t.Fatal("expected error")
			}
		},
	)
	t.Run(
		"empty",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "uptime")
			if err := os.WriteFile(p, []byte(""), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := readUptimeSecondsFrom(p)
			if err == nil {
				t.Fatal("expected error for empty file")
			}
		},
	)
	t.Run(
		"bad_float",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "uptime")
			if err := os.WriteFile(p, []byte("notafloat idle\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := readUptimeSecondsFrom(p)
			if err == nil {
				t.Fatal("expected error for non-float")
			}
		},
	)
}

func TestReadLoadAverageFrom(t *testing.T) {
	t.Parallel()
	t.Run(
		"ok",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "loadavg")
			if err := os.WriteFile(p, []byte("0.12 0.08 0.05 1/200 12345\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			v, err := readLoadAverageFrom(p)
			if err != nil || v != "0.12 0.08 0.05" {
				t.Fatalf("got %q %v", v, err)
			}
		},
	)
	t.Run(
		"missing",
		func(t *testing.T) {
			t.Parallel()
			_, err := readLoadAverageFrom(filepath.Join(t.TempDir(), "nope"))
			if err == nil {
				t.Fatal("expected error")
			}
		},
	)
	t.Run(
		"too_few_fields",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "loadavg")
			if err := os.WriteFile(p, []byte("0.12 0.08\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := readLoadAverageFrom(p)
			if err == nil {
				t.Fatal("expected error for too few fields")
			}
		},
	)
}

func TestReadMeminfoKBFrom(t *testing.T) {
	t.Parallel()
	t.Run(
		"ok",
		func(t *testing.T) {
			t.Parallel()
			content := "MemTotal:       16384000 kB\nMemAvailable:   8192000 kB\nOther: 1 kB\n"
			p := filepath.Join(t.TempDir(), "meminfo")
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			tot, avail, err := readMeminfoKBFrom(p)
			if err != nil || tot != 16384000 || avail != 8192000 {
				t.Fatalf("tot=%d avail=%d err=%v", tot, avail, err)
			}
		},
	)
	t.Run(
		"missing_file",
		func(t *testing.T) {
			t.Parallel()
			_, _, err := readMeminfoKBFrom(filepath.Join(t.TempDir(), "nope"))
			if err == nil {
				t.Fatal("expected error")
			}
		},
	)
	t.Run(
		"missing_memtotal",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "meminfo")
			if err := os.WriteFile(p, []byte("MemAvailable: 1000 kB\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := readMeminfoKBFrom(p)
			if err == nil {
				t.Fatal("expected error")
			}
		},
	)
	t.Run(
		"missing_memavailable",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "meminfo")
			if err := os.WriteFile(p, []byte("MemTotal: 1000 kB\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := readMeminfoKBFrom(p)
			if err == nil {
				t.Fatal("expected error")
			}
		},
	)
	t.Run(
		"bad_memtotal_value",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "meminfo")
			if err := os.WriteFile(
				p,
				[]byte("MemTotal: bad kB\nMemAvailable: 1000 kB\n"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			_, _, err := readMeminfoKBFrom(p)
			if err == nil {
				t.Fatal("expected error for bad MemTotal")
			}
		},
	)
	t.Run(
		"bad_memavailable_value",
		func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "meminfo")
			if err := os.WriteFile(
				p,
				[]byte("MemTotal: 1000 kB\nMemAvailable: bad kB\n"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			_, _, err := readMeminfoKBFrom(p)
			if err == nil {
				t.Fatal("expected error for bad MemAvailable")
			}
		},
	)
}

func TestGetSystemInfo_InjectablePaths(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	uptimePath := filepath.Join(tmp, "uptime")
	loadAvgPath := filepath.Join(tmp, "loadavg")
	meminfoPath := filepath.Join(tmp, "meminfo")

	if err := os.WriteFile(uptimePath, []byte("12345.67 9999.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loadAvgPath, []byte("0.01 0.02 0.03 1/100 9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	memContent := "MemTotal:       4096000 kB\nMemAvailable:   2048000 kB\n"
	if err := os.WriteFile(meminfoPath, []byte(memContent), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(testLogger(t), nil)
	srv.uptimePath = uptimePath
	srv.loadAvgPath = loadAvgPath
	srv.meminfoPath = meminfoPath
	cli := startTestServer(t, srv)
	res, err := cli.GetSystemInfo(context.Background(), &mwanv1.GetSystemInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetUptimeSeconds() != 12345 {
		t.Fatalf("uptime_seconds=%d want 12345", res.GetUptimeSeconds())
	}
	if res.GetLoadAverage() != "0.01 0.02 0.03" {
		t.Fatalf("load_average=%q want '0.01 0.02 0.03'", res.GetLoadAverage())
	}
	wantTotal := int64(4096000 * 1024)
	wantUsed := int64((4096000 - 2048000) * 1024)
	if res.GetMemoryTotalBytes() != wantTotal {
		t.Fatalf("memory_total=%d want %d", res.GetMemoryTotalBytes(), wantTotal)
	}
	if res.GetMemoryUsedBytes() != wantUsed {
		t.Fatalf("memory_used=%d want %d", res.GetMemoryUsedBytes(), wantUsed)
	}
	if res.GetHostname() == "" {
		t.Fatal("hostname should not be empty")
	}
	if res.GetDiskTotalBytes() <= 0 {
		t.Fatal("disk_total should be > 0")
	}
}

func TestGetSystemInfo_UptimeError(t *testing.T) {
	t.Parallel()
	srv := NewServer(testLogger(t), nil)
	srv.uptimePath = filepath.Join(t.TempDir(), "nope")
	cli := startTestServer(t, srv)
	_, err := cli.GetSystemInfo(context.Background(), &mwanv1.GetSystemInfoRequest{})
	if err == nil || status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}

func TestGetSystemInfo_LoadAvgError(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	uptimePath := filepath.Join(tmp, "uptime")
	if err := os.WriteFile(uptimePath, []byte("100.0 200.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(testLogger(t), nil)
	srv.uptimePath = uptimePath
	srv.loadAvgPath = filepath.Join(tmp, "nope")
	cli := startTestServer(t, srv)
	_, err := cli.GetSystemInfo(context.Background(), &mwanv1.GetSystemInfoRequest{})
	if err == nil || status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}

func TestGetSystemInfo_MeminfoError(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	uptimePath := filepath.Join(tmp, "uptime")
	loadAvgPath := filepath.Join(tmp, "loadavg")
	if err := os.WriteFile(uptimePath, []byte("100.0 200.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loadAvgPath, []byte("0.1 0.2 0.3 1/50 123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(testLogger(t), nil)
	srv.uptimePath = uptimePath
	srv.loadAvgPath = loadAvgPath
	srv.meminfoPath = filepath.Join(tmp, "nope")
	cli := startTestServer(t, srv)
	_, err := cli.GetSystemInfo(context.Background(), &mwanv1.GetSystemInfoRequest{})
	if err == nil || status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}

func TestGetSystemInfo_StatfsError(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	uptimePath := filepath.Join(tmp, "uptime")
	loadAvgPath := filepath.Join(tmp, "loadavg")
	meminfoPath := filepath.Join(tmp, "meminfo")
	if err := os.WriteFile(uptimePath, []byte("100.0 200.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loadAvgPath, []byte("0.1 0.2 0.3 1/50 123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		meminfoPath,
		[]byte("MemTotal: 1000 kB\nMemAvailable: 500 kB\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(testLogger(t), nil)
	srv.uptimePath = uptimePath
	srv.loadAvgPath = loadAvgPath
	srv.meminfoPath = meminfoPath
	srv.statfsPath = filepath.Join(tmp, "nonexistent-mount")
	cli := startTestServer(t, srv)
	_, err := cli.GetSystemInfo(context.Background(), &mwanv1.GetSystemInfoRequest{})
	if err == nil || status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}

func TestGetHealth_PingExecMissing(t *testing.T) {
	// PATH omits ping and ping6. GetHealth logs command errors and returns a response.
	t.Setenv("PATH", t.TempDir())
	srv := NewServer(testLogger(t), nil)
	cli := startTestServer(t, srv)
	res, err := cli.GetHealth(context.Background(), &mwanv1.GetHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetIpv4Ok() || res.GetIpv6Ok() {
		t.Fatalf(
			"expected both false with missing ping, got ipv4=%v ipv6=%v",
			res.GetIpv4Ok(),
			res.GetIpv6Ok(),
		)
	}
}
