package commandline

import (
	"log/slog"
	"reflect"
	"testing"

	"goodkind.io/mwan/gateway/internal/config"
)

const (
	webpassName     = "webpass"
	attName         = "att"
	webpassTableID  = 200
	attTableID      = 100
	webpassFwMark   = 2
	attFwMark       = 1
	webpassIface    = "webpass0"
	attIface        = "att0"
	debugSourceCIDR = "10.250.250.0/29"
	debugSourceIP   = "10.250.250.2"
)

func TestDebugWANsSortedByName(t *testing.T) {
	t.Parallel()

	cfg := new(config.Config)
	cfg.IfMgr.WAN = map[string]config.IfMgrWANEntry{
		webpassName: {Iface: webpassIface, TableID: webpassTableID, FwMark: webpassFwMark},
		attName:     {Iface: attIface, TableID: attTableID, FwMark: attFwMark},
	}

	got := debugWANs(cfg)
	want := []debugWAN{
		{ID: attName, Name: attName, Iface: attIface, TableID: attTableID, FwMark: attFwMark},
		{
			ID:      webpassName,
			Name:    webpassName,
			Iface:   webpassIface,
			TableID: webpassTableID,
			FwMark:  webpassFwMark,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("debugWANs mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestDebugIPv4SourceUsesSecondAddress(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	source, err := debugIPv4Source(logger, debugSourceCIDR)
	if err != nil {
		t.Fatalf("debugIPv4Source returned error: %v", err)
	}

	if source != debugSourceIP {
		t.Fatalf("debugIPv4Source = %q, want %q", source, debugSourceIP)
	}
}
