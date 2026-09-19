package main

import (
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/netif"
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
		{Name: "att", Iface: "att0", TableID: 100, FwMark: 1},
		{Name: "webpass", Iface: "webpass0", TableID: 200, FwMark: 2},
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

func TestPrintDebugRouteLookupNoRoute(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	printDebugRouteLookup(&builder, "1.1.1.1", netif.RouteLookupResult{}, false)
	got := builder.String()
	want := "1.1.1.1: no route\n"
	if got != want {
		t.Fatalf("printDebugRouteLookup(no route) = %q, want %q", got, want)
	}
}

func TestPrintDebugRouteLookupResolved(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	result := netif.RouteLookupResult{OIF: "enatt0", Gateway: "fe80::1", Source: "2001:db8::1"}
	printDebugRouteLookup(&builder, "2606:4700:4700::1111", result, true)
	got := builder.String()
	want := "2606:4700:4700::1111 via fe80::1 oif enatt0 src 2001:db8::1\n"
	if got != want {
		t.Fatalf("printDebugRouteLookup(resolved) = %q, want %q", got, want)
	}
}
