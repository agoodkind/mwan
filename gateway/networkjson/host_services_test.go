//go:build cgo

package networkjson_test

// In OVS mode Linux still provides host networking services. This case loads an
// OVS network document and checks the WAN module settings for configured
// interfaces, health probes, and the absence of Linux translation settings.

import (
	"testing"

	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr/moduleconfig"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/addresses"
	"goodkind.io/mwan/gateway/networkload"
)

func TestOVSLoadsHostServicesWithoutKernelTranslation(t *testing.T) {
	path := writeDocument(t, ovsHostDocument(validDocument))
	loaded, err := networkload.Load(path, schemaDirForTest(t))
	if err != nil {
		t.Fatal(err)
	}

	var settings config.Config
	loaded.Apply(&settings)
	modules, err := moduleconfig.Build(settings.IfMgr, "wan")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"npt", "wan.routes", "steering"} {
		if _, selected := modules[name]; selected {
			t.Fatalf("OVS mode selected kernel module %s", name)
		}
	}

	local, ok := modules["addresses"].(addresses.Config)
	if !ok || len(local.Connections) != len(loaded.Connections) {
		t.Fatal("host address module did not receive configured interfaces")
	}

	for id, provider := range local.Providers {
		if provider.IPv4 != nil || provider.IPv6 != nil {
			t.Fatalf("host address module received translation settings for %s", id)
		}
	}

	if !loaded.Health["webpass"].Enabled {
		t.Fatal("OVS mode disabled probing for webpass")
	}
}
