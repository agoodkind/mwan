package networkload_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/wanconfig"
)

func nativeNetworkDocument(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../networkjson/testdata/plan-base.json")
	if err != nil {
		t.Fatal(err)
	}
	document := strings.Replace(string(raw), `"match": { "driver": "igc" }`, `"match": { "hardware-address": "02:57:50:04:00:00" },
          "native-offload": {"data-pci":"0000:1a:00.2", "data-mac":"02:57:50:04:00:04",
            "controller":{"pci":"0000:1a:00.0", "receiver-vf":1, "data-vf":2, "physical-miss":true}}`, 1)
	return strings.Replace(document, `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`, `{
      "name":"enmwanbr0", "type":"iana-if-type:other",
      "goodkind-mwan-steering:link":{"native-offload":{"data-pci":"0000:1a:03.1", "data-mac":"02:57:50:04:03:01"}}
    }`, 1)
}

func writeNativeNetworkDocument(t *testing.T, document string) string {
	t.Helper()
	return writeDocument(t, []byte(document))
}

func TestNativeOffloadNetworkLoadAndApply(t *testing.T) {
	t.Parallel()
	document := nativeNetworkDocument(t)
	path := writeNativeNetworkDocument(t, document)
	schema := writeSchemaDir(t)
	loaded, err := networkload.Load(path, schema)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	loaded.Apply(&cfg)
	configuredBindings := 0
	for _, connection := range cfg.IfMgr.Connections {
		if connection.Link != nil && connection.Link.NativeOffload != nil {
			configuredBindings++
		}
	}
	if configuredBindings != 2 {
		t.Fatalf("Apply omitted native bindings: %d", configuredBindings)
	}
	mutatedControllers := 0
	for _, connection := range loaded.Connections {
		if connection.Link != nil && connection.Link.NativeOffload != nil && connection.Link.NativeOffload.Controller != nil {
			connection.Link.NativeOffload.Controller.DataVF = 1
			mutatedControllers++
		}
	}
	if mutatedControllers != 1 {
		t.Fatalf("the loaded configuration did not expose its native controller: %d", mutatedControllers)
	}
	if err := interfaceintent.ValidateNativeBindings(cfg.IfMgr.Connections); err != nil {
		t.Fatalf("Apply shared mutable native bindings: %v", err)
	}
}

func TestNativeOffloadPublishedConfiguration(t *testing.T) {
	t.Parallel()
	path := writeNativeNetworkDocument(t, nativeNetworkDocument(t))
	loaded, err := networkload.Load(path, writeSchemaDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	loaded.Apply(&cfg)
	var gateway wanconfig.Gateway
	gateway.GuestType = cfg.IfMgr.GuestType
	gateway.InternalIface = loaded.InternalIface
	gateway.ConnectionIDs = cfg.IfMgr.ExplicitConnectionIDs
	gateway.Connections = cfg.IfMgr.Connections
	items, err := wanconfig.ConfigItems(gateway)
	if err != nil {
		t.Fatal(err)
	}
	const wanPath = "/ietf-interfaces:interfaces/interface[name='enwebpass0']/goodkind-mwan-steering:link/native-offload"
	const lanPath = "/ietf-interfaces:interfaces/interface[name='enmwanbr0']/goodkind-mwan-steering:link/native-offload"
	want := map[string]string{
		wanPath + "/data-pci":                 "0000:1a:00.2",
		wanPath + "/data-mac":                 "02:57:50:04:00:04",
		wanPath + "/controller/pci":           "0000:1a:00.0",
		wanPath + "/controller/receiver-vf":   "1",
		wanPath + "/controller/data-vf":       "2",
		wanPath + "/controller/physical-miss": "true",
		lanPath + "/data-pci":                 "0000:1a:03.1",
		lanPath + "/data-mac":                 "02:57:50:04:03:01",
	}
	seen := make(map[string]string)
	for _, item := range items {
		if !strings.Contains(item.Path, "/native-offload/") {
			continue
		}
		if _, duplicate := seen[item.Path]; duplicate {
			t.Fatalf("published native configuration contains a duplicate leaf: %s", item.Path)
		}
		seen[item.Path] = item.Value
	}
	if len(seen) != len(want) {
		t.Fatalf("published native configuration omitted or added leaves: %+v", seen)
	}
	for path, value := range want {
		if seen[path] != value {
			t.Fatalf("published native leaf %s = %q, want %q", path, seen[path], value)
		}
	}
}

func TestNativeOffloadNetworkRejectsInvalidBindings(t *testing.T) {
	t.Parallel()
	document := nativeNetworkDocument(t)
	schema := writeSchemaDir(t)
	for _, check := range []struct{ name, before, after string }{
		{"PCI-device", "0000:1a:00.2", "0000:1a:20.2"},
		{"PCI-function", "0000:1a:00.2", "0000:1a:00.8"},
		{"PCI-format", "0000:1a:00.2", "1a:00.2"},
		{"PCI-case", "0000:1a:00.2", "0000:1A:00.2"},
		{"MAC-zero", "02:57:50:04:00:04", "00:00:00:00:00:00"},
		{"MAC-multicast", "02:57:50:04:00:04", "01:00:5e:00:00:04"},
		{"MAC-receiver", "02:57:50:04:00:04", "02:57:50:04:00:00"},
		{"VF-overlap", `"data-vf":2`, `"data-vf":1`},
		{"missing-VF", `"data-vf":2,`, ""},
		{"missing-miss", `, "physical-miss":true`, ""},
		{"false-miss", `"physical-miss":true`, `"physical-miss":false`},
		{"DATA-duplicate", "0000:1a:03.1", "0000:1a:00.2"},
		{"DATA-MAC-duplicate", `"data-pci":"0000:1a:03.1", "data-mac":"02:57:50:04:03:01"`, `"data-pci":"0000:1a:00.4", "data-mac":"02:57:50:04:00:04", "controller":{"pci":"0000:1a:00.0", "receiver-vf":3, "data-vf":4, "physical-miss":false}`},
		{"PCI-role-overlap", "0000:1a:03.1", "0000:1a:00.0"},
		{"VLAN", `"native-offload": {`, `"vlan":{"parent":"enmwanbr0","id":2}, "native-offload": {`},
	} {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()
			changed := strings.Replace(document, check.before, check.after, 1)
			if changed == document {
				t.Fatal("test input did not change")
			}
			path := writeNativeNetworkDocument(t, changed)
			if _, err := networkload.Load(path, schema); err == nil {
				t.Fatal("network loader accepted invalid native bindings")
			}
		})
	}
}

