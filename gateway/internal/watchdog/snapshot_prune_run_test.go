package watchdog_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/watchdog"
)

const (
	pruneChildEnv       = "MWAN_WATCHDOG_PRUNE_TEST_CHILD"
	pruneDirectoryEnv   = "MWAN_WATCHDOG_PRUNE_TEST_DIRECTORY"
	pruneVMID           = "4200"
	prunePreDeployCount = 15
	pruneListingFile    = "listing"
	pruneCreatedFile    = "created"
	pruneDeletedFile    = "deleted"
	pruneExecutableMode = 0o755
)

func installPruneCommands(t *testing.T, directory string) {
	t.Helper()

	commands := t.TempDir()
	qm := fmt.Sprintf(
		"#!/bin/sh\ncase \"$1\" in\n"+
			"status) printf '%%s\\n' 'status: running' ;;\n"+
			"agent) printf '%%s\\n' 'thawed' ;;\n"+
			"listsnapshot) while IFS= read -r line; do printf '%%s\\n' \"$line\"; done < '%[1]s' ;;\n"+
			"snapshot) printf '%%s\\n' \"$3\" >> '%[1]s'; printf '%%s\\n' \"$3\" >> '%[2]s' ;;\n"+
			"delsnapshot) printf '%%s\\n' \"$3\" >> '%[3]s' ;;\n"+
			"*) exit 1 ;;\nesac\n",
		filepath.Join(directory, pruneListingFile),
		filepath.Join(directory, pruneCreatedFile),
		filepath.Join(directory, pruneDeletedFile),
	)
	scripts := map[string]string{
		"qm":    qm,
		"ping":  "#!/bin/sh\nexit 0\n",
		"ping6": "#!/bin/sh\nexit 0\n",
	}
	for name, script := range scripts {
		path := filepath.Join(commands, name)
		if err := os.WriteFile(path, []byte(script), pruneExecutableMode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", commands)
}

func readPruneNames(t *testing.T, directory, name string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func runPruneChild(t *testing.T) {
	t.Helper()

	directory := os.Getenv(pruneDirectoryEnv)
	cfg := &config.Config{}
	cfg.MwanVMID = pruneVMID
	cfg.Network.PingTargetIPv4 = "192.0.2.1"
	cfg.Network.PingTargetIPv6 = "2001:db8::1"
	cfg.Watchdog.LogFile = filepath.Join(directory, "watchdog.log")
	cfg.Watchdog.JSONLogFile = filepath.Join(directory, "watchdog.json")
	cfg.Watchdog.RollbackStateFile = filepath.Join(directory, "rollback.state")
	cfg.Watchdog.RollbackLockFile = filepath.Join(directory, "rollback.lock")
	cfg.Watchdog.CheckIntervalDegraded = 1
	cfg.Watchdog.MaxIterations = 1
	cfg.Watchdog.SnapshotHealthyThreshold = 1
	cfg.Watchdog.MaxKnownGoodSnapshots = 3
	cfg.Watchdog.MaxTotalSnapshots = prunePreDeployCount

	if err := watchdog.Run(cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunPrunesOldestPreDeploySnapshotAtTotalLimit(t *testing.T) {
	if os.Getenv(pruneChildEnv) == "1" {
		runPruneChild(t)
		return
	}
	directory := t.TempDir()
	preDeploys := make([]string, 0, prunePreDeployCount)
	for i := range prunePreDeployCount {
		preDeploys = append(preDeploys, fmt.Sprintf("pre-deploy-20260809T1200%02d", i))
	}
	listing := strings.Join(preDeploys, "\n") + "\n"
	listingPath := filepath.Join(directory, pruneListingFile)
	if err := os.WriteFile(listingPath, []byte(listing), 0o600); err != nil {
		t.Fatal(err)
	}
	installPruneCommands(t, directory)

	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), pruneChildEnv+"=1", pruneDirectoryEnv+"="+directory)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("Run: %v: %s", err, output)
	}

	created := readPruneNames(t, directory, pruneCreatedFile)
	if len(created) != 1 || !strings.HasPrefix(created[0], "known-good-") {
		t.Fatalf("created = %v, want one known-good-* snapshot", created)
	}
	deleted := readPruneNames(t, directory, pruneDeletedFile)
	want := []string{preDeploys[0]}
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
}
