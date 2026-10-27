package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkload"
)

func nativeNetworkDocument(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../internal/networkjson/testdata/plan-base.json")
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
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeOffloadNetworkRoundTrip(t *testing.T) {
	t.Parallel()
	document := nativeNetworkDocument(t)
	path := writeNativeNetworkDocument(t, document)
	schema := networkSchemaDirForTest(t)
	inFile := flattenNetworkJSON(t, []byte(document))
	served := withoutServedOnlyPairs(itemPairs(servedConfigItems(t, path, schema)), inFile)
	compareLeafSets(t, inFile, served)
	loaded, err := networkload.Load(path, schema)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	loaded.Apply(&cfg)
	for _, connection := range loaded.Connections {
		if connection.Link != nil && connection.Link.NativeOffload != nil && connection.Link.NativeOffload.Controller != nil {
			connection.Link.NativeOffload.Controller.DataVF = 1
		}
	}
	if err := interfaceintent.ValidateNativeBindings(cfg.IfMgr.Connections); err != nil {
		t.Fatalf("Apply shared mutable native bindings: %v", err)
	}
}

func TestNativeOffloadNetworkRejectsInvalidBindings(t *testing.T) {
	t.Parallel()
	document := nativeNetworkDocument(t)
	schema := networkSchemaDirForTest(t)
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
	if _, err := networkload.Load(path, networkSchemaDirForTest(t)); err == nil {
		t.Fatal("invalid native binding silently removed a provider from the loaded policy")
	}
}

func TestNativeOffloadBindingValidationPrecedesProviderFiltering(t *testing.T) {
	t.Parallel()
	schema := networkSchemaDirForTest(t)
	const weight = ",\n          \"weight\": 1"
	raw, err := os.ReadFile("../../internal/networkjson/testdata/plan-base.json")
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
