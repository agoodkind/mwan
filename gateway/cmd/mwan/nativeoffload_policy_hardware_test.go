//go:build linux && cgo && nativeoffload && hardware

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/nativeoffload"
	"goodkind.io/mwan/internal/ifmgr/modules/steering"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/offload"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub/schema"
)

const (
	nativePolicyHardwarePoll         = 20 * time.Millisecond
	nativePolicyHardwareDropHandle   = "0x4e50"
	nativePolicyHardwareDropPriority = "49123"
	nativePolicyHardwareDropTarget   = "198.18.254.254/32"
)

// The hardware tag permits fixture tables and a selective DROP in the prepared lab.
// MWAN_CONFIG selects its process file beside network/network.json.
func TestNativePolicyResyncHardware(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("native policy hardware regression requires root in the isolated lab")
	}
	if os.Getenv("MWAN_CONFIG") == "" {
		t.Fatal("set MWAN_CONFIG to the isolated lab process configuration")
	}
	cfg, _, err := config.LoadArguments([]string{"mwan"})
	if err != nil {
		t.Fatal(err)
	}
	models := t.TempDir()
	for _, model := range schema.Modules() {
		data, err := schema.Read(model.File)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(models, model.File), data, schema.FileMode); err != nil {
			t.Fatal(err)
		}
	}
	networkPath := filepath.Join(filepath.Dir(cfg.Path), "network", "network.json")
	network, err := networkload.Load(networkPath, models)
	if err != nil {
		t.Fatal(err)
	}
	network.Apply(cfg)
	modules, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatal(err)
	}
	nativeConfig, ok := modules["nativeoffload"].(nativeoffload.Config)
	if !ok || !nativeConfig.Enabled || !nativeConfig.Firewall.Enabled || nativeConfig.Steering == nil {
		t.Fatal("lab configuration requires enabled native offload, firewall and steering")
	}
	log, closer := logging.New(logging.Config{BuildVersion: "native-policy-hardware"})
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Errorf("close native policy logger: %v", err)
		}
	})
	store := wanstate.New()
	desired, err := firewall.Compile(nativeConfig.Firewall)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := steering.CompilePolicy(*nativeConfig.Steering, store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	desired.Chains = append(desired.Chains, policy.Chains...)
	installNativeHardwarePolicy(t, desired, nativeConfig.Settings.RequestTimeout)
	link := nativeHardwareReceiver(t, cfg)
	prepareNativeHardwareClsact(t, link)

	ctx := t.Context()
	forwarding, err := offload.NewForwardingEvents(ctx, int(nativeConfig.Settings.SocketBufferBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := forwarding.Close(); err != nil {
			t.Errorf("close independent forwarding observer: %v", err)
		}
	})
	rules, err := offload.NewRulesetEvents(ctx, int(nativeConfig.Settings.SocketBufferBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rules.Close(); err != nil {
			t.Errorf("close independent ruleset observer: %v", err)
		}
	})
	checkpointContext, checkpointCancel := context.WithTimeout(ctx, nativeConfig.Settings.RequestTimeout)
	_, err = rules.Barrier(checkpointContext)
	checkpointCancel()
	if err != nil {
		t.Fatalf("establish independent ruleset generation: %v", err)
	}
	env := &ifmgr.Env{
		Log: log, Connections: cfg.IfMgr.Connections, LiveState: store,
		ForwardingChanges: new(ifmgr.ForwardingChanges),
	}
	module := openNativeHardwareModule(t, ctx, nativeConfig, env, log)
	recoveryWindow := 5*nativeConfig.Settings.ResyncInterval + 2*nativeConfig.Settings.RequestTimeout
	waitNativeHardwarePolicy(t, module, false, recoveryWindow, nil)

	// Runtime startup can publish link notifications before certification settles.
	stableWindow := 2*nativeConfig.Settings.ResyncInterval + nativeConfig.Settings.RequestTimeout
	stableNativeHardwareEvidence(t, module, forwarding, rules, stableWindow, recoveryWindow)
	forwardingGeneration, rulesGeneration := forwarding.Generation(), rules.Generation()
	unchanged := func() {
		requireNativeHardwareEvidence(t, forwarding, rules)
		if forwarding.Generation() != forwardingGeneration || rules.Generation() != rulesGeneration {
			t.Fatalf("kernel policy changed during timer recovery: forwarding %d -> %d, ruleset %d -> %d",
				forwardingGeneration, forwarding.Generation(), rulesGeneration, rules.Generation())
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := module.Reconcile(canceled, log); err != nil {
		t.Fatalf("trigger recoverable policy checkpoint cancellation: %v", err)
	}
	requireNativeHardwareBlocked(t, module)
	waitNativeHardwarePolicy(t, module, false, recoveryWindow, unchanged)
	// A delayed forwarding notification would invalidate the timer-only proof.
	observeNativeHardwarePolicy(t, module, false, stableWindow, unchanged)

	requireNativeHardwareEmptyEgress(t, link)
	t.Cleanup(func() {
		if err := removeNativeHardwareDrop(link, nativeConfig.Settings.RequestTimeout); err != nil {
			t.Error(err)
		}
	})
	filterContext, filterCancel := context.WithTimeout(ctx, nativeConfig.Settings.RequestTimeout)
	output, err := exec.CommandContext(filterContext, "tc", "filter", "add", "dev", link.Attrs().Name,
		"egress", "protocol", "ip", "pref", nativePolicyHardwareDropPriority,
		"handle", nativePolicyHardwareDropHandle, "flower", "skip_hw",
		"dst_ip", nativePolicyHardwareDropTarget, "action", "drop").CombinedOutput()
	filterCancel()
	if err != nil {
		t.Fatalf("install lab egress DROP on %s: %v: %s", link.Attrs().Name, err, output)
	}
	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatalf("certify selective lab DROP: %v", err)
	}
	requireNativeHardwareBlocked(t, module)
	waitNativeHardwarePolicy(t, module, true, recoveryWindow, nil)
	observeNativeHardwarePolicy(t, module, true, 3*nativeConfig.Settings.ResyncInterval+2*nativeConfig.Settings.RequestTimeout, nil)
	if err := removeNativeHardwareDrop(link, nativeConfig.Settings.RequestTimeout); err != nil {
		t.Fatal(err)
	}
	waitNativeHardwarePolicy(t, module, false, recoveryWindow, nil)
	if err := module.Shutdown(context.Background(), log); err != nil {
		t.Fatalf("shut down native runtime: %v", err)
	}
}

func openNativeHardwareModule(t *testing.T, ctx context.Context, settings nativeoffload.Config, env *ifmgr.Env, log *slog.Logger) *nativeoffload.Module {
	t.Helper()
	created, err := nativeoffload.New(settings)
	if err != nil {
		t.Fatal(err)
	}
	module, ok := created.(*nativeoffload.Module)
	if !ok {
		t.Fatalf("native constructor returned %T", created)
	}
	t.Cleanup(func() {
		if err := module.Shutdown(context.Background(), log); err != nil {
			t.Errorf("clean up native runtime: %v", err)
		}
	})
	if err := module.Init(ctx, env); err != nil {
		t.Fatalf("initialize real native module: %v", err)
	}
	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatalf("start real native runtime: %v", err)
	}
	return module
}

