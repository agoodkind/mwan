//go:build linux && ovs

package ovs

import (
	"context"
	"os"
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestSetupWithOVS(t *testing.T) {
	socket := os.Getenv("MWAN_TEST_OVS_SOCKET")
	if socket == "" {
		t.Fatal("run make test-ovs to start a disposable OVS instance")
	}

	ctx := t.Context()
	settings := ovsconfig.Config{
		Socket:        socket,
		TimeoutMillis: 5000,
		Bridge:        "mwan-test",
		DatapathType:  "netdev",
		Offload:       "",
		OpenFlow:      nil,
		Ports: []ovsconfig.Port{
			{Name: "mwan-test-host", Type: "internal", Options: nil},
			{
				Name: "mwan-test-tun",
				Type: "geneve",
				Options: []ovsconfig.Option{
					{Name: "remote_ip", Value: "2001:db8::2"},
					{Name: "key", Value: "200"},
				},
			},
		},
	}

	doca := settings
	doca.Offload = "doca"
	if err := Setup(ctx, doca); err == nil || !strings.Contains(err.Error(), "doca-init") {
		t.Fatalf("missing DOCA setting returned %v", err)
	}

	db, err := connect(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var bridges []bridgeRow
	if err := db.List(ctx, &bridges); err != nil || len(bridges) != 0 {
		t.Fatalf("rejected configuration created bridges %v: %v", bridges, err)
	}

	root, err := readSwitch(ctx, db, settings)
	if err != nil {
		t.Fatal(err)
	}
	original := root.OtherConfig
	root.OtherConfig = map[string]string{"hw-offload-ct-unidir-udp-enabled": "true"}
	setOtherConfig := func() {
		t.Helper()
		operations, err := db.Where(&root).Update(&root, &root.OtherConfig)
		if err != nil {
			t.Fatal(err)
		}
		if err := transact(ctx, db, operations); err != nil {
			t.Fatal(err)
		}
	}
	setOtherConfig()
	if err := Setup(ctx, doca); err != errUnidirectionalUDP {
		t.Fatalf("unidirectional UDP offload returned %v", err)
	}
	root.OtherConfig = original
	setOtherConfig()

	if err := Setup(ctx, settings); err != nil {
		t.Fatal(err)
	}

	first := readTestInterface(t, ctx, settings, "mwan-test-tun")
	if first.Type != "geneve" || first.Options["remote_ip"] != "2001:db8::2" ||
		first.Options["key"] != "200" {
		t.Fatalf("unexpected Geneve interface: %+v", first)
	}

	settings.Ports[1].Options[1].Value = "201"
	settings.Ports = settings.Ports[1:]
	if err := Setup(ctx, settings); err != nil {
		t.Fatal(err)
	}

	updated := readTestInterface(t, ctx, settings, "mwan-test-tun")
	if updated.UUID != first.UUID || updated.Options["key"] != "201" {
		t.Fatalf("Geneve interface UUID changed or key did not match 201: %+v", updated)
	}

	if err := Setup(ctx, settings); err != nil {
		t.Fatal(err)
	}

	repeated := readTestInterface(t, ctx, settings, "mwan-test-tun")
	if repeated.UUID != first.UUID {
		t.Fatalf(
			"reapplying identical settings replaced interface %s with %s",
			first.UUID,
			repeated.UUID,
		)
	}
}

func readTestInterface(
	t *testing.T,
	ctx context.Context,
	config ovsconfig.Config,
	name string,
) interfaceRow {
	t.Helper()
	db, err := connect(ctx, config.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var device interfaceRow
	device.Name = name
	if err := db.Get(ctx, &device); err != nil {
		t.Fatal(err)
	}

	return device
}
