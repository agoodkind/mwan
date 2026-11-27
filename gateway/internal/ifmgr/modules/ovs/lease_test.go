//go:build linux && ovs

package ovs

import (
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

type leaseClock struct{ now time.Time }

func (c *leaseClock) Now() time.Time { return c.now }

func TestReconcileLeaseOperands(t *testing.T) {
	ctx := t.Context()
	const id = "uplink"
	socket := os.Getenv("MWAN_TEST_OVS_SOCKET")
	if socket == "" {
		t.Fatal("run make test-ovs to start a disposable OVS instance")
	}
	bridge := filepath.Join(filepath.Dir(socket), "br-dhcp.mgmt")
	config := ovsconfig.Config{
		Bridge:        "br-dhcp",
		DatapathType:  "netdev",
		Socket:        socket,
		TimeoutMillis: 5000,
		OpenFlow: &ovsconfig.OpenFlowConfig{
			Socket: bridge,
			Flows: []ovsconfig.Flow{
				{
					Name:     "delegation",
					Priority: 100,
					Match: []ovsconfig.Match{
						{Field: "ipv6"},
						{Field: "ipv6_dst", Value: "${dhcpv6-pd:uplink}"},
					},
					Actions: []ovsconfig.Action{
						{ID: 1, Name: "set_field", Argument: "${dhcpv6-pd:uplink}->ipv6_src"},
					},
				},
				{Name: "permanent", Priority: 20},
			},
			Groups: []ovsconfig.Group{
				{
					ID:   1,
					Type: ovsconfig.GroupSelect,
					Buckets: []ovsconfig.Bucket{
						{
							ID: 1,
							Actions: []ovsconfig.Action{
								{Name: "set_field", Argument: "${dhcpv4:uplink}->ip_src"},
							},
						},
						{ID: 2, Actions: []ovsconfig.Action{{Name: "drop"}}},
					},
				},
			},
		},
	}
	t.Cleanup(func() {
		output, err := exec.Command(
			"ovs-vsctl",
			"--db=unix:"+socket,
			"--if-exists",
			"del-br",
			config.Bridge,
		).CombinedOutput()
		if err != nil {
			t.Errorf("remove test bridge: %v: %s", err, output)
		}
	})
	m, err := newModule(Loader(func() (ovsconfig.Config, error) { return config, nil }))
	if err != nil {
		t.Fatal(err)
	}
	clock := &leaseClock{now: time.Unix(1000, 0)}
	m.clock = clock
	store := wanstate.New()
	store.SetConnections([]interfaceintent.Connection{
		{ID: id, Name: "wan0", Owner: interfaceintent.OwnerMWAN},
	})
	if err := m.Init(ctx, &ifmgr.Env{Log: slog.Default(), LiveState: store}); err != nil {
		t.Fatal(err)
	}
	read := func(command string) string {
		t.Helper()
		output, err := exec.CommandContext(
			ctx,
			"ovs-ofctl",
			"-O",
			"OpenFlow15",
			command,
			"unix:"+bridge,
		).CombinedOutput()
		if err != nil {
			t.Fatalf("read switch %s: %v: %s", command, err, output)
		}
		return string(output)
	}
	check := func(match, action string) {
		t.Helper()
		if err := m.Reconcile(ctx, slog.Default()); err != nil {
			t.Fatal(err)
		}
		flows, buckets := read("dump-flows"), read("dump-groups")
		if !strings.Contains(flows, "priority=20 ") || !strings.Contains(buckets, "bucket_id:2,") {
			t.Fatalf("missing independent entries: %s\n%s", flows, buckets)
		}
		expectedFlows := 0
		if match != "" {
			expectedFlows = 1
		}
		if strings.Count(flows, "priority=100,") != expectedFlows ||
			(match != "" && (!strings.Contains(flows, "ipv6_dst="+match) ||
				!strings.Contains(flows, "set_field:"+match+"->ipv6_src"))) {
			t.Fatalf("wanted prefix %q in installed flows: %s", match, flows)
		}
		if strings.Contains(buckets, "bucket_id:1,") != (action != "") ||
			!strings.Contains(buckets, action) {
			t.Fatalf("wanted address %q in installed buckets: %s", action, buckets)
		}
	}
	expires := clock.now.Add(time.Hour)
	ipv4 := interfaceintent.Assignment{
		Kind:       interfaceintent.AssignmentDHCPv4,
		Value:      netip.MustParsePrefix("192.0.2.13/24"),
		Valid:      true,
		ValidUntil: &expires,
	}
	pd := interfaceintent.Assignment{
		Kind:       interfaceintent.AssignmentDHCPv6IAPD,
		Value:      netip.MustParsePrefix("2001:db8:10::/56"),
		Valid:      true,
		ValidUntil: &expires,
	}
	publish := func(family string, assignments ...interfaceintent.Assignment) {
		store.SetAssignment(id, family, "bound", "valid", assignments)
	}
	check("", "")
	publish("ipv4", ipv4)
	publish("ipv6", pd, pd)
	check("2001:db8:10::/56", "192.0.2.13")
	next := pd
	next.Value = netip.MustParsePrefix("2001:db8:20::/56")
	publish("ipv6", pd, next)
	check("", "192.0.2.13")
	publish("ipv6", next)
	ipv4.Value = netip.MustParsePrefix("192.0.2.44/24")
	publish("ipv4", ipv4)
	check("2001:db8:20::/56", "192.0.2.44")
	publish("ipv4")
	check("2001:db8:20::/56", "")
	clock.now = expires
	check("", "")
}
