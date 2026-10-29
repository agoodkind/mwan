//go:build linux && cgo && nativeoffload && netns

package offload_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"goodkind.io/mwan/internal/offload"
)

func checkNativeVXLANRejectedLifecycle(t *testing.T, owner *offload.NativeRuntime) {
	t.Helper()
	endpoint := offload.NativeVXLANEndpoint{
		Receiver: 0, SourceVF: 0, LinuxIfIndex: 1,
		PCI: offload.NativePCIAddress{Domain: 65535, Bus: 255, Device: 31, Function: 4},
	}
	result, err := owner.OpenVXLANContext(t.Context(), endpoint)
	var detail *offload.NativeError
	if !errors.Is(err, syscall.EBUSY) || !errors.As(err, &detail) || detail.ObjectKind != offload.NativeObjectReceiver || result.Context != (offload.NativeVXLANContext{}) {
		t.Fatalf("faulted owner did not reject VXLAN context on the owner thread: result=%+v error=%v", result, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owner.OpenVXLANContext(canceled, endpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled VXLAN open: %v", err)
	}
	evidence := nativeVXLANForwardingEvidence(t)
	_, err = owner.InstallVXLAN(t.Context(), result.Context, nativeVXLANPlan(), evidence)
	if !errors.Is(err, syscall.ESTALE) || !errors.As(err, &detail) {
		t.Fatalf("VXLAN install accepted an absent context: %v", err)
	}
	_, err = owner.QueryVXLAN(t.Context(), result)
	if !errors.Is(err, syscall.ESTALE) || !errors.As(err, &detail) {
		t.Fatalf("VXLAN query accepted an absent rule: %v", err)
	}
	_, err = owner.RemoveVXLAN(t.Context(), result)
	if !errors.Is(err, syscall.ESTALE) || !errors.As(err, &detail) {
		t.Fatalf("VXLAN removal accepted an absent rule: %v", err)
	}
	_, err = owner.CloseVXLANContext(t.Context(), result.Context)
	if !errors.Is(err, syscall.ESTALE) || !errors.As(err, &detail) {
		t.Fatalf("VXLAN close accepted an absent context: %v", err)
	}
}

func checkNativeVXLANClosedLifecycle(t *testing.T, owner *offload.NativeRuntime) {
	t.Helper()
	var contextHandle offload.NativeVXLANContext
	var rule offload.NativeVXLANResult
	evidence := nativeVXLANForwardingEvidence(t)
	operations := []struct {
		name string
		run  func() error
	}{
		{"open", func() error { _, err := owner.OpenVXLANContext(t.Context(), offload.NativeVXLANEndpoint{}); return err }},
		{"install", func() error {
			_, err := owner.InstallVXLAN(t.Context(), contextHandle, nativeVXLANPlan(), evidence)
			return err
		}},
		{"query", func() error { _, err := owner.QueryVXLAN(t.Context(), rule); return err }},
		{"remove", func() error { _, err := owner.RemoveVXLAN(t.Context(), rule); return err }},
		{"close", func() error { _, err := owner.CloseVXLANContext(t.Context(), contextHandle); return err }},
	}
	for _, operation := range operations {
		if err := operation.run(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("closed native owner accepted VXLAN %s: %v", operation.name, err)
		}
	}
}

func nativeVXLANForwardingEvidence(t *testing.T) *offload.NativeEvidenceGuard {
	t.Helper()
	source, err := offload.NewForwardingEvents(t.Context(), nativeEvidenceBufferBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	expected := source.Generation()
	return &offload.NativeEvidenceGuard{Check: func() error {
		if err := source.Err(); err != nil {
			return err
		}
		if source.Generation() != expected {
			return fmt.Errorf("VXLAN forwarding namespace changed: %w", syscall.ESTALE)
		}
		return nil
	}}
}
