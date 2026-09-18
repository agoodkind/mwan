package main

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
)

func TestRunDebugWithWritersPrintsUsageForMissingAndUnknownView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "missing", args: nil},
		{name: "empty", args: []string{""}},
		{name: "unknown", args: []string{"not-a-view"}},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var output strings.Builder
			var diagnostics strings.Builder
			code := runDebugWithWriters(
				testCase.args,
				&config.Config{},
				&output,
				&diagnostics,
				func(
					_ context.Context,
					_ io.Writer,
					_ *slog.Logger,
					_ *config.Config,
					_ string,
					_ []string,
				) error {
					t.Fatal("active probe dispatcher was called")
					return nil
				},
			)
			if code == 0 {
				t.Fatalf("runDebugWithWriters(%v) code = 0, want non-zero", testCase.args)
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", output.String())
			}
			if !strings.Contains(diagnostics.String(), "usage: mwan debug") {
				t.Fatalf("stderr = %q, want usage", diagnostics.String())
			}
		})
	}
}

func TestRunDebugWithWritersDispatchesEveryActiveProbeView(t *testing.T) {
	t.Parallel()

	views := []string{
		"connectivity",
		"ping4",
		"ping6",
		"curl4",
		"curl6",
		"lb4",
		"lb6",
		"lb4-ifaces",
		"lb6-ifaces",
	}
	for _, view := range views {
		view := view
		t.Run(view, func(t *testing.T) {
			t.Parallel()

			var gotView string
			var gotArgs []string
			var output strings.Builder
			var diagnostics strings.Builder
			code := runDebugWithWriters(
				[]string{view, "forwarded"},
				&config.Config{},
				&output,
				&diagnostics,
				func(
					_ context.Context,
					probeOutput io.Writer,
					_ *slog.Logger,
					_ *config.Config,
					dispatchedView string,
					args []string,
				) error {
					gotView = dispatchedView
					gotArgs = append([]string(nil), args...)
					if probeOutput != &output {
						t.Fatal("probe dispatcher received the wrong output writer")
					}
					return nil
				},
			)
			if code != 0 {
				t.Fatalf(
					"runDebugWithWriters(%q) code = %d, stderr = %q",
					view,
					code,
					diagnostics.String(),
				)
			}
			if gotView != view {
				t.Fatalf("dispatched view = %q, want %q", gotView, view)
			}
			wantArgs := []string{"forwarded"}
			if !reflect.DeepEqual(gotArgs, wantArgs) {
				t.Fatalf("dispatched args = %v, want %v", gotArgs, wantArgs)
			}
		})
	}
}

func TestPrintDebugUsageListsEveryView(t *testing.T) {
	t.Parallel()

	var output strings.Builder
	printDebugUsage(&output)
	usage := output.String()
	views := []string{
		"npt",
		"prefixes",
		"routes",
		"policy",
		"status",
		"stats",
		"sim4",
		"sim6",
		"connectivity",
		"ping4",
		"ping6",
		"curl4",
		"curl6",
		"lb4",
		"lb6",
		"lb4-ifaces",
		"lb6-ifaces",
	}
	for _, view := range views {
		if !strings.Contains(usage, view) {
			t.Errorf("usage %q does not list %q", usage, view)
		}
	}
}
