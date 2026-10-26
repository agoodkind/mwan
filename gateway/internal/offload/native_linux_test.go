//go:build linux && cgo && nativeoffload && netns

package offload_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
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
	for _, path := range []string{"/sys/bus/pci/devices/ffff:ff:1f.6", "/sys/bus/pci/devices/ffff:ff:1f.7"} {
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
		Endpoints: []offload.NativeEndpoint{{
			Mode:       offload.NativeIngress,
			Controller: offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 6},
			Data:       offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 7},
			KernelVF:   0, DataVF: 1,
			KernelMAC: [6]byte{2, 0, 0, 0, 0, 1}, DataMAC: [6]byte{2, 0, 0, 0, 0, 2},
		}},
		PairCapacity: 1, MbufCount: 255, DrainTimeout: time.Second,
		RXDescriptors: 64, TXDescriptors: 64, MTU: 1500,
		FilePrefix: "mwan-native-binding-" + strconv.Itoa(os.Getpid()),
		PollBudget: 32, IdleWait: time.Millisecond,
	}
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
	flow := createTrackedFlow(t, connection, [2]string{"192.0.2.1", "198.51.100.2"}, unix.IPPROTO_TCP, 0)
	for index, mask := range [2]uint32{1, 2} {
		spec := offload.NativePairSpec{RequestedDirections: mask}
		tuple := flow.TupleOrig
		if index == 1 {
			tuple = flow.TupleReply
		}
		spec.Directions[index] = offload.NativeDirection{Original: tuple, Translated: tuple}
		_, installErr := owner.Install(t.Context(), flow, spec)
		nativeError = nil
		// OP_CREATE proves the zero unused direction did not fail Go encoding.
		if !errors.As(installErr, &nativeError) || nativeError.Operation != nativeCreateOperation || !errors.Is(installErr, unix.EINVAL) {
			t.Fatalf("mask %d did not enter the C create-state guard: %v", mask, installErr)
		}
	}
}