func TestNativeOffloadInvalidProviderDoesNotDisappear(t *testing.T) {
	t.Parallel()
	document := nativeNetworkDocument(t)
	document = strings.Replace(document, `"native-offload":{"data-pci":"0000:1a:03.1", "data-mac":"02:57:50:04:03:01"}`, "", 1)
	document = strings.Replace(document, "02:57:50:04:00:04", "01:00:5e:00:00:04", 1)
	path := writeNativeNetworkDocument(t, document)
	if _, err := networkload.Load(path, writeSchemaDir(t)); err == nil {
		t.Fatal("invalid native binding silently removed a provider from the loaded policy")
	}
}

func TestNativeOffloadBindingValidationPrecedesProviderFiltering(t *testing.T) {
	t.Parallel()
	schema := writeSchemaDir(t)
	const weight = ",\n          \"weight\": 1"
	raw, err := os.ReadFile("../networkjson/testdata/plan-base.json")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := strings.Replace(string(raw), weight, "", 1)
	if ordinary == string(raw) {
		t.Fatal("test did not remove the provider weight")
	}
	loaded, err := networkload.Load(writeNativeNetworkDocument(t, ordinary), schema)
	if err != nil {
		t.Fatalf("ordinary provider rejection failed: %v", err)
	}
	if len(loaded.Rejected) != 1 || loaded.Rejected[0].Interface != "enwebpass0" || len(loaded.WAN) != 2 {
		t.Fatalf("ordinary provider rejection changed: rejected=%+v providers=%+v", loaded.Rejected, loaded.WAN)
	}
	document := strings.Replace(nativeNetworkDocument(t), weight, "", 1)
	document = strings.Replace(document, `"native-offload":{"data-pci":"0000:1a:03.1", "data-mac":"02:57:50:04:03:01"}`, "", 1)
	document = strings.Replace(document, `"data-vf":2`, `"data-vf":1`, 1)
	loaded, err = networkload.Load(writeNativeNetworkDocument(t, document), schema)
	var semantic *networkload.SemanticError
	if loaded != nil || !errors.As(err, &semantic) || !strings.Contains(err.Error(), "native receiver and DATA VF overlap") {
		t.Fatalf("native binding did not fail after YANG validation and before provider filtering: loaded=%v error=%v", loaded != nil, err)
	}
}
