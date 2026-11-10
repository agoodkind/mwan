package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/configstate"
)

const (
	configStateDeployText  = "1700000000\n"
	configStateDeployEpoch = 1700000000
	configStateHashedFile  = "etc/mwan/network.json"
	configStateGlobbedFile = "etc/systemd/network/10-wan.network"
)

type configStateInputs struct {
	root       string
	deployFile []byte
	args       []string
}

func writeConfigStateFile(t *testing.T, path string, text string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write config state file: %v", err)
	}
}

func writeConfigStateInputs(t *testing.T, deployText string) configStateInputs {
	t.Helper()

	directory := t.TempDir()
	root := filepath.Join(directory, "root")
	writeConfigStateFile(t, filepath.Join(root, configStateHashedFile), "{}\n")
	writeConfigStateFile(t, filepath.Join(root, configStateGlobbedFile), "[Match]\nName=wan0\n")
	deployPath := filepath.Join(directory, "last-deploy")
	if deployText != "" {
		writeConfigStateFile(t, deployPath, deployText)
	}
	configPath := filepath.Join(directory, "config.toml")
	writeConfigStateFile(t, configPath, fmt.Sprintf("[agent]\ndeploy_file = %q\n", deployPath))
	return configStateInputs{
		root:       root,
		deployFile: []byte(deployText),
		args:       []string{"config-state", "--config", configPath, "--root", root},
	}
}

func runConfigStateSubcommand(t *testing.T, args []string) configstate.State {
	t.Helper()

	run := runStatusSubcommand(t, args)
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", run.exitCode, run.stderr)
	}
	if strings.Count(run.stdout, "\n") != 1 {
		t.Fatalf("stdout = %q, want exactly one line", run.stdout)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run.stdout), &fields); err != nil {
		t.Fatalf("decode stdout %q: %v", run.stdout, err)
	}
	for _, name := range []string{"config_hash", "config_manifest", "last_deploy_epoch"} {
		if _, present := fields[name]; !present {
			t.Fatalf("stdout = %q, want the field %s", run.stdout, name)
		}
	}
	if len(fields) != 3 {
		t.Fatalf("stdout = %q, want exactly three fields", run.stdout)
	}
	var state configstate.State
	if err := json.Unmarshal([]byte(run.stdout), &state); err != nil {
		t.Fatalf("decode stdout %q: %v", run.stdout, err)
	}
	return state
}

func TestConfigStatePrintsTheSharedHashAndDeployEpoch(t *testing.T) {
	t.Parallel()

	inputs := writeConfigStateInputs(t, configStateDeployText)

	state := runConfigStateSubcommand(t, inputs.args)

	want := configstate.Read(inputs.root, inputs.deployFile)
	if state != want {
		t.Fatalf("state = %+v, want %+v", state, want)
	}
	if state.LastDeployEpoch != configStateDeployEpoch {
		t.Fatalf("last_deploy_epoch = %d, want %d", state.LastDeployEpoch, configStateDeployEpoch)
	}
	for _, name := range []string{configStateHashedFile, configStateGlobbedFile} {
		if !strings.Contains(state.ConfigManifest, "  "+filepath.Join(inputs.root, name)+"\n") {
			t.Fatalf("config_manifest = %q, want the file %s", state.ConfigManifest, name)
		}
	}
}

func TestConfigStateHashChangesWhenAHashedFileChanges(t *testing.T) {
	t.Parallel()

	inputs := writeConfigStateInputs(t, configStateDeployText)
	before := runConfigStateSubcommand(t, inputs.args)
	writeConfigStateFile(t, filepath.Join(inputs.root, configStateHashedFile), "{\"changed\":true}\n")

	after := runConfigStateSubcommand(t, inputs.args)

	if after.ConfigHash == before.ConfigHash {
		t.Fatalf("config_hash = %q before and after the file change", after.ConfigHash)
	}
	if after.ConfigManifest == before.ConfigManifest {
		t.Fatalf("config_manifest = %q before and after the file change", after.ConfigManifest)
	}
	want := configstate.Read(inputs.root, inputs.deployFile)
	if after != want {
		t.Fatalf("state = %+v, want %+v", after, want)
	}
}

func TestConfigStateReportsEpochZeroWithoutADeployFile(t *testing.T) {
	t.Parallel()

	inputs := writeConfigStateInputs(t, "")

	state := runConfigStateSubcommand(t, inputs.args)

	if state.LastDeployEpoch != 0 {
		t.Fatalf("last_deploy_epoch = %d, want 0", state.LastDeployEpoch)
	}
	if state.ConfigHash != configstate.Read(inputs.root, nil).ConfigHash {
		t.Fatalf("config_hash = %q, want the hash of %s", state.ConfigHash, inputs.root)
	}
}

func TestConfigStateFailsOnAnUnknownFlag(t *testing.T) {
	t.Parallel()

	inputs := writeConfigStateInputs(t, configStateDeployText)

	run := runStatusSubcommand(t, append(inputs.args, "--unknown"))

	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1, stdout:\n%s", run.exitCode, run.stdout)
	}
	if run.stdout != "" {
		t.Fatalf("stdout = %q, want empty", run.stdout)
	}
	if !strings.Contains(run.stderr, "mwan config-state: ") {
		t.Fatalf("stderr = %q, want the mwan config-state prefix", run.stderr)
	}
}