func nativeHardwareReceiver(t *testing.T, cfg *config.Config) netlink.Link {
	t.Helper()
	for _, connection := range cfg.IfMgr.Connections {
		if connection.Enabled != nil && !*connection.Enabled {
			continue
		}
		if connection.Link == nil || connection.Link.NativeOffload == nil {
			continue
		}
		link, err := netlink.LinkByName(connection.Name)
		if err != nil {
			t.Fatalf("find configured lab receiver %s: %v", connection.Name, err)
		}
		return link
	}
	t.Fatal("lab configuration does not contain an enabled native receiver")
	return nil
}

func prepareNativeHardwareClsact(t *testing.T, link netlink.Link) {
	t.Helper()
	qdiscs, err := netlink.QdiscList(link)
	if err != nil {
		t.Fatal(err)
	}
	for _, qdisc := range qdiscs {
		if qdisc.Type() == "clsact" {
			requireNativeHardwareEmptyEgress(t, link)
			return
		}
	}
	qdisc := &netlink.Clsact{QdiscAttrs: netlink.QdiscAttrs{
		LinkIndex: link.Attrs().Index, Handle: netlink.MakeHandle(0xffff, 0), Parent: netlink.HANDLE_CLSACT,
	}}
	if err := netlink.QdiscAdd(qdisc); err != nil {
		t.Fatalf("create lab clsact on %s: %v", link.Attrs().Name, err)
	}
	t.Cleanup(func() {
		for _, parent := range []uint32{netlink.HANDLE_MIN_INGRESS, netlink.HANDLE_MIN_EGRESS} {
			filters, err := netlink.FilterList(link, parent)
			if err != nil {
				t.Errorf("inspect lab clsact before deletion on %s: %v", link.Attrs().Name, err)
				return
			}
			if len(filters) != 0 {
				t.Errorf("preserve lab clsact on %s with remaining filters: %v", link.Attrs().Name, filters)
				return
			}
		}
		if err := netlink.QdiscDel(qdisc); err != nil {
			t.Errorf("delete lab clsact on %s: %v", link.Attrs().Name, err)
		}
	})
	requireNativeHardwareEmptyEgress(t, link)
}

