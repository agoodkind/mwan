package ovsreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestReportedForwarding(t *testing.T) {
	for _, test := range []struct{ total, offload, want string }{
		{"12", "0", "software"},
		{"12", "12", "hardware"},
		{"12", "1", "mixed"},
		{"0", "0", "unobserved"},
		{"18446744073709551615", "0", "unavailable"},
		{"12", "18446744073709551615", "unavailable"},
	} {
		line := fmt.Sprintf(
			"duration=2s, n_packets=%s, n_bytes=800, n_offload_packets=%s, n_offload_bytes=0, ip,actions=output:2",
			test.total,
			test.offload,
		)
		rules, err := unmarshalRules(line)
		if err != nil || len(rules) != 1 || rules[0].Forwarding != test.want {
			t.Fatalf("parse(%q) = %v, %v, want %s", line, rules, err, test.want)
		}
	}

	// The BlueField M16 TCP run recorded total counters without per-rule offload counters.
	rules, err := unmarshalRules(
		"cookie=0x0, duration=2317.331s, table=0, n_packets=467913, " +
			"n_bytes=42710930, idle_age=1179, priority=300,tcp6,in_port=2," +
			"ipv6_src=fd12:3456:789a::/48,tp_src=0x1388/0xfffc actions=ct(table=1,zone=1)",
	)
	if err != nil || len(rules) != 1 || rules[0].Forwarding != "unavailable" {
		t.Fatalf("missing offload counter: %v, %v", rules, err)
	}
}

func TestReadOlderSwitch(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "ovs.ctl")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	settings := ovsconfig.Config{
		Bridge: "br0",
		OpenFlow: &ovsconfig.OpenFlowConfig{
			Flows: []ovsconfig.Flow{{Name: "active"}, {Name: "missing"}},
		},
	}
	done := make(chan error, 1)
	go func() {
		for attempt := range 2 {
			conn, err := listener.Accept()
			if err != nil {
				done <- err

				return
			}

			var request struct{ Params []string }
			err = json.NewDecoder(conn).Decode(&request)
			if err == nil {
				switch {
				case attempt == 0 && slices.Equal(
					request.Params,
					[]string{"--offload-stats", "br0"},
				):
					err = json.NewEncoder(conn).Encode(map[string]any{
						"id":    1,
						"error": `"bridge/dump-flows" command takes at most 1 arguments`,
					})
				case attempt == 1 && slices.Equal(request.Params, []string{"br0"}):
					dump := fmt.Sprintf(
						"cookie=%#x, n_packets=7, n_bytes=42,actions=drop\n"+
							"cookie=%#x, n_packets=9, n_bytes=74,actions=drop\n"+
							"cookie=0x4321, n_packets=1, actions=drop",
						settings.OpenFlow.Flows[0].Cookie(),
						ovsconfig.FlowCookiePrefix,
					)
					err = json.NewEncoder(conn).Encode(
						struct {
							ID     int    `json:"id"`
							Result string `json:"result"`
						}{
							1,
							dump,
						},
					)
				default:
					err = fmt.Errorf("unexpected request %v", request.Params)
				}
			}

			conn.Close()
			if err != nil {
				done <- err

				return
			}
		}

		done <- nil
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	rules, err := Read(ctx, socket, settings)
	if err != nil {
		t.Fatal(err)
	}

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if len(rules) != 2 {
		t.Fatalf("unexpected rules: %+v", rules)
	}

	active, missing := rules[0], rules[1]
	if active.Name != "active" || active.Forwarding != "unavailable" || active.Offloaded != nil {
		t.Fatalf("unexpected hardware evidence: %+v", active)
	}

	if active.Packets == nil || *active.Packets != 7 {
		t.Fatalf("unexpected total: %+v", active)
	}

	if missing.Name != "missing" || missing.Forwarding != "unavailable" || missing.Packets != nil {
		t.Fatalf("unexpected missing rule: %+v", missing)
	}
}

func TestCancelledCounterRead(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "ovs.ctl")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := dumpRules(ctx, socket, []string{"br0"})
		done <- err
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var request json.RawMessage
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled report returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("report did not stop after cancellation")
	}
}
