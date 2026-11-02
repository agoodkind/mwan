package connprobe_test

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/connprobe"
)

func TestInitTargetLists(t *testing.T) {
	ipv4Target := netip.MustParseAddr("192.0.2.1")
	ipv6Target := netip.MustParseAddr("2001:db8::1")
	tests := []struct {
		name      string
		targetsV4 []netip.Addr
		targetsV6 []netip.Addr
		wantError bool
	}{
		{name: "targets_v4", targetsV4: []netip.Addr{ipv4Target}},
		{name: "targets_v6", targetsV6: []netip.Addr{ipv6Target}},
		{name: "empty", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module, err := connprobe.New(connprobe.Config{
				Iface:     "eth0",
				TargetsV4: test.targetsV4,
				TargetsV6: test.targetsV6,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			err = module.Init(context.Background(), &ifmgr.Env{Log: log})
			if (err != nil) != test.wantError {
				t.Fatalf("Init() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}