func requireNativeHardwareEmptyEgress(t *testing.T, link netlink.Link) {
	t.Helper()
	filters, err := netlink.FilterList(link, netlink.HANDLE_MIN_EGRESS)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 0 {
		t.Fatalf("configured lab receiver %s already has egress filters", link.Attrs().Name)
	}
}

func removeNativeHardwareDrop(link netlink.Link, timeout time.Duration) error {
	filters, err := netlink.FilterList(link, netlink.HANDLE_MIN_EGRESS)
	if err != nil {
		return fmt.Errorf("inspect lab egress DROP on %s: %w", link.Attrs().Name, err)
	}
	if len(filters) == 0 {
		return nil
	}
	if len(filters) != 1 {
		return fmt.Errorf("preserve unexpected egress filters on %s: %v", link.Attrs().Name, filters)
	}
	filter := filters[0]
	attributes := filter.Attrs()
	if attributes.LinkIndex != link.Attrs().Index || attributes.Parent != netlink.HANDLE_MIN_EGRESS ||
		attributes.Protocol != unix.ETH_P_IP || fmt.Sprint(attributes.Priority) != nativePolicyHardwareDropPriority ||
		fmt.Sprintf("0x%x", attributes.Handle) != nativePolicyHardwareDropHandle || attributes.Chain != nil && *attributes.Chain != 0 {
		return fmt.Errorf("preserve mismatched lab egress filter on %s: %v", link.Attrs().Name, attributes)
	}
	flower, ok := filter.(*netlink.Flower)
	if !ok {
		return fmt.Errorf("preserve unexpected classifier %s on %s", filter.Type(), link.Attrs().Name)
	}
	target := netip.MustParsePrefix(nativePolicyHardwareDropTarget)
	destination, valid := netip.AddrFromSlice(flower.DestIP)
	ones, bits := flower.DestIPMask.Size()
	if !valid || destination.Unmap() != target.Addr() || ones != target.Bits() || bits != target.Addr().BitLen() ||
		flower.EthType != unix.ETH_P_IP || len(flower.Actions) != 1 {
		return fmt.Errorf("preserve mismatched Flower selector on %s: %+v", link.Attrs().Name, flower)
	}
	action, ok := flower.Actions[0].(*netlink.GenericAction)
	if !ok || action.Action != netlink.TC_ACT_SHOT {
		return fmt.Errorf("preserve mismatched Flower action on %s: %v", link.Attrs().Name, flower.Actions)
	}
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), timeout)
	defer cleanupCancel()
	output, err := exec.CommandContext(cleanupContext, "tc", "filter", "del", "dev", link.Attrs().Name,
		"egress", "protocol", "ip", "pref", nativePolicyHardwareDropPriority,
		"handle", nativePolicyHardwareDropHandle, "flower").CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete lab egress DROP on %s: %w: %s", link.Attrs().Name, err, output)
	}
	return nil
}

