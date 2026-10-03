//go:build linux && netns

package netif

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestOwnedLinkAliasOnCreate(t *testing.T) {
	const childEnv = "MWAN_OWNED_LINK_ALIAS_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedLinkAliasOnCreate$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated alias test: %v: %s", err, output)
		}
		return
	}
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "alias-parent"}}); err != nil {
		t.Fatal(err)
	}
	parent, err := netlink.LinkByName("alias-parent")
	if err != nil {
		t.Fatal(err)
	}
	links := []netlink.Link{
		&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "alias-vlan", Alias: "mwan-link:alias-vlan", ParentIndex: parent.Attrs().Index}, VlanId: 397, VlanProtocol: netlink.VLAN_PROTOCOL_8021Q},
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "alias-bridge", Alias: "mwan-link:alias-bridge"}},
	}
	for _, requested := range links {
		if err := netlink.LinkAdd(requested); err != nil {
			t.Fatal(err)
		}
		observed, err := netlink.LinkByName(requested.Attrs().Name)
		if err != nil {
			t.Fatal(err)
		}
		if observed.Attrs().Alias != requested.Attrs().Alias {
			if observed.Attrs().Alias != "" {
				t.Errorf("%s: unexpected alias %q", requested.Attrs().Name, observed.Attrs().Alias)
			}
			t.Logf("%s: LinkAdd ignored requested alias; staged creation is required", requested.Attrs().Name)
		}
	}
}

