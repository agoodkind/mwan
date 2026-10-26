//go:build linux && cgo && nativeoffload && netns

package offload_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ti-mo/conntrack"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/offload"
)

const (
	nativeTestChild       = "MWAN_NATIVE_BINDING_TEST_CHILD"
	nativeConfigOperation = 1
	nativePortOperation   = 3
	nativeCreateOperation = 6
	nativeCPUSetSize      = 1024
)

func TestNativeSingleDirection(t *testing.T) {
	if os.Getenv(nativeTestChild) != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), binary, "-test.run=^TestNativeSingleDirection$", "-test.v")
		command.Env = append(os.Environ(), nativeTestChild+"=1")
		command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated native binding test: %v: %s", err, output)
		}
		t.Log(string(output))
		return
	}
	// Unavailable functions permit real EAL initialization without device access.
	for _, path := range []string{"/sys/bus/pci/devices/ffff:ff:1f.4", "/sys/bus/pci/devices/ffff:ff:1f.5", "/sys/bus/pci/devices/ffff:ff:1f.6", "/sys/bus/pci/devices/ffff:ff:1f.7"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("test requires an unavailable PCI function at %s: %v", path, err)
		}
	}
	var affinity unix.CPUSet
	if err := unix.SchedGetaffinity(0, &affinity); err != nil {
		t.Fatal(err)
	}
	cpu := -1
	for candidate := range nativeCPUSetSize {
		if affinity.IsSet(candidate) {
			cpu = candidate
			break
		}
	}
	if cpu < 0 {
		t.Fatal("test requires one allowed CPU")
	}
	config := offload.NativeConfig{
		CPU: uint32(cpu), MemoryMiB: 64, MemoryMode: offload.NativeAnonymousMemory,
		Controllers: []offload.NativeController{{
			PCI:              offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 6},
			PhysicalReceiver: 0,
		}},
		Receivers: []offload.NativeKernelReceiver{
			{Controller: 0, VF: 0, MAC: [6]byte{2, 0, 0, 0, 0, 1}},
			{Controller: 0, VF: 1, MAC: [6]byte{2, 0, 0, 0, 0, 2}},
		},
		Data: []offload.NativeDataDevice{
			{
				PCI:        offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 5},
				Controller: 0, VF: 2, MAC: [6]byte{2, 0, 0, 0, 0, 3},
			},
			{
				PCI:        offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 7},
				Controller: 0, VF: 3, MAC: [6]byte{2, 0, 0, 0, 0, 4},
			},
		},
		Origins: []offload.NativeOrigin{
			{Controller: 0, Kind: offload.NativePhysicalOrigin, Receiver: 0, Data: 0, ReturnMode: offload.NativeReturnOriginal},
			{Controller: 0, Kind: offload.NativePhysicalOrigin, Receiver: 1, Data: 1, ReturnMode: offload.NativeReturnOriginal},
		},
		PairCapacity: 1, MbufCount: 255, DrainTimeout: time.Second,
		ConntrackUpdateTimeout: conntrackTestTimeout,
		RXDescriptors:          64, TXDescriptors: 64, MTU: 1500,
		FilePrefix: "mwan-native-binding-" + strconv.Itoa(os.Getpid()),
		PollBudget: 32, IdleWait: time.Millisecond,
	}
	for _, timeout := range []time.Duration{0, -time.Second} {
		invalid := config
		invalid.ConntrackUpdateTimeout = timeout
		if rejected, rejectErr := offload.NewNativeRuntime(invalid, slog.Default()); rejectErr == nil || rejected != nil {
			t.Fatalf("nonpositive conntrack timeout initialized native runtime: %v", rejectErr)
		}
	}
	checkNativeGraphRejections(t, config)
	owner, err := offload.NewNativeRuntime(config, slog.Default())
	if owner != nil {
		t.Cleanup(func() {
			if closeErr := owner.Close(context.Background()); closeErr != nil {
				t.Errorf("final native cleanup failed: %v", closeErr)
			}
		})
	}
	var nativeError *offload.NativeError
	if owner == nil || !errors.As(err, &nativeError) || nativeError.Operation != nativePortOperation {
		t.Fatalf("expected retained EAL owner after unavailable-device discovery: owner=%v error=%v", owner != nil, err)
	}
	connection, err := conntrack.Dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := connection.Close(); closeErr != nil {
			t.Errorf("close conntrack socket: %v", closeErr)
		}
	})
	for _, addresses := range [][2]string{{"192.0.2.1", "198.51.100.2"}, {"2001:db8:1::1", "2001:db8:2::2"}} {
		checkNativeTCPAdmission(t, owner, connection, addresses)
	}
	if err := owner.Close(t.Context()); err != nil {
		t.Fatalf("final native cleanup failed: %v", err)
	}
	if err := owner.Close(t.Context()); err != nil {
		t.Fatalf("repeated native cleanup failed: %v", err)
	}
	flow := createTrackedFlow(t, connection, [2]string{"192.0.2.3", "198.51.100.4"}, unix.IPPROTO_TCP, 0)
	spec := offload.NativePairSpec{RequestedDirections: 1}
	spec.Directions[0] = offload.NativeDirection{Original: flow.TupleOrig, Translated: flow.TupleOrig}
	if _, err := owner.Install(t.Context(), flow, spec); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed native runtime accepted admission: %v", err)
	}
	requireSameTrackedFlow(t, flow, readTrackedFlow(t, connection, flow), false)
}