func installNativeHardwarePolicy(t *testing.T, desired firewall.Ruleset, timeout time.Duration) {
	t.Helper()
	tables := make(map[firewall.Table]bool)
	for _, chain := range desired.Chains {
		tables[chain.Table] = true
	}
	for _, set := range desired.Sets {
		tables[set.Table] = true
	}
	existing, err := (&nftables.Conn{}).ListTables()
	if err != nil {
		t.Fatal(err)
	}
	families := make(map[firewall.Table]nftables.TableFamily, len(tables))
	for table := range tables {
		var family nftables.TableFamily
		switch table.Family {
		case "inet":
			family = nftables.TableFamilyINet
		case "ip":
			family = nftables.TableFamilyIPv4
		case "ip6":
			family = nftables.TableFamilyIPv6
		default:
			t.Fatalf("unsupported fixture table family %s", table.Family)
		}
		families[table] = family
		for _, present := range existing {
			if present.Family == family && present.Name == table.Name {
				t.Fatalf("lab fixture table %s %s already exists", table.Family, table.Name)
			}
		}
	}
	t.Cleanup(func() {
		present, err := (&nftables.Conn{}).ListTables()
		if err != nil {
			t.Errorf("inspect lab tables before deletion: %v", err)
			return
		}
		for table, family := range families {
			for _, actual := range present {
				if actual.Family != family || actual.Name != table.Name {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				output, err := exec.CommandContext(ctx, "nft", "delete", "table", table.Family, table.Name).CombinedOutput()
				cancel()
				if err != nil {
					t.Errorf("delete lab table %s %s: %v: %s", table.Family, table.Name, err, output)
				}
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	if err := firewall.Apply(ctx, desired); err != nil {
		t.Fatalf("install lab firewall fixture: %v", err)
	}
	if err := firewall.InspectNative(ctx, desired); err != nil {
		t.Fatalf("certify lab firewall fixture: %v", err)
	}
}

func requireNativeHardwareStats(t *testing.T, module *nativeoffload.Module) nativeoffload.AdmissionStats {
	t.Helper()
	stats := module.Stats()
	if stats.Failure != nil {
		t.Fatalf("native module entered terminal failure: %v", stats.Failure)
	}
	if stats.Installed != 0 || stats.Active != 0 || stats.Tracked != 0 || stats.ResumeCandidates != 0 || stats.CleanupUncertain != 0 {
		t.Fatalf("certification regression requires empty ownership: %+v", stats)
	}
	return stats
}

func requireNativeHardwareBlocked(t *testing.T, module *nativeoffload.Module) {
	t.Helper()
	stats := requireNativeHardwareStats(t, module)
	if !stats.PolicyBlocked || !stats.Paused {
		t.Fatalf("native module did not pause after failed certification: %+v", stats)
	}
}

func waitNativeHardwarePolicy(t *testing.T, module *nativeoffload.Module, blocked bool, timeout time.Duration, inspect func()) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(nativePolicyHardwarePoll)
	defer ticker.Stop()
	for {
		if inspect != nil {
			inspect()
		}
		stats := requireNativeHardwareStats(t, module)
		if stats.PolicyBlocked == blocked {
			if blocked {
				requireNativeHardwareBlocked(t, module)
			}
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-deadline.C:
			t.Fatalf("native policy did not become blocked=%t within %s: %+v", blocked, timeout, stats)
		case <-ticker.C:
		}
	}
}

func observeNativeHardwarePolicy(t *testing.T, module *nativeoffload.Module, blocked bool, duration time.Duration, inspect func()) {
	t.Helper()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(nativePolicyHardwarePoll)
	defer ticker.Stop()
	for {
		if inspect != nil {
			inspect()
		}
		stats := requireNativeHardwareStats(t, module)
		if stats.PolicyBlocked != blocked || blocked && !stats.Paused {
			t.Fatalf("native policy changed during required blocked=%t interval: %+v", blocked, stats)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

func requireNativeHardwareEvidence(t *testing.T, forwarding *offload.ForwardingEvents, rules *offload.RulesetEvents) {
	t.Helper()
	if err := errors.Join(forwarding.Err(), rules.Err()); err != nil {
		t.Fatalf("independent kernel observers failed: %v", err)
	}
}

func stableNativeHardwareEvidence(t *testing.T, module *nativeoffload.Module, forwarding *offload.ForwardingEvents, rules *offload.RulesetEvents, duration, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(nativePolicyHardwarePoll)
	defer ticker.Stop()
	stableSince := time.Now()
	forwardingGeneration, rulesGeneration := forwarding.Generation(), rules.Generation()
	for {
		requireNativeHardwareEvidence(t, forwarding, rules)
		stats := requireNativeHardwareStats(t, module)
		if stats.PolicyBlocked || forwarding.Generation() != forwardingGeneration || rules.Generation() != rulesGeneration {
			stableSince = time.Now()
			forwardingGeneration, rulesGeneration = forwarding.Generation(), rules.Generation()
		}
		if time.Since(stableSince) >= duration {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-deadline.C:
			t.Fatalf("lab policy did not remain certified with stable kernel generations for %s", duration)
		case <-ticker.C:
		}
	}
}
