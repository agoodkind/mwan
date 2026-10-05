package main_test

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/statuspush"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	// mainEntryEnv makes the re-run test binary execute the mwan command line,
	// which TestMain in the package tests reads.
	mainEntryEnv = "MWAN_INSTALL_TEST_MAIN"

	networkTemplate = "../../yang/instances/network-min.json"

	freshStateBound = "10m"
)

type gatewayStatusRun struct {
	stdout   string
	stderr   string
	exitCode int
}

func writeGatewayStatusInputs(t *testing.T, statePath string, maxStateAge string) []string {
	t.Helper()

	directory := t.TempDir()
	template, err := os.ReadFile(networkTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", networkTemplate, err)
	}
	document := strings.Replace(string(template), `"enabled": false`, `"enabled": true`, 1)
	if document == string(template) {
		t.Fatal("the network template has no disabled health container to enable")
	}
	networkPath := filepath.Join(directory, "network.json")
	if err := os.WriteFile(networkPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write network document: %v", err)
	}
	configPath := filepath.Join(directory, "config.toml")
	configText := fmt.Sprintf("[ifmgr.modules.health]\nstate_file = %q\n", statePath)
	if maxStateAge != "" {
		configText += fmt.Sprintf("max_state_age = %q\n", maxStateAge)
	}
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	schemaDir := filepath.Join(directory, "schema")
	if err := os.Mkdir(schemaDir, 0o700); err != nil {
		t.Fatalf("create schema directory: %v", err)
	}
	if _, err := yangpub.WriteSchema(schemaDir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	return []string{
		"gateway-status",
		"--config", configPath,
		"--network", networkPath,
		"--schema-dir", schemaDir,
	}
}

func runStatusSubcommand(t *testing.T, args []string) gatewayStatusRun {
	t.Helper()

	command := exec.Command(os.Args[0], args...)
	command.Env = append(os.Environ(), mainEntryEnv+"=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("run mwan %v: %v", args, err)
		}
		exitCode = exitError.ExitCode()
	}
	return gatewayStatusRun{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func TestGatewayStatusPrintsTheVerdictFromTheStateFile(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "health.state")
	stateText := "webpass:unhealthy\nmonkeybrains:healthy\n"
	if err := os.WriteFile(statePath, []byte(stateText), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	writtenAt := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if err := os.Chtimes(statePath, writtenAt, writtenAt); err != nil {
		t.Fatalf("set state file time: %v", err)
	}

	run := runStatusSubcommand(t, writeGatewayStatusInputs(t, statePath, freshStateBound))

	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", run.exitCode, run.stderr)
	}
	if strings.Count(run.stdout, "\n") != 1 {
		t.Fatalf("stdout = %q, want exactly one line", run.stdout)
	}
	status, err := statuspush.UnmarshalStatus([]byte(run.stdout))
	if err != nil {
		t.Fatalf("decode stdout %q: %v", run.stdout, err)
	}
	if !status.SentAt.Equal(writtenAt) {
		t.Fatalf("sent_at = %s, want the state file time %s", status.SentAt, writtenAt)
	}
	if status.ActiveTier != 2 {
		t.Fatalf("active tier = %d, want 2", status.ActiveTier)
	}
	want := map[string]string{"webpass": "unhealthy", "monkeybrains": "healthy"}
	if !maps.Equal(status.Providers, want) {
		t.Fatalf("providers = %v, want %v", status.Providers, want)
	}
}

func TestGatewayStatusFailsWhenTheStateFileIsMissing(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "absent.state")

	run := runStatusSubcommand(t, writeGatewayStatusInputs(t, statePath, freshStateBound))

	if run.exitCode == 0 {
		t.Fatalf("exit code = 0 with no state file, stdout:\n%s", run.stdout)
	}
	if run.stdout != "" {
		t.Fatalf("stdout = %q, want empty", run.stdout)
	}
	if !strings.Contains(run.stderr, "stat health state file") {
		t.Fatalf("stderr = %q, want the missing file reason", run.stderr)
	}
}

func TestGatewayStatusFailsWhenTheStateFileIsStale(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "health.state")
	if err := os.WriteFile(statePath, []byte("webpass:healthy\n"), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	writtenAt := time.Now().Add(-time.Hour)
	if err := os.Chtimes(statePath, writtenAt, writtenAt); err != nil {
		t.Fatalf("set state file time: %v", err)
	}

	run := runStatusSubcommand(t, writeGatewayStatusInputs(t, statePath, freshStateBound))

	if run.exitCode == 0 {
		t.Fatalf("exit code = 0 with a stale state file, stdout:\n%s", run.stdout)
	}
	if run.stdout != "" {
		t.Fatalf("stdout = %q, want empty", run.stdout)
	}
	if !strings.Contains(run.stderr, "older than ifmgr.modules.health.max_state_age 10m0s") {
		t.Fatalf("stderr = %q, want the stale file reason", run.stderr)
	}
}

func TestGatewayStatusFailsWhenTheAgeBoundIsNotSet(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "health.state")
	if err := os.WriteFile(statePath, []byte("webpass:healthy\n"), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	run := runStatusSubcommand(t, writeGatewayStatusInputs(t, statePath, ""))

	if run.exitCode == 0 {
		t.Fatalf("exit code = 0 with no age bound, stdout:\n%s", run.stdout)
	}
	if run.stdout != "" {
		t.Fatalf("stdout = %q, want empty", run.stdout)
	}
	if !strings.Contains(run.stderr, "ifmgr.modules.health.max_state_age is not set") {
		t.Fatalf("stderr = %q, want the missing setting reason", run.stderr)
	}
}