func checkNativeTCPAdmission(t *testing.T, owner *offload.NativeRuntime, connection *conntrack.Conn, addresses [2]string) {
	t.Helper()
	flow := createTrackedFlow(t, connection, addresses, unix.IPPROTO_TCP, 0)
	stale := flow
	stale.ID++
	if stale.ID == 0 {
		stale.ID++
	}
	var spec offload.NativePairSpec
	spec.RequestedDirections = 1
	spec.Directions[0] = offload.NativeDirection{Origin: 0, EgressData: 0, Original: stale.TupleOrig, Translated: stale.TupleOrig}
	_, staleErr := owner.Install(t.Context(), stale, spec)
	var nativeError *offload.NativeError
	if (!errors.Is(staleErr, unix.ENOENT) && !errors.Is(staleErr, unix.ESTALE)) || errors.As(staleErr, &nativeError) {
		t.Fatalf("stale conntrack identity did not reject before C admission: %v", staleErr)
	}
	requireSameTrackedFlow(t, flow, readTrackedFlow(t, connection, flow), false)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owner.Install(canceled, flow, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled native admission: %v", err)
	}
	requireSameTrackedFlow(t, flow, readTrackedFlow(t, connection, flow), false)
	udp := createTrackedFlow(t, connection, addresses, unix.IPPROTO_UDP, 0)
	if _, err := owner.Install(t.Context(), udp, spec); err == nil {
		t.Fatal("TCP direction accepted a UDP conntrack identity")
	}
	requireSameTrackedFlow(t, udp, readTrackedFlow(t, connection, udp), false)
	// Stale caller metadata must not overwrite the current kernel entry.
	input := flow
	input.Timeout, input.Mark, input.Status = 1, 999, 0
	input.ProtoInfo.TCP = &conntrack.ProtoInfoTCP{State: 1}
	for index, mask := range [2]uint32{1, 2} {
		spec = offload.NativePairSpec{RequestedDirections: mask}
		tuple := flow.TupleOrig
		if index == 1 {
			tuple = flow.TupleReply
		}
		spec.Directions[index] = offload.NativeDirection{Origin: offload.NativeOriginID(index), EgressData: 0, Original: tuple, Translated: tuple}
		_, installErr := owner.Install(t.Context(), input, spec)
		nativeError = nil
		// OP_CREATE proves the zero unused direction did not fail Go encoding.
		if !errors.As(installErr, &nativeError) || nativeError.Operation != nativeCreateOperation || !errors.Is(installErr, unix.EINVAL) {
			t.Fatalf("mask %d did not enter the C create-state guard: %v", mask, installErr)
		}
		requireSameTrackedFlow(t, flow, readTrackedFlow(t, connection, flow), true)
	}
	spec.RequestedDirections = 1
	spec.Directions[0] = offload.NativeDirection{Original: udp.TupleOrig, Translated: udp.TupleOrig}
	_, udpErr := owner.Install(t.Context(), udp, spec)
	if !errors.As(udpErr, &nativeError) || nativeError.Operation != nativeCreateOperation {
		t.Fatalf("UDP admission attempted a TCP flag update: %v", udpErr)
	}
	requireSameTrackedFlow(t, udp, readTrackedFlow(t, connection, udp), false)
}

