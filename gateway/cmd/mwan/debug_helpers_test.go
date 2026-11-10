package main

import (
	"io"
	"log/slog"
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/config"
)

func TestDebugWANsSortedByName(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{
				"webpass": {Iface: "webpass0", TableID: 200, FwMark: 2},
				"att":     {Iface: "att0", TableID: 100, FwMark: 1},
			},
		},
	}

	got := debugWANs(cfg)
	want := []debugWAN{
		{ID: "att", Name: "att", Iface: "att0", TableID: 100, FwMark: 1},
		{ID: "webpass", Name: "webpass", Iface: "webpass0", TableID: 200, FwMark: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("debugWANs mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestDebugIPv4SourceUsesSecondAddress(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	source, err := debugIPv4Source(logger, "10.250.250.0/29")
	if err != nil {
		t.Fatalf("debugIPv4Source returned error: %v", err)
	}

	if source != "10.250.250.2" {
		t.Fatalf("debugIPv4Source = %q, want %q", source, "10.250.250.2")
	}
}
