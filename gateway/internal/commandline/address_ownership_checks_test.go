//go:build !systemd

package commandline

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/networkjson"
)

func TestCheckOwnedAddressesReportsPresentAndMissingMappings(t *testing.T) {
	t.Parallel()

	network := &networkjson.Config{
		WAN: map[string]config.IfMgrWANEntry{
			"connection": {
				Iface: "wan0",
				TranslationV4: &config.IPv4Translation{
					StaticMappings: []config.StaticMapping{
						{External: netip.MustParseAddr("203.0.113.3")},
						{External: netip.MustParseAddr("203.0.113.4")},
					},
				},
			},
		},
	}
	addresses := []netif.CurrentAddr{
		{Family: "inet", CIDR: "203.0.113.2/29"},
		{Family: "inet", CIDR: "203.0.113.3/32"},
	}
	var output strings.Builder
	deps := gatewayCheckDeps{
		out:         &output,
		loadNetwork: func() (*networkjson.Config, error) { return network, nil },
		listAddrs: func(
			context.Context,
			*slog.Logger,
			string,
		) ([]netif.CurrentAddr, error) {
			return addresses, nil
		},
	}

	if code := checkOwnedAddresses(context.Background(), deps); code != exitCheckFailed {
		t.Fatalf("exit code = %d, want %d", code, exitCheckFailed)
	}

	for _, want := range []string{
		"203.0.113.3 on wan0: present",
		"203.0.113.4 on wan0: missing",
		"owned addresses: 1 present, 1 missing",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output omits %q: %s", want, output.String())
		}
	}
}
