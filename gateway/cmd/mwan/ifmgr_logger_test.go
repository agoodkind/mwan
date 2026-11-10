package main_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test needs a DEBUG record that precedes any interface reads or changes.
const ifmgrDebugProbeMessage = "ifmgr: module constructed"

// TestIfMgrStdoutHonorsDebugSetting guards against DEBUG records filling syslog.
// Missing module sections disable every host role module during the test.
func TestIfMgrStdoutHonorsDebugSetting(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "mwan")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	testCases := []struct {
		name        string
		flagDebug   bool
		configDebug bool
		wantStdout  bool
	}{
		{name: "debug off", flagDebug: false, configDebug: false, wantStdout: false},
		{name: "debug flag", flagDebug: true, configDebug: false, wantStdout: true},
		{name: "debug config", flagDebug: false, configDebug: true, wantStdout: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			jsonLogPath := filepath.Join(root, "ifmgr.json")
			configPath := filepath.Join(root, "config.toml")
			configuration := fmt.Sprintf("[ifmgr]\nrole = \"host\"\ndebug = %t\njson_log_file = %q\n[ifmgr.iface.lo]\n",
				testCase.configDebug, jsonLogPath)
			if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
				t.Fatal(err)
			}
			arguments := []string{"ifmgr"}
			if testCase.flagDebug {
				arguments = append(arguments, "--debug")
			}
			stdout, stderr := runIfMgrUntilJSONRecord(t, binaryPath, configPath, jsonLogPath, arguments)
			if got := strings.Contains(stdout, ifmgrDebugProbeMessage); got != testCase.wantStdout {
				t.Errorf("stdout contains %q = %t, want %t\nstdout:\n%s\nstderr:\n%s",
					ifmgrDebugProbeMessage, got, testCase.wantStdout, stdout, stderr)
			}
		})
	}
}

// runIfMgrUntilJSONRecord needs an explicit stop because the host role daemon
// runs until it receives a signal.
func runIfMgrUntilJSONRecord(t *testing.T, binaryPath, configPath, jsonLogPath string, arguments []string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(t.Context(), binaryPath, arguments...)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start mwan ifmgr: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()

	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		jsonLog, _ := os.ReadFile(jsonLogPath)
		if bytes.Contains(jsonLog, []byte(ifmgrDebugProbeMessage)) {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("mwan ifmgr exited before the JSON log recorded %q: %v\njson log:\n%s\nstdout:\n%s\nstderr:\n%s",
				ifmgrDebugProbeMessage, err, jsonLog, stdout.String(), stderr.String())
		case <-deadline:
			_ = command.Process.Kill()
			<-exited
			t.Fatalf("JSON log lacks %q\njson log:\n%s\nstderr:\n%s", ifmgrDebugProbeMessage, jsonLog, stderr.String())
		case <-ticker.C:
		}
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal mwan ifmgr: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		<-exited
		t.Fatalf("mwan ifmgr ignored SIGTERM\nstderr:\n%s", stderr.String())
	}
	return stdout.String(), stderr.String()
}
