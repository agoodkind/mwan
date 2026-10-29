//go:build linux && netns

package oobv4

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

func TestDHCPSourceRuleLifecycle(t *testing.T) {
	const childEnv = "MWAN_OOB_SOURCE_RULE_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestDHCPSourceRuleLifecycle$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated source rule test: %v: %s", err, output)
		}
		return
	}
	const iface = "wan-source"
	const priority = 7
	const table = 500
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: iface}}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	uidRule := netlink.NewRule()
	uidRule.Family, uidRule.Priority, uidRule.Table = unix.AF_INET, priority, table
	uidRule.UIDRange = netlink.NewRuleUIDRange(1000, 1000)
	if err := netlink.RuleAdd(uidRule); err != nil {
		t.Fatal(err)
	}
	foreignRule := netlink.NewRule()
	foreignRule.Family, foreignRule.Priority, foreignRule.Table = unix.AF_INET, priority, table
	foreignRule.Src = &net.IPNet{IP: net.IPv4(192, 0, 2, 8), Mask: net.CIDRMask(32, 32)}
	if err := netlink.RuleAdd(foreignRule); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := netif.StartDHCPClient(ctx, slog.Default(), netif.DHCPConfig{Iface: iface, DiscoverTimeout: time.Minute})
	stateFile := filepath.Join(t.TempDir(), "state.json")
	settings := Config{Iface: iface, OOBTableID: table, SourceRulePriority: priority, StateFile: stateFile}
	created, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}
	module := created.(*Module)
	if err := module.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default(), DHCP: client}); err != nil {
		t.Fatal(err)
	}
	lease := netif.LeaseInfo{
		State: netif.LeaseBound, LinkIndex: link.Attrs().Index, LinkHardwareAddr: link.Attrs().HardwareAddr,
		IP: net.IPv4(192, 0, 2, 8), PrefixLen: 24, Routes: []netif.LeaseRoute{{
			Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, Gateway: net.IPv4(192, 0, 2, 1),
		}},
	}
	for index, source := range []string{"192.0.2.8", "192.0.2.9"} {
		if index == 1 {
			created, err = New(settings)
			if err != nil {
				t.Fatal(err)
			}
			module = created.(*Module)
			if err := module.Init(ctx, &ifmgr.Env{Iface: iface, Log: slog.Default(), DHCP: client, DHCPRecoveryPending: true}); err != nil {
				t.Fatal(err)
			}
		}
		lease.IP = net.ParseIP(source)
		if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
			t.Fatal(err)
		}
		assertSourceRules(t, priority, table, source)
	}
	lease.State = netif.LeaseExpired
	if err := module.OnDHCPLease(ctx, slog.Default(), lease); err != nil {
		t.Fatal(err)
	}
	assertSourceRules(t, priority, table, "")
}

func assertSourceRules(t *testing.T, priority, table int, source string) {
	t.Helper()
	rules, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	var owned, uid, foreign int
	for _, rule := range rules {
		if rule.Priority != priority || rule.Table != table {
			continue
		}
		if rule.UIDRange != nil && rule.UIDRange.Start == 1000 && rule.UIDRange.End == 1000 {
			uid++
		}
		if rule.Src != nil && rule.Src.String() == "192.0.2.8/32" && rule.Protocol != sourceRuleProtocol {
			foreign++
		}
		if rule.Protocol == sourceRuleProtocol && rule.Src != nil {
			owned++
			if rule.Src.String() != source+"/32" {
				t.Fatalf("source rule = %s, want %s/32; type=%d goto=%d flow=%d suppress=%d/%d mask=%v", rule.Src, source, rule.Type, rule.Goto, rule.Flow, rule.SuppressIfgroup, rule.SuppressPrefixlen, rule.Mask)
			}
		}
	}
	want := 0
	if source != "" {
		want = 1
	}
	if owned != want || uid != 1 || foreign != 1 {
		t.Fatalf("source rules=%d, UID rules=%d, foreign rules=%d; want %d, 1, 1", owned, uid, foreign, want)
	}
}
