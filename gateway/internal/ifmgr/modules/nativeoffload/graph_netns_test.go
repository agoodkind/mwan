//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
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
