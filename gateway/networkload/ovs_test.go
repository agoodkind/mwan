package networkload_test

import (
	"testing"

	"goodkind.io/mwan/gateway/networkload"
)

func TestLoadOVS(t *testing.T) {
	loaded, err := networkload.Load("testdata/ovs.json", "../yangpub/schema")
	if err != nil {
		t.Fatal(err)
	}

	settings := loaded.OVS
	if settings == nil || settings.Bridge != "br-mwan" || settings.DatapathType != "doca" ||
		settings.Offload != "doca" || len(settings.Ports) != 4 || settings.Ports[0].Type != "doca" {
		t.Fatalf("unexpected OVS settings: %+v", settings)
	}
}