func checkNativeGraphRejections(t *testing.T, config offload.NativeConfig) {
	t.Helper()
	cases := []struct {
		name   string
		change func(*offload.NativeConfig)
		kind   offload.NativeObjectKind
		id     uint32
		code   syscall.Errno
	}{
		{"missing-physical-receiver", func(c *offload.NativeConfig) {
			c.Controllers[0].PhysicalReceiver = 8
		}, offload.NativeObjectController, 0, unix.EINVAL},
		{"duplicate-controller", func(c *offload.NativeConfig) {
			c.Controllers = append(c.Controllers, offload.NativeController{PCI: c.Controllers[0].PCI, PhysicalReceiver: 2})
			c.Receivers = append(c.Receivers, offload.NativeKernelReceiver{Controller: 1, VF: 0, MAC: [6]byte{2, 0, 0, 0, 0, 5}})
		}, offload.NativeObjectController, 1, unix.EINVAL},
		{"duplicate-receiver-vf", func(c *offload.NativeConfig) {
			c.Receivers[1].VF = c.Receivers[0].VF
		}, offload.NativeObjectReceiver, 1, unix.EINVAL},
		{"duplicate-receiver-mac", func(c *offload.NativeConfig) {
			c.Receivers[1].MAC = c.Receivers[0].MAC
		}, offload.NativeObjectReceiver, 1, unix.EINVAL},
		{"receiver-controller-reference", func(c *offload.NativeConfig) {
			c.Receivers[1].Controller = 4
		}, offload.NativeObjectReceiver, 1, unix.EINVAL},
		{"duplicate-data-pci", func(c *offload.NativeConfig) {
			c.Data[1].PCI = c.Data[0].PCI
		}, offload.NativeObjectData, 1, unix.EINVAL},
		{"data-controller-pci-overlap", func(c *offload.NativeConfig) {
			c.Data[0].PCI = c.Controllers[0].PCI
		}, offload.NativeObjectData, 0, unix.EINVAL},
		{"data-receiver-role-overlap", func(c *offload.NativeConfig) {
			c.Data[0].VF = c.Receivers[0].VF
		}, offload.NativeObjectData, 0, unix.EINVAL},
		{"origin-receiver-reference", func(c *offload.NativeConfig) {
			c.Origins[0].Receiver = 8
		}, offload.NativeObjectOrigin, 0, unix.EINVAL},
		{"origin-data-reference", func(c *offload.NativeConfig) {
			c.Origins[0].Data = 8
		}, offload.NativeObjectOrigin, 0, unix.EINVAL},
		{"origin-controller-reference", func(c *offload.NativeConfig) {
			c.Origins[0].Controller = 4
		}, offload.NativeObjectOrigin, 0, unix.EINVAL},
		{"receiver-on-another-controller", func(c *offload.NativeConfig) {
			c.Controllers = append(c.Controllers, offload.NativeController{PCI: offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 4}, PhysicalReceiver: 2})
			c.Receivers = append(c.Receivers, offload.NativeKernelReceiver{Controller: 1, VF: 0, MAC: [6]byte{2, 0, 0, 0, 0, 5}})
			c.Origins[0].Receiver = 2
		}, offload.NativeObjectOrigin, 0, unix.EINVAL},
		{"data-on-another-controller", func(c *offload.NativeConfig) {
			c.Controllers = append(c.Controllers, offload.NativeController{PCI: offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 4}, PhysicalReceiver: 2})
			c.Receivers = append(c.Receivers, offload.NativeKernelReceiver{Controller: 1, VF: 0, MAC: [6]byte{2, 0, 0, 0, 0, 5}})
			c.Data[0].Controller = 1
		}, offload.NativeObjectOrigin, 0, unix.EINVAL},
		{"output-only-data-as-input", func(c *offload.NativeConfig) {
			c.Data[0].Controller, c.Data[0].VF = offload.NativeNoController, 0
		}, offload.NativeObjectOrigin, 0, unix.EINVAL},
		{"shared-data-rx", func(c *offload.NativeConfig) {
			c.Origins[1].Data = c.Origins[0].Data
		}, offload.NativeObjectOrigin, 1, unix.EINVAL},
		{"source-vf-origin", func(c *offload.NativeConfig) {
			c.Origins[0].Kind, c.Origins[0].SourceVF = offload.NativeVFOrigin, 4
		}, offload.NativeObjectOrigin, 0, unix.ENOTSUP},
		{"peer-unicast-return", func(c *offload.NativeConfig) {
			c.Origins[0].ReturnMode = offload.NativeReturnPeerUnicast
		}, offload.NativeObjectOrigin, 0, unix.ENOTSUP},
	}
	for _, check := range cases {
		t.Run(check.name, func(t *testing.T) {
			changed := config
			changed.Controllers = slices.Clone(config.Controllers)
			changed.Receivers = slices.Clone(config.Receivers)
			changed.Data = slices.Clone(config.Data)
			changed.Origins = slices.Clone(config.Origins)
			check.change(&changed)
			owner, err := offload.NewNativeRuntime(changed, slog.Default())
			if owner != nil {
				if closeErr := owner.Close(context.Background()); closeErr != nil {
					t.Errorf("cleanup after unexpected EAL initialization: %v", closeErr)
				}
				t.Fatal("invalid graph initialized EAL")
			}
			var nativeError *offload.NativeError
			if !errors.As(err, &nativeError) || nativeError.Operation != nativeConfigOperation ||
				nativeError.ObjectKind != check.kind || nativeError.ObjectID != check.id || !errors.Is(err, check.code) {
				t.Fatalf("unexpected graph rejection: %v", err)
			}
		})
		if t.Failed() {
			t.Fatal("graph validation failed before the EAL acceptance case")
		}
	}
}
