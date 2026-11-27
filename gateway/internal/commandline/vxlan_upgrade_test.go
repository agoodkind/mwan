package commandline

// These tests seed independent running and startup configuration in temporary
// sysrepo repositories and run the schema upgrade. A legacy vni of 17 in
// running and 27 in startup must keep both values as named tunnel options.
// Retry cases use a saved journal and check that current edits survive.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/mwan/gateway/internal/yangpub"
	"goodkind.io/mwan/gateway/yangpub/schema"
)

const (
	upgradeInterface = "/ietf-interfaces:interfaces/interface[name='vxlan0']"
	upgradeTunnel    = upgradeInterface + "/goodkind-mwan-steering:link/tunnel"
)

func upgradeVXLANValues(ds yangpub.Datastore) map[string]string {
	vni := "17"
	if ds == yangpub.DatastoreStartup {
		vni = "27"
	}

	return map[string]string{
		"vni":              vni,
		"source-port-min":  "49152",
		"source-port-max":  "49153",
		"destination-port": "4789",
		"tos":              "2",
	}
}

func seedLegacyVXLANDatastores(t *testing.T, datastore yangpub.Publisher, dir string) {
	t.Helper()
	models, err := yangpub.WriteSchema(dir)
	if err != nil {
		t.Fatal(err)
	}

	for index := range models {
		if filepath.Base(models[index].Path) == schema.SteeringFile {
			models[index].Path = filepath.Join("testdata", "legacy-vxlan", schema.SteeringFile)
		}
	}

	if _, err := datastore.InstallModules(t.Context(), models, dir); err != nil {
		t.Fatal(err)
	}

	for _, ds := range []yangpub.Datastore{yangpub.DatastoreRunning, yangpub.DatastoreStartup} {
		items := []yangpub.Item{
			{
				Path:  "/ietf-interfaces:interfaces/interface[name='wan0']/type",
				Value: "iana-if-type:other",
			},
			{Path: upgradeInterface + "/type", Value: "iana-if-type:tunnel"},
			{Path: upgradeInterface + "/description", Value: string(ds)},
			{Path: upgradeTunnel + "/protocol", Value: "vxlan"},
			{Path: upgradeTunnel + "/underlay", Value: "wan0"},
			{Path: upgradeTunnel + "/remote-address", Value: "192.0.2.7"},
		}
		for name, value := range upgradeVXLANValues(ds) {
			items = append(
				items,
				yangpub.Item{Path: upgradeTunnel + "/vxlan/" + name, Value: value},
			)
		}

		if err := datastore.SetItems(t.Context(), ds, items); err != nil {
			t.Fatalf("seed %s: %v", ds, err)
		}
	}
}

func TestInstallPreservesVXLANDatastores(t *testing.T) {
	t.Parallel()
	const child = "MWAN_TEST_TUNNEL_UPGRADE"
	if os.Getenv(child) == "" {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "etc", "sysrepo"), 0o750); err != nil {
			t.Fatal(err)
		}

		env := append(sysrepoChildEnv(t, root, "", "upgrade"), child+"=1")
		runChild(t, env, "-test.run=^"+t.Name()+"$")

		return
	}

	datastore := upgradePublisher(t)
	dir := t.TempDir()
	seedLegacyVXLANDatastores(t, datastore, dir)
	models, err := yangpub.WriteSchema(dir)
	if err != nil {
		t.Fatal(err)
	}

	install := func() {
		t.Helper()
		batches := []moduleBatch{{models: models, searchDir: dir}}
		if err := applyDatastore(
			t.Context(),
			slog.Default(),
			batches,
			nil,
			&schemaOutcome{},
		); err != nil {
			t.Fatal(err)
		}
	}
	install()

	check := func(ds yangpub.Datastore, path, want string) {
		t.Helper()
		got, found, err := datastore.GetItem(t.Context(), ds, path)
		if err != nil || !found || got != want {
			t.Errorf("%s %s = %q, found %v, error %v; want %q", ds, path, got, found, err, want)
		}
	}
	option := func(name string) string { return upgradeTunnel + "/options[name='" + name + "']" }

	for _, ds := range []yangpub.Datastore{yangpub.DatastoreRunning, yangpub.DatastoreStartup} {
		check(ds, upgradeInterface+"/description", string(ds))

		for name, want := range upgradeVXLANValues(ds) {
			check(ds, option(name)+"/value", want)
		}
	}

	// An operator can edit running configuration after the schema update
	// and before a retry restores the remaining options.
	const running = yangpub.DatastoreRunning
	if err := datastore.DeleteItem(t.Context(), running, option("tos")); err != nil {
		t.Fatal(err)
	}

	pending := []yangpub.Item{
		{Path: option("vni") + "/value", Value: "9"},
		{Path: option("tos") + "/value", Value: "7"},
	}
	journal, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(yangpub.RepositoryPath(), "mwan-vxlan-options-running.json")
	if err := os.WriteFile(path, journal, 0o600); err != nil {
		t.Fatal(err)
	}

	install()
	check(running, option("vni")+"/value", "17")
	check(running, option("tos")+"/value", "7")
	check(yangpub.DatastoreStartup, option("tos")+"/value", "2")

	if err := datastore.DeleteItem(t.Context(), running, upgradeInterface); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, journal, 0o600); err != nil {
		t.Fatal(err)
	}

	install()
	if _, exists, err := datastore.GetItem(
		t.Context(),
		running,
		upgradeInterface+"/name",
	); err != nil ||
		exists {
		t.Fatalf("deleted interface exists after retry: %v, error %v", exists, err)
	}
}

func upgradePublisher(t *testing.T) yangpub.Publisher {
	t.Helper()
	datastore, err := yangpub.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = datastore.Close() })

	return datastore
}

func TestRootedInstallUpgradesVXLANTunnel(t *testing.T) {
	t.Parallel()
	const child = "MWAN_TEST_ROOTED_TUNNEL_UPGRADE"
	switch os.Getenv(child) {
	case "seed":
		seedLegacyVXLANDatastores(t, upgradePublisher(t), t.TempDir())
	case "read":
		datastore := upgradePublisher(t)

		for name, want := range upgradeVXLANValues(yangpub.DatastoreStartup) {
			path := upgradeTunnel + "/options[name='" + name + "']/value"
			got, found, err := datastore.GetItem(t.Context(), yangpub.DatastoreStartup, path)
			if err != nil || !found || got != want {
				t.Errorf(
					"startup %s = %q, found %v, error %v; want %q",
					name,
					got,
					found,
					err,
					want,
				)
			}
		}
	default:
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "etc", "sysrepo"), 0o750); err != nil {
			t.Fatal(err)
		}

		childEnv := func(stage string) []string {
			return append(sysrepoChildEnv(t, root, "", stage), child+"="+stage)
		}
		runChild(t, childEnv("seed"), "-test.run=^"+t.Name()+"$")
		runInstallChild(t, root)
		runChild(t, childEnv("read"), "-test.run=^"+t.Name()+"$")
	}
}
