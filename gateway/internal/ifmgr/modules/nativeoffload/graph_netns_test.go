//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/offload"
)

func TestNativeAdmissionRejectsLiveBridge(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, "")
	observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "native-master", HardwareAddr: fixture.input.HardwareAddr}}
	if err := netlink.LinkAdd(bridge); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(bridge); err != nil {
		t.Fatal(err)
	}
	input, err := netlink.LinkByIndex(fixture.input.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMaster(input, bridge); err != nil {
		t.Fatal(err)
	}
	current, err := netlink.LinkByIndex(fixture.input.Index)
	if err != nil {
		t.Fatal(err)
	}
	if current.Attrs().MasterIndex != bridge.Attrs().Index || current.Attrs().HardwareAddr.String() != fixture.input.HardwareAddr.String() || current.Attrs().MTU != fixture.input.MTU {
		t.Fatalf("bridge fixture changed unrelated receiver attributes: %+v", current.Attrs())
	}
	fixture.requireRejected(t, observation, EligibilityStale)
	dataPCI, err := interfaceintent.ParsePCIAddress("0000:01:00.2")
	if err != nil {
		t.Fatal(err)
	}
	fixture.module.Env.Connections[0].Link = &interfaceintent.Link{
		Kind: interfaceintent.KindPhysical,
		NativeOffload: &interfaceintent.NativeOffloadBinding{
			DataPCI: dataPCI, DataMAC: [6]byte{2, 0, 0, 0, 0, 99},
			Controller: &interfaceintent.NativeOffloadController{
				PCI: interfaceintent.PCIAddress{Bus: 1}, ReceiverVF: 1, DataVF: 2, PhysicalMiss: true,
			},
		},
	}
	if err := fixture.module.Reconcile(fixture.ctx, fixture.module.Log); err == nil || !strings.Contains(err.Error(), "live master interface") {
		t.Fatalf("public reconciliation did not reject the live bridge before opening native devices: %v", err)
	}
	if err := netlink.LinkSetNoMaster(input); err != nil {
		t.Fatal(err)
	}
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
}

func TestNativeAdmissionRestorationProfiles(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	for index := range fixture.module.Env.Connections {
		connection := &fixture.module.Env.Connections[index]
		connection.Link = &interfaceintent.Link{
			Kind: interfaceintent.KindPhysical,
			NativeOffload: &interfaceintent.NativeOffloadBinding{
				DataPCI: interfaceintent.PCIAddress{Bus: byte(index + 1), Function: 2},
				DataMAC: [6]byte{2, 0, 0, 0, 0, byte(index + 99)},
			},
		}
	}
	for _, connection := range fixture.module.Env.Connections {
		connection.Link.NativeOffload.Controller = &interfaceintent.NativeOffloadController{
			PCI: interfaceintent.PCIAddress{Bus: 4}, ReceiverVF: 1, DataVF: 2, PhysicalMiss: true,
		}
		configuration, links, err := fixture.module.resolveGraph()
		if err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(connection.Name)
		if err != nil {
			t.Fatal(err)
		}
		bound := links[link.Attrs().Index]
		field := offload.NativeIPv6RestoreDestination
		if connection.Roles&interfaceintent.RoleInternal != 0 {
			field = offload.NativeIPv6RestoreSource
		}
		if !bound.hasOrigin || configuration.Origins[bound.origin].IPv6RestoreField != field {
			t.Fatalf("configured connection %s selected the wrong restoration profile: %+v", connection.Name, configuration.Origins)
		}
		connection.Link.NativeOffload.Controller = nil
	}
	controller := &interfaceintent.NativeOffloadController{
		PCI: interfaceintent.PCIAddress{Bus: 4}, ReceiverVF: 1, DataVF: 2, PhysicalMiss: true,
	}
	fixture.module.Env.Connections[0].Link.NativeOffload.Controller = controller
	for _, roles := range []interfaceintent.Role{0, interfaceintent.RoleInternal | interfaceintent.RoleProvider} {
		fixture.module.Env.Connections[0].Roles = roles
		if err := fixture.module.Reconcile(fixture.ctx, fixture.module.Log); err == nil || !strings.Contains(err.Error(), "requires one internal or provider role") {
			t.Fatalf("public reconciliation accepted ambiguous ingress roles %d: %v", roles, err)
		}
	}
	fixture.module.Env.Connections[0].Roles = interfaceintent.RoleInternal
	fixture.module.Env.Connections[0].Link.NativeOffload.Controller = nil
	fixture.module.Env.Connections[1].Link.NativeOffload.Controller = controller
	fixture.module.cfg.WANs = nil
	if err := fixture.module.Reconcile(fixture.ctx, fixture.module.Log); err == nil || !strings.Contains(err.Error(), "requires a configured WAN") {
		t.Fatalf("public reconciliation accepted a provider outside WAN configuration: %v", err)
	}
}