func TestOwnedLinkReservationFromOlderBoot(t *testing.T) {
	const childEnv = "MWAN_OWNED_LINK_OLDER_BOOT_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedLinkReservationFromOlderBoot$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated older boot reservation test: %v: %s", err, output)
		}
		return
	}
	connection := interfaceintent.Connection{
		ID: "older-boot-bridge", Name: "older-boot-br",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	statePath := filepath.Join(t.TempDir(), "links.json")
	interrupted, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	interrupted.state.Virtuals[connection.ID.String()] = virtualRecord{
		BootID: "previous-boot", ConnectionID: connection.ID.String(),
		Alias:    aliasFor(connection.ID.String()) + ":older-boot-token",
		TempName: "mw-older-boot", Name: connection.Name, Kind: string(interfaceintent.KindBridge),
	}
	if err := interrupted.save(); err != nil {
		t.Fatal(err)
	}
	rebooted, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	results, err := rebooted.Reconcile(context.Background(), log, []interfaceintent.Connection{connection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("reservation from an older boot blocked link creation: results=%+v err=%v", results, err)
	}
	bridge, err := netlink.LinkByName(connection.Name)
	if err != nil {
		t.Fatalf("owned bridge is absent after reconciliation: %v", err)
	}
	record := rebooted.state.Virtuals[connection.ID.String()]
	if !record.Complete || record.BootID != rebooted.bootID || bridge.Attrs().Alias != record.Alias {
		t.Fatalf("journal kept the reservation from the older boot: record=%+v alias=%q", record, bridge.Attrs().Alias)
	}
}

func TestOwnedLinksCreateRestartAndRemove(t *testing.T) {
	const childEnv = "MWAN_OWNED_LINK_LIFECYCLE_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedLinksCreateRestartAndRemove$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated owned link test: %v: %s", err, output)
		}
		return
	}
	parentMAC, err := net.ParseMAC("02:00:5e:00:39:71")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "owned-parent", HardwareAddr: parentMAC}, PeerName: "owned-peer"}); err != nil {
		t.Fatal(err)
	}
	parent, err := netlink.LinkByName("owned-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(parent); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "foreign-br"}}); err != nil {
		t.Fatal(err)
	}
	parentConnection := interfaceintent.Connection{
		ID: "owned-parent", Name: "configured-parent", Owner: interfaceintent.OwnerExternal,
		Link: &interfaceintent.Link{
			Kind:  interfaceintent.KindPhysical,
			Match: interfaceintent.Match{HardwareAddress: parentMAC.String()},
		},
	}
	vlanConnection := interfaceintent.Connection{
		ID: "owned-vlan", Name: "owned-vlan", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindVLAN, VLAN: &interfaceintent.VLAN{Parent: parentConnection.Name, ID: 397}, BridgeMaster: "owned-bridge"},
	}
	bridgeConnection := interfaceintent.Connection{
		ID: "owned-bridge", Name: "owned-bridge", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	connections := []interfaceintent.Connection{vlanConnection, parentConnection, bridgeConnection}
	statePath := filepath.Join(t.TempDir(), "links.json")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reconciler, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err := reconciler.Reconcile(context.Background(), log, connections)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Err != nil || result.Status != OwnedLinkReady && result.Status != OwnedLinkPendingRename {
			t.Fatalf("initial result = %+v", result)
		}
	}
	vlan, err := netlink.LinkByName("owned-vlan")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := netlink.LinkByName("owned-bridge")
	if err != nil {
		t.Fatal(err)
	}
	if vlan.Attrs().ParentIndex != parent.Attrs().Index || vlan.Attrs().MasterIndex != bridge.Attrs().Index ||
		vlan.Attrs().Alias != reconciler.state.Virtuals[vlanConnection.ID.String()].Alias ||
		bridge.Attrs().Alias != reconciler.state.Virtuals[bridgeConnection.ID.String()].Alias {
		t.Fatalf("created links: vlan=%+v bridge=%+v", vlan, bridge)
	}
	vlanIndex, bridgeIndex := vlan.Attrs().Index, bridge.Attrs().Index
	lostJournal, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "lost.json"))
	if err != nil {
		t.Fatal(err)
	}
	lostResults, err := lostJournal.Reconcile(context.Background(), log, connections)
	if err != nil || len(lostResults) != 2 {
		t.Fatalf("lost journal reconcile: results=%+v err=%v", lostResults, err)
	}
	for _, result := range lostResults {
		if result.Status != OwnedLinkFailed || result.Err == nil {
			t.Fatalf("lost journal adopted tagged link: %+v", result)
		}
	}
	vlan, err = netlink.LinkByName(vlanConnection.Name)
	if err != nil || vlan.Attrs().Index != vlanIndex {
		t.Fatalf("lost journal changed VLAN: link=%+v err=%v", vlan, err)
	}
	bridge, err = netlink.LinkByName(bridgeConnection.Name)
	if err != nil || bridge.Attrs().Index != bridgeIndex {
		t.Fatalf("lost journal changed bridge: link=%+v err=%v", bridge, err)
	}
	stale, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	originalRecord := stale.state.Virtuals[bridgeConnection.ID.String()]
	staleRecord := originalRecord
	staleRecord.BootID = "previous-boot"
	stale.state.Virtuals[bridgeConnection.ID.String()] = staleRecord
	staleResults, err := stale.Reconcile(context.Background(), log, connections)
	if err != nil || len(staleResults) != 2 || staleResults[1].Status != OwnedLinkFailed {
		t.Fatalf("stale boot journal adopted bridge: results=%+v err=%v", staleResults, err)
	}
	staleRecord.BootID = stale.bootID
	staleRecord.LinkIndex = bridgeIndex + 1
	stale.state.Virtuals[bridgeConnection.ID.String()] = staleRecord
	staleResults, err = stale.Reconcile(context.Background(), log, connections)
	if err != nil || len(staleResults) != 2 || staleResults[1].Status != OwnedLinkFailed {
		t.Fatalf("replaced bridge index adopted: results=%+v err=%v", staleResults, err)
	}
	staleResults, err = stale.Reconcile(context.Background(), log, []interfaceintent.Connection{vlanConnection, parentConnection})
	if err != nil || len(staleResults) != 1 {
		t.Fatalf("stale journal pruned bridge: results=%+v err=%v", staleResults, err)
	}
	if _, err := netlink.LinkByName(bridgeConnection.Name); err != nil {
		t.Fatalf("stale journal deleted bridge: %v", err)
	}
	stale.state.Virtuals[bridgeConnection.ID.String()] = originalRecord
	if err := stale.save(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reconcile(context.Background(), log, connections); err != nil {
		t.Fatal(err)
	}
	vlan, err = netlink.LinkByName("owned-vlan")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err = netlink.LinkByName("owned-bridge")
	if err != nil {
		t.Fatal(err)
	}
	if vlan.Attrs().Index != vlanIndex || bridge.Attrs().Index != bridgeIndex {
		t.Fatal("restart recreated owned links")
	}
	renamedBridge := bridgeConnection
	renamedBridge.Name = "owned-br-new"
	renamedVLAN := vlanConnection
	renamedLink := *vlanConnection.Link
	renamedLink.BridgeMaster = renamedBridge.Name
	renamedVLAN.Link = &renamedLink
	results, err = restarted.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{renamedVLAN, parentConnection, renamedBridge})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Status != OwnedLinkReady {
			t.Fatalf("renamed configuration result = %+v", result)
		}
	}
	bridge, err = netlink.LinkByName(renamedBridge.Name)
	if err != nil || bridge.Attrs().Index != bridgeIndex {
		t.Fatalf("owned bridge rename: link=%+v err=%v", bridge, err)
	}
	if err := netlink.LinkSetName(bridge, "external-br"); err != nil {
		t.Fatal(err)
	}
	results, err = restarted.Reconcile(context.Background(), log, []interfaceintent.Connection{parentConnection})
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, result := range results {
		if result.Status == OwnedLinkRemoved {
			removed++
		}
	}
	if removed != 2 {
		t.Fatalf("removed %d owned links, want 2: %+v", removed, results)
	}
	if _, err := netlink.LinkByName("owned-vlan"); !IsLinkNotFound(err) {
		t.Fatalf("owned VLAN still exists: %v", err)
	}
	if _, err := netlink.LinkByName("external-br"); !IsLinkNotFound(err) {
		t.Fatalf("owned bridge still exists: %v", err)
	}
	if _, err := netlink.LinkByName("foreign-br"); err != nil {
		t.Fatalf("foreign bridge removed: %v", err)
	}
	if _, err := netlink.LinkByName("owned-parent"); err != nil {
		t.Fatalf("external parent removed: %v", err)
	}
	recovery := interfaceintent.Connection{
		ID: "recovered-bridge", Name: "recover-br",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	restarted.state.Virtuals[recovery.ID.String()] = virtualRecord{
		BootID: restarted.bootID, ConnectionID: recovery.ID.String(), Alias: aliasFor(recovery.ID.String()) + ":recovery-token",
		TempName: "mw-recovery", Name: recovery.Name,
		Kind: string(interfaceintent.KindBridge),
	}
	if err := restarted.save(); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "mw-recovery"}}); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err = recovered.Reconcile(context.Background(), log, []interfaceintent.Connection{recovery})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("recover reserved bridge: results=%+v err=%v", results, err)
	}
	if _, err := netlink.LinkByName("mw-recovery"); !IsLinkNotFound(err) {
		t.Fatalf("temporary bridge remains: %v", err)
	}
	bridge, err = netlink.LinkByName(recovery.Name)
	if err != nil || bridge.Attrs().Alias != recovered.state.Virtuals[recovery.ID.String()].Alias {
		t.Fatalf("recovered bridge: link=%+v err=%v", bridge, err)
	}
	interrupted := interfaceintent.Connection{
		ID: "renamed-before-save", Name: "before-save",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	recovered.state.Virtuals[interrupted.ID.String()] = virtualRecord{
		BootID: recovered.bootID, ConnectionID: interrupted.ID.String(), Alias: aliasFor(interrupted.ID.String()) + ":before-save-token",
		TempName: "mw-before-save", Name: interrupted.Name, Kind: string(interfaceintent.KindBridge),
	}
	if err := recovered.save(); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: interrupted.Name}}); err != nil {
		t.Fatal(err)
	}
	interruptedLink, err := netlink.LinkByName(interrupted.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetAlias(interruptedLink, recovered.state.Virtuals[interrupted.ID.String()].Alias); err != nil {
		t.Fatal(err)
	}
	crashRestart, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err = crashRestart.Reconcile(context.Background(), log, []interfaceintent.Connection{recovery, interrupted})
	if err != nil || len(results) != 2 || results[1].Status != OwnedLinkReady {
		t.Fatalf("renamed before journal completion: results=%+v err=%v", results, err)
	}
	if record := crashRestart.state.Virtuals[interrupted.ID.String()]; !record.Complete || record.LinkIndex != interruptedLink.Attrs().Index {
		t.Fatalf("interrupted rename journal = %+v", record)
	}
	collision := interfaceintent.Connection{
		ID: "alias-collision", Name: "alias-collision",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	collisionState, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "collision.json"))
	if err != nil {
		t.Fatal(err)
	}
	collisionState.state.Virtuals[collision.ID.String()] = virtualRecord{
		BootID: collisionState.bootID, ConnectionID: collision.ID.String(),
		Alias:    aliasFor(collision.ID.String()) + ":reserved-token",
		TempName: "mw-collision", Name: collision.Name, Kind: string(interfaceintent.KindBridge),
	}
	if err := collisionState.save(); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: collision.Name}}); err != nil {
		t.Fatal(err)
	}
	foreignCollision, err := netlink.LinkByName(collision.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetAlias(foreignCollision, aliasFor(collision.ID.String())); err != nil {
		t.Fatal(err)
	}
	collisionRestart, err := NewOwnedLinkReconciler(collisionState.statePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err = collisionRestart.Reconcile(context.Background(), log, []interfaceintent.Connection{collision})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkFailed {
		t.Fatalf("predictable alias was adopted: results=%+v err=%v", results, err)
	}
	foreignCollision, err = netlink.LinkByName(collision.Name)
	if err != nil || foreignCollision.Attrs().Alias != aliasFor(collision.ID.String()) {
		t.Fatalf("foreign alias collision changed: link=%+v err=%v", foreignCollision, err)
	}
	untagged := interfaceintent.Connection{
		ID: "untagged-name", Name: "untagged-name",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: untagged.Name}}); err != nil {
		t.Fatal(err)
	}
	foreignUntagged, err := netlink.LinkByName(untagged.Name)
	if err != nil {
		t.Fatal(err)
	}
	untaggedState, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "untagged.json"))
	if err != nil {
		t.Fatal(err)
	}
	results, err = untaggedState.Reconcile(context.Background(), log, []interfaceintent.Connection{untagged})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkFailed {
		t.Fatalf("foreign untagged name was accepted: results=%+v err=%v", results, err)
	}
	if _, exists := untaggedState.state.Virtuals[untagged.ID.String()]; exists {
		t.Fatal("foreign untagged name left a reservation")
	}
	foreignUntaggedAfter, err := netlink.LinkByName(untagged.Name)
	if err != nil || foreignUntaggedAfter.Attrs().Index != foreignUntagged.Attrs().Index {
		t.Fatalf("foreign untagged link changed: link=%+v err=%v", foreignUntaggedAfter, err)
	}
	interruptedRemoval, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "interrupted-removal.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mw-prune-temp", "prune-final"} {
		id := "prune-" + name
		interruptedRemoval.state.Virtuals[id] = virtualRecord{
			BootID: interruptedRemoval.bootID, ConnectionID: id,
			Alias: aliasFor(id) + ":reserved-token", TempName: "mw-prune-temp",
			Name: "prune-final", Kind: string(interfaceintent.KindBridge),
		}
		if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetAlias(link, interruptedRemoval.state.Virtuals[id].Alias); err != nil {
			t.Fatal(err)
		}
	}
	interruptedRemoval.state.Virtuals["untagged-reservation"] = virtualRecord{
		BootID: interruptedRemoval.bootID, ConnectionID: "untagged-reservation",
		Alias:    aliasFor("untagged-reservation") + ":reserved-token",
		TempName: "mw-untagged", Name: "untagged-final", Kind: string(interfaceintent.KindBridge),
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "mw-untagged"}}); err != nil {
		t.Fatal(err)
	}
	if err := interruptedRemoval.save(); err != nil {
		t.Fatal(err)
	}
	removedIncomplete, err := interruptedRemoval.Reconcile(context.Background(), log, nil)
	if err != nil || len(removedIncomplete) != 2 {
		t.Fatalf("interrupted tagged links not removed: results=%+v err=%v", removedIncomplete, err)
	}
	for _, name := range []string{"mw-prune-temp", "prune-final"} {
		if _, err := netlink.LinkByName(name); !IsLinkNotFound(err) {
			t.Fatalf("interrupted tagged link %s remains: %v", name, err)
		}
	}
	if _, err := netlink.LinkByName("mw-untagged"); err != nil {
		t.Fatalf("untagged reservation was removed: %v", err)
	}
	if _, exists := interruptedRemoval.state.Virtuals["untagged-reservation"]; !exists {
		t.Fatal("untagged reservation was forgotten")
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "retry-parent"}}); err != nil {
		t.Fatal(err)
	}
	retryParent, err := netlink.LinkByName("retry-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkDel(retryParent); err != nil {
		t.Fatal(err)
	}
	retryConnection := interfaceintent.Connection{
		ID: "retry-vlan", Name: "retry-vlan", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind: interfaceintent.KindVLAN,
			VLAN: &interfaceintent.VLAN{Parent: "retry-parent", ID: 398},
		},
	}
	retryParentConnection := interfaceintent.Connection{
		ID: "retry-parent", Name: "retry-parent",
		Owner: interfaceintent.OwnerExternal, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	retryStatePath := filepath.Join(t.TempDir(), "retry.json")
	retryReconciler, err := NewOwnedLinkReconciler(retryStatePath)
	if err != nil {
		t.Fatal(err)
	}
	retryResults, err := retryReconciler.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{retryParentConnection, retryConnection})
	if err != nil || len(retryResults) != 1 || retryResults[0].Status != OwnedLinkWaiting ||
		retryResults[0].Operation != "wait-for-parent" {
		t.Fatalf("deleted parent result: results=%+v err=%v", retryResults, err)
	}
	onDisk, err := NewOwnedLinkReconciler(retryStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := onDisk.state.Virtuals[retryConnection.ID.String()]; exists {
		t.Fatal("failed LinkAdd left a blocking reservation")
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "retry-parent"}}); err != nil {
		t.Fatal(err)
	}
	retryParent, err = netlink.LinkByName("retry-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(retryParent); err != nil {
		t.Fatal(err)
	}
	results, err = onDisk.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{retryParentConnection, retryConnection})
	if err != nil || len(results) < 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("retry after parent recreation: results=%+v err=%v", results, err)
	}
	var waitGroup sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results, err := onDisk.Reconcile(context.Background(), log,
				[]interfaceintent.Connection{retryParentConnection, retryConnection})
			if err != nil {
				failures <- err
				return
			}
			if len(results) != 1 || results[0].Status != OwnedLinkReady {
				failures <- fmt.Errorf("concurrent reconcile results: %+v", results)
			}
		}()
	}
	waitGroup.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if _, err := NewOwnedLinkReconciler(statePath); err != nil {
		t.Fatalf("concurrent reconciliation corrupted the journal: %v", err)
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "mac-parent"}}); err != nil {
		t.Fatal(err)
	}
	bridgeParent, err := netlink.LinkByName("mac-parent")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(bridgeParent); err != nil {
		t.Fatal(err)
	}
	bridgeParentConnection := interfaceintent.Connection{
		ID: "mac-parent", Name: "mac-parent", Owner: interfaceintent.OwnerExternal,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	bridgeChild := interfaceintent.Connection{
		ID: "mac-vlan", Name: "mac-vlan", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind: interfaceintent.KindVLAN,
			VLAN: &interfaceintent.VLAN{Parent: bridgeParentConnection.Name, ID: 400},
		},
	}
	bridgeChildStatePath := filepath.Join(t.TempDir(), "bridge-parent.json")
	bridgeChildState, err := NewOwnedLinkReconciler(bridgeChildStatePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err = bridgeChildState.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{bridgeParentConnection, bridgeChild})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("bridge-parent VLAN creation: results=%+v err=%v", results, err)
	}
	for _, member := range []struct {
		name string
		peer string
		mac  net.HardwareAddr
	}{
		{"mac-high", "mac-high-peer", net.HardwareAddr{2, 0, 94, 0, 58, 255}},
		{"mac-low", "mac-low-peer", net.HardwareAddr{2, 0, 94, 0, 58, 1}},
	} {
		if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: member.name, HardwareAddr: member.mac}, PeerName: member.peer}); err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(member.name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetMasterByIndex(link, bridgeParent.Attrs().Index); err != nil {
			t.Fatal(err)
		}
	}
	bridgeParentAfter, err := netlink.LinkByName("mac-parent")
	if err != nil || bridgeParentAfter.Attrs().HardwareAddr.String() == bridgeParent.Attrs().HardwareAddr.String() {
		t.Fatalf("bridge-parent MAC did not change: link=%+v err=%v", bridgeParentAfter, err)
	}
	bridgeChildRestart, err := NewOwnedLinkReconciler(bridgeChildStatePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err = bridgeChildRestart.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{bridgeParentConnection, bridgeChild})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("bridge-parent MAC drift blocked adoption: results=%+v err=%v", results, err)
	}
	results, err = bridgeChildRestart.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{bridgeParentConnection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkRemoved {
		t.Fatalf("bridge-parent MAC drift blocked removal: results=%+v err=%v", results, err)
	}
	taggedParent := interfaceintent.Connection{
		ID: "tag-parent", Name: "tag-parent", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	taggedChild := interfaceintent.Connection{
		ID: "tag-vlan", Name: "tag-vlan", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind: interfaceintent.KindVLAN,
			VLAN: &interfaceintent.VLAN{Parent: taggedParent.Name, ID: 401},
		},
	}
	taggedStatePath := filepath.Join(t.TempDir(), "tagged-parent.json")
	taggedState, err := NewOwnedLinkReconciler(taggedStatePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err = taggedState.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{taggedChild, taggedParent})
	if err != nil || len(results) != 2 || results[0].Status != OwnedLinkReady || results[1].Status != OwnedLinkReady {
		t.Fatalf("tagged parent creation: results=%+v err=%v", results, err)
	}
	renamedTaggedParent := taggedParent
	renamedTaggedParent.Name = "tag-parent-new"
	renamedTaggedChild := taggedChild
	renamedTaggedVLAN := *taggedChild.Link
	renamedTaggedVLAN.VLAN = &interfaceintent.VLAN{Parent: renamedTaggedParent.Name, ID: 401}
	renamedTaggedChild.Link = &renamedTaggedVLAN
	taggedRestart, err := NewOwnedLinkReconciler(taggedStatePath)
	if err != nil {
		t.Fatal(err)
	}
	results, err = taggedRestart.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{renamedTaggedChild, renamedTaggedParent})
	if err != nil || len(results) != 2 || results[0].Status != OwnedLinkReady || results[1].Status != OwnedLinkReady {
		t.Fatalf("tagged parent rename blocked VLAN adoption: results=%+v err=%v", results, err)
	}
	waiting, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "waiting.json"))
	if err != nil {
		t.Fatal(err)
	}
	missingParent := interfaceintent.Connection{
		ID: "absent-parent", Name: "absent-parent",
		Owner: interfaceintent.OwnerExternal, Link: &interfaceintent.Link{
			Kind:  interfaceintent.KindPhysical,
			Match: interfaceintent.Match{HardwareAddress: "02:00:5e:00:39:99"},
		},
	}
	missingMaster := interfaceintent.Connection{
		ID: "absent-br", Name: "absent-br",
		Owner: interfaceintent.OwnerExternal, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	waitingVLAN := interfaceintent.Connection{
		ID: "wait-vlan", Name: "wait-vlan",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{
			Kind: interfaceintent.KindVLAN,
			VLAN: &interfaceintent.VLAN{Parent: missingParent.Name, ID: 399},
		},
	}
	waitingMember := interfaceintent.Connection{
		ID: "wait-member", Name: "wait-member",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{
			Kind:         interfaceintent.KindBridge,
			BridgeMaster: missingMaster.Name,
		},
	}
	independent := interfaceintent.Connection{
		ID: "independent", Name: "independent",
		Owner: interfaceintent.OwnerMWAN, Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	results, err = waiting.Reconcile(context.Background(), log, []interfaceintent.Connection{
		waitingVLAN, waitingMember, independent, missingParent, missingMaster,
	})
	if err != nil || len(results) != 3 {
		t.Fatalf("missing dependency reconcile: results=%+v err=%v", results, err)
	}
	if results[0].Status != OwnedLinkWaiting || results[0].Operation != "wait-for-parent" ||
		results[0].Dependency != missingParent.Name || results[0].Err == nil {
		t.Fatalf("missing parent result = %+v", results[0])
	}
	if results[1].Status != OwnedLinkWaiting || results[1].Operation != "wait-for-bridge" ||
		results[1].Dependency != missingMaster.Name || results[1].Err == nil {
		t.Fatalf("missing bridge result = %+v", results[1])
	}
	if results[2].Status != OwnedLinkReady {
		t.Fatalf("independent bridge result = %+v", results[2])
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "replace-br"}}); err != nil {
		t.Fatal(err)
	}
	oldMaster, err := netlink.LinkByName("replace-br")
	if err != nil {
		t.Fatal(err)
	}
	masterConnection := interfaceintent.Connection{
		ID: "replace-br", Name: "replace-br", Owner: interfaceintent.OwnerExternal,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	memberConnection := interfaceintent.Connection{
		ID: "member-vlan", Name: "member-vlan", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind:         interfaceintent.KindVLAN,
			VLAN:         &interfaceintent.VLAN{Parent: parentConnection.Name, ID: 402},
			BridgeMaster: masterConnection.Name, HardwareAddress: "02:00:5e:00:39:ff",
		},
	}
	membership, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "membership.json"))
	if err != nil {
		t.Fatal(err)
	}
	results, err = membership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, masterConnection, memberConnection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("attach owned VLAN: results=%+v err=%v", results, err)
	}
	memberLink, err := netlink.LinkByName(memberConnection.Name)
	if err != nil || memberLink.Attrs().MasterIndex != oldMaster.Attrs().Index {
		t.Fatalf("owned VLAN not attached: link=%+v err=%v", memberLink, err)
	}
	if _, exists := membership.state.Memberships[memberConnection.ID.String()]; !exists {
		t.Fatal("attached VLAN has no membership record")
	}
	firstMaster, err := netlink.LinkByName("replace-br")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "low-slave", HardwareAddr: net.HardwareAddr{2, 0, 94, 0, 57, 1}}, PeerName: "low-peer"}); err != nil {
		t.Fatal(err)
	}
	lowSlave, err := netlink.LinkByName("low-slave")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMasterByIndex(lowSlave, oldMaster.Attrs().Index); err != nil {
		t.Fatal(err)
	}
	secondMaster, err := netlink.LinkByName("replace-br")
	if err != nil {
		t.Fatal(err)
	}
	if firstMaster.Attrs().HardwareAddr.String() == secondMaster.Attrs().HardwareAddr.String() {
		t.Fatal("second member did not change the bridge MAC")
	}
	detachedMember := memberConnection
	detachedLink := *memberConnection.Link
	detachedLink.BridgeMaster = ""
	detachedMember.Link = &detachedLink
	results, err = membership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, masterConnection, detachedMember})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("bridge MAC change blocked recorded detach: results=%+v err=%v", results, err)
	}
	memberLink, err = netlink.LinkByName(memberConnection.Name)
	if err != nil || memberLink.Attrs().MasterIndex != 0 {
		t.Fatalf("owned VLAN remained attached: link=%+v err=%v", memberLink, err)
	}
	if _, exists := membership.state.Memberships[memberConnection.ID.String()]; exists {
		t.Fatal("detached VLAN retained a membership record")
	}
	results, err = membership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, masterConnection, memberConnection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("reattach owned VLAN: results=%+v err=%v", results, err)
	}
	if err := netlink.LinkDel(oldMaster); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "replace-br"}}); err != nil {
		t.Fatal(err)
	}
	newMaster, err := netlink.LinkByName("replace-br")
	if err != nil {
		t.Fatal(err)
	}
	if newMaster.Attrs().Index == oldMaster.Attrs().Index {
		t.Fatal("replacement bridge reused the old index")
	}
	memberLink, err = netlink.LinkByName(memberConnection.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMasterByIndex(memberLink, newMaster.Attrs().Index); err != nil {
		t.Fatal(err)
	}
	results, err = membership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, masterConnection, memberConnection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkFailed {
		t.Fatalf("replacement bridge was accepted as recorded master: results=%+v err=%v", results, err)
	}
	results, err = membership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, masterConnection, detachedMember})
	if err == nil || len(results) != 1 || results[0].Status != OwnedLinkFailed {
		t.Fatalf("replacement bridge membership was detached: results=%+v err=%v", results, err)
	}
	memberLink, err = netlink.LinkByName(memberConnection.Name)
	if err != nil || memberLink.Attrs().MasterIndex != newMaster.Attrs().Index {
		t.Fatalf("owned VLAN changed under replacement bridge: link=%+v err=%v", memberLink, err)
	}
	if _, exists := membership.state.Memberships[memberConnection.ID.String()]; !exists {
		t.Fatal("conflicting membership record was forgotten")
	}
	unrecordedConnection := interfaceintent.Connection{
		ID: "unrecorded-vlan", Name: "unrecorded-vlan", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind: interfaceintent.KindVLAN,
			VLAN: &interfaceintent.VLAN{Parent: parentConnection.Name, ID: 404},
		},
	}
	unrecordedState, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "unrecorded-membership.json"))
	if err != nil {
		t.Fatal(err)
	}
	results, err = unrecordedState.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, masterConnection, unrecordedConnection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("create unrecorded VLAN: results=%+v err=%v", results, err)
	}
	unrecordedLink, err := netlink.LinkByName(unrecordedConnection.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMasterByIndex(unrecordedLink, newMaster.Attrs().Index); err != nil {
		t.Fatal(err)
	}
	unrecordedAttached := unrecordedConnection
	unrecordedAttachedLink := *unrecordedConnection.Link
	unrecordedAttachedLink.BridgeMaster = masterConnection.Name
	unrecordedAttached.Link = &unrecordedAttachedLink
	results, err = unrecordedState.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, masterConnection, unrecordedAttached})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkFailed {
		t.Fatalf("unrecorded membership was accepted: results=%+v err=%v", results, err)
	}
	unrecordedLink, err = netlink.LinkByName(unrecordedConnection.Name)
	if err != nil || unrecordedLink.Attrs().MasterIndex != newMaster.Attrs().Index {
		t.Fatalf("unrecorded membership changed: link=%+v err=%v", unrecordedLink, err)
	}
	if _, exists := unrecordedState.state.Memberships[unrecordedConnection.ID.String()]; exists {
		t.Fatal("unrecorded membership acquired a journal entry")
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "tagged-br"}}); err != nil {
		t.Fatal(err)
	}
	taggedOld, err := netlink.LinkByName("tagged-br")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetAlias(taggedOld, "mwan-link:tagged-br"); err != nil {
		t.Fatal(err)
	}
	tagMasterConnection := interfaceintent.Connection{
		ID: "tagged-br", Name: "tagged-br", Owner: interfaceintent.OwnerExternal,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindBridge},
	}
	tagMemberConnection := interfaceintent.Connection{
		ID: "tag-member", Name: "tag-member", Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind:         interfaceintent.KindVLAN,
			VLAN:         &interfaceintent.VLAN{Parent: parentConnection.Name, ID: 403},
			BridgeMaster: tagMasterConnection.Name,
		},
	}
	tagMembership, err := NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "tag-membership.json"))
	if err != nil {
		t.Fatal(err)
	}
	results, err = tagMembership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, tagMasterConnection, tagMemberConnection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkReady {
		t.Fatalf("attach VLAN to tagged bridge: results=%+v err=%v", results, err)
	}
	tagMember, err := netlink.LinkByName(tagMemberConnection.Name)
	if err != nil || tagMember.Attrs().MasterIndex != taggedOld.Attrs().Index {
		t.Fatalf("tagged bridge membership absent: link=%+v err=%v", tagMember, err)
	}
	if _, exists := tagMembership.state.Memberships[tagMemberConnection.ID.String()]; !exists {
		t.Fatal("tagged bridge membership has no record")
	}
	if err := netlink.LinkDel(taggedOld); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "tagged-br"}}); err != nil {
		t.Fatal(err)
	}
	taggedNew, err := netlink.LinkByName("tagged-br")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetAlias(taggedNew, "mwan-link:tagged-br"); err != nil {
		t.Fatal(err)
	}
	if taggedNew.Attrs().Index == taggedOld.Attrs().Index {
		t.Fatal("tagged replacement reused the old index")
	}
	tagMember, err = netlink.LinkByName(tagMemberConnection.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMasterByIndex(tagMember, taggedNew.Attrs().Index); err != nil {
		t.Fatal(err)
	}
	results, err = tagMembership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, tagMasterConnection, tagMemberConnection})
	if err != nil || len(results) != 1 || results[0].Status != OwnedLinkFailed {
		t.Fatalf("replacement tagged bridge was accepted as recorded master: results=%+v err=%v", results, err)
	}
	detachedTagMember := tagMemberConnection
	detachedTagLink := *tagMemberConnection.Link
	detachedTagLink.BridgeMaster = ""
	detachedTagMember.Link = &detachedTagLink
	results, err = tagMembership.Reconcile(context.Background(), log,
		[]interfaceintent.Connection{parentConnection, tagMasterConnection, detachedTagMember})
	if err == nil || len(results) != 1 || results[0].Status != OwnedLinkFailed {
		t.Fatalf("replacement tagged bridge membership was detached: results=%+v err=%v", results, err)
	}
	tagMember, err = netlink.LinkByName(tagMemberConnection.Name)
	if err != nil || tagMember.Attrs().MasterIndex != taggedNew.Attrs().Index {
		t.Fatalf("tagged replacement membership changed: link=%+v err=%v", tagMember, err)
	}
	if _, exists := tagMembership.state.Memberships[tagMemberConnection.ID.String()]; !exists {
		t.Fatal("tagged replacement membership record was forgotten")
	}
}
