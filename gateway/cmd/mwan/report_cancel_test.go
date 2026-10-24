package main_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/mwan/internal/testsmtp"
)

func TestCanceledManualReportDoesNotClaimAcceptance(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	cfg, configPath, arguments := reportCommandConfig(t, server)
	recordReportCommandHistory(t, cfg, arguments)
	network, err := os.ReadFile(arguments[3])
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "network.json")
	if err := syscall.Mkfifo(input, 0o600); err != nil {
		t.Fatal(err)
	}
	arguments[3] = input
	command := exec.CommandContext(t.Context(), buildReportCommand(t), arguments...)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+configPath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	writer, err := os.OpenFile(input, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	// The blocked document read lets the signal cancel the command before rendering.
	time.Sleep(100 * time.Millisecond)
	if _, err := writer.Write(network); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || !strings.Contains(output.String(), "context canceled") {
		t.Fatalf("canceled send did not report failure: error=%v output=%s", err, output.String())
	}
	if strings.Contains(output.String(), "accepted") {
		t.Fatalf("canceled send claimed transport acceptance: %s", output.String())
	}
}
