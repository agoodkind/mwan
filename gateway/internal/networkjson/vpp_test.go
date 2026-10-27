//go:build cgo

package networkjson_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkload"
)

func TestLoadVPPRouterPolicy(t *testing.T) {
	loaded, err := networkload.Load(filepath.Join("testdata", "vpp-router.json"), schemaDirForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VPP == nil || loaded.VPP.NAT64 == nil || len(loaded.VPP.Connections) != 4 || len(loaded.WAN) != 0 {
		t.Fatalf("router network policy = %+v", loaded)
	}
	if loaded.GuestType != config.GuestTypeLXC {
		t.Fatalf("VPP router guest type = %s; expected the configured LXC type", loaded.GuestType)
	}
	var configuration config.Config
	loaded.Apply(&configuration)
	if configuration.IfMgr.VPPPolicy != loaded.VPP || len(configuration.IfMgr.WAN) != 0 {
		t.Fatal("router policy did not configure VPP independently of WAN steering")
	}
	if got := loaded.VPP.NAT64.Prefix.String(); got != "3d06:bad:6464::/96" {
		t.Fatalf("NAT64 prefix = %s", got)
	}
}

func TestLoadRejectsInvalidNAT64Policy(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "vpp-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ name, old, replacement string }{
		{"missing interface", `"inside-interface": "tap64"`, `"inside-interface": "missing"`},
		{"unsupported prefix", `"3d06:bad:6464::/96"`, `"3d06:bad:6464::/95"`},
		{"IPv6 pool", `"10.230.231.254"`, `"2001:db8::1"`},
		{"duplicate connection ID", `"router-vpp-outside"`, `"router-vpp-inside"`},
	} {
		t.Run(change.name, func(t *testing.T) {
			document := strings.Replace(string(data), change.old, change.replacement, 1)
			path := filepath.Join(t.TempDir(), "network.json")
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := networkload.Load(path, schemaDirForTest(t)); err == nil {
				t.Fatal("Load accepted invalid NAT64 policy")
			}
		})
	}
}
