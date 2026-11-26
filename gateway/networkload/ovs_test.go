package networkload_test

import (
	"testing"

	"goodkind.io/mwan/gateway/networkload"
)

func TestLoadOVS(t *testing.T) {
	settings, err := networkload.LoadOVS("testdata/ovs.json", "../yangpub/schema")
	if err != nil {
		t.Fatal(err)
	}

	if settings.Bridge != "br-mwan" || settings.DatapathType != "doca" ||
		settings.Offload != "doca" || len(settings.Ports) != 4 || settings.Ports[0].Type != "doca" {
		t.Fatalf("unexpected OVS settings: %+v", settings)
	}

	if _, err := networkload.LoadOVS(
		"../yang/instances/network-min.json",
		"../yangpub/schema",
	); err == nil {
		t.Fatal("LoadOVS accepted a kernel network document")
	}
}
