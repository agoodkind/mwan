package commandline_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/commandline"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/version"
)

func runCLIProcess(t *testing.T, binary string, arguments ...string) (string, string, int) {
	t.Helper()

	return runCLIProcessInput(t, binary, "", arguments...)
}

func runCLIProcessInput(
	t *testing.T,
	binary string,
	input string,
	arguments ...string,
) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = append(os.Environ(), "MWAN_CONFIG="+filepath.Join(t.TempDir(), "absent.toml"))
	command.Stdin = strings.NewReader(input)
	var output, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostics
	err := command.Run()
	if err == nil {
		return output.String(), diagnostics.String(), 0
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatal(err)
	}

	return output.String(), diagnostics.String(), exitError.ExitCode()
}

func TestCLIHelpEveryCommand(t *testing.T) {
	binary := testMwanBinary(t)

	for _, arguments := range commandline.CommandPathsForTest() {
		t.Run(
			strings.Join(arguments, "/"),
			func(t *testing.T) {
				invocation := append(append([]string(nil), arguments...), "--help")
				output, diagnostics, code := runCLIProcess(t, binary, invocation...)
				if code != 0 || !strings.Contains(output, "Usage:") ||
					!strings.Contains(output, "Examples:\n  mwan ") ||
					!strings.Contains(output, "Flags:") {
					t.Fatalf("help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
				}

				if diagnostics != "" {
					t.Fatalf("help wrote diagnostics: %s", diagnostics)
				}
			},
		)
	}

	output, diagnostics, code := runCLIProcess(t, binary)
	if code != 0 || !strings.Contains(output, "Available Commands:") {
		t.Fatalf("root help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}

	for _, name := range []string{
		"agent",
		"ifmgr",
		"health",
		"pd",
		"debug",
		"wanconfig-selftest",
		"version",
		"install",
		"gateway-status",
	} {
		if !strings.Contains(output, name) {
			t.Errorf("root help omitted %s", name)
		}
	}

	output, diagnostics, code = runCLIProcess(t, binary, "install")
	if code != 0 || strings.Count(output, "Usage:") != 1 || diagnostics != "" {
		t.Fatalf("install help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
}

func TestCLIHelpDocumentsInputs(t *testing.T) {
	binary := testMwanBinary(t)

	for _, test := range []struct {
		command []string
		inputs  []string
	}{
		{[]string{"help"}, []string{"command path", "root help"}},
		{[]string{"ifmgr"}, []string{"/etc/mwan/network.json", "/usr/local/share/wanconfig/yang"}},
		{[]string{"health"}, []string{"WANCONFIG JSON document", "YANG models"}},
		{[]string{"gateway-status"}, []string{"WANCONFIG JSON document", "YANG models"}},
		{
			[]string{"wanconfig-selftest"},
			[]string{"--repository", "--models-dir", "must be empty", "YANG models"},
		},
		{[]string{"pd"}, []string{"iface", "Linux interface", "CIDR notation"}},
		{[]string{"debug", "curl4"}, []string{"iface", "lowest firewall mark"}},
		{[]string{"debug", "curl6"}, []string{"iface", "lowest firewall mark"}},
		{[]string{"debug", "ping4"}, []string{"iface", "lowest firewall mark"}},
		{[]string{"debug", "ping6"}, []string{"iface", "lowest firewall mark"}},
		{[]string{"debug", "trace-start"}, []string{"trace_id", "current time"}},
		{[]string{"debug", "trace-tail"}, []string{"trace_id", "follows all events"}},
		{[]string{"debug", "check-network"}, []string{"network_json", "schema_dir"}},
		{[]string{"debug", "check-networkd"}, []string{"network_json", "schema_dir", "unit_dir"}},
		{
			[]string{"debug", "check-release"},
			[]string{"connection_id", "previous_owner", "networkd or mwan"},
		},
		{[]string{"debug", "check-firewall"}, []string{"network_json", "schema_dir"}},
		{[]string{"debug", "inspect-firewall"}, []string{"network_json", "schema_dir"}},
	} {
		arguments := append(append([]string(nil), test.command...), "--help")
		output, diagnostics, code := runCLIProcess(t, binary, arguments...)
		if code != 0 || diagnostics != "" {
			t.Fatalf("%v exit=%d diagnostics=%s", arguments, code, diagnostics)
		}

		description, _, _ := strings.Cut(output, "\n\nUsage:")

		for _, input := range test.inputs {
			if !strings.Contains(description, input) {
				t.Errorf("%v help description omitted %q: %s", arguments, input, description)
			}
		}
	}
}

func TestRunDisplaysCommandGroupHelp(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		action    string
	}{
		{nil, "gateway-status"},
		{[]string{"debug"}, "check-network"},
	} {
		var output, diagnostics bytes.Buffer
		logger, closer := logging.NewCommandLogger(&diagnostics, version.BuildVersionString())
		defer closer.Close()
		code := commandline.Run(
			test.arguments,
			strings.NewReader(""),
			&output,
			&diagnostics,
			logger,
		)
		if code != 0 || !strings.Contains(output.String(), "Available Commands:") ||
			!strings.Contains(output.String(), test.action) || diagnostics.Len() != 0 {
			t.Fatalf(
				"%v exit=%d output=%s diagnostics=%s",
				test.arguments,
				code,
				output.String(),
				diagnostics.String(),
			)
		}
	}
}

func TestRunWritesSelftestFailureToDiagnostics(t *testing.T) {
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "existing"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var output, diagnostics bytes.Buffer
	logger, closer := logging.NewCommandLogger(&diagnostics, version.BuildVersionString())
	defer closer.Close()
	code := commandline.Run(
		[]string{"wanconfig-selftest", "--repository", repository, "--models-dir", t.TempDir()},
		strings.NewReader(""),
		&output,
		&diagnostics,
		logger,
	)
	if code != 1 || output.Len() != 0 ||
		!strings.Contains(diagnostics.String(), "mwan wanconfig-selftest:") ||
		!strings.Contains(diagnostics.String(), "repository "+repository+" is not empty") {
		t.Fatalf(
			"selftest exit=%d output=%q diagnostics=%q",
			code,
			output.String(),
			diagnostics.String(),
		)
	}
}

func TestAgentHelpDoesNotCreateSocketOrLog(t *testing.T) {
	binary := testMwanBinary(t)
	root := t.TempDir()
	configPath := filepath.Join(root, "valid.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	socket := filepath.Join(root, "agent.sock")
	logPath := filepath.Join(root, "agent.log")
	output, diagnostics, code := runCLIProcess(
		t,
		binary,
		"agent",
		"--config",
		configPath,
		"--socket-path",
		socket,
		"--tcp-addr",
		"127.0.0.1:0",
		"--log-file",
		logPath,
		"--help",
	)
	if code != 0 || !strings.Contains(output, "Usage:") || diagnostics != "" {
		t.Fatalf("agent help exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}

	for _, path := range []string{socket, logPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("agent help changed %s: %v", path, err)
		}
	}
}

func TestCLIHelpSkipsConfigurationAndFirewall(t *testing.T) {
	binary := testMwanBinary(t)
	path := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(path, []byte("invalid = ["), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, arguments := range [][]string{
		{"ifmgr", "--role", "wan", "--config", path, "--help"},
		{"ifmgr", "--role", "wan", "--config", path, "-h"},
		{"debug", "--config", path, "status", "--help"},
	} {
		output, diagnostics, code := runCLIProcess(t, binary, arguments...)
		if code != 0 || !strings.Contains(output, "Usage:") || diagnostics != "" {
			t.Fatalf("%v exit=%d output=%s diagnostics=%s", arguments, code, output, diagnostics)
		}
	}
}

func TestCLINestedConfigurationPlacement(t *testing.T) {
	binary := testMwanBinary(t)

	firstPath := filepath.Join(t.TempDir(), "selected-runtime.toml")
	secondPath := filepath.Join(t.TempDir(), "ignored-runtime.toml")
	arguments := []string{
		"debug",
		"--config",
		firstPath,
		"status",
		"--config",
		secondPath,
	}
	_, diagnostics, code := runCLIProcess(t, binary, arguments...)
	if code != 1 || !strings.Contains(diagnostics, firstPath) ||
		strings.Contains(diagnostics, secondPath) || strings.Contains(diagnostics, "Usage:") {
		t.Fatalf("%v exit=%d diagnostics=%s", arguments, code, diagnostics)
	}
}

func TestCLIUsageAndRuntimeErrors(t *testing.T) {
	binary := testMwanBinary(t)

	for _, test := range []struct {
		arguments []string
		code      int
		usage     string
	}{
		{[]string{"unknown-command"}, 1, "mwan"},
		{[]string{"debug", "not-a-view"}, 1, "mwan debug"},
		{[]string{"ifmgr", "--unknown"}, 1, "mwan ifmgr"},
		{[]string{"ifmgr", "-config", "/absent.toml", "--help"}, 1, "mwan ifmgr"},
		{[]string{"ifmgr", "-help"}, 1, "mwan ifmgr"},
		{[]string{"ifmgr", "--role", "not-a-role"}, 1, "mwan ifmgr"},
		{[]string{"agent", "--config", ""}, 1, "mwan agent"},
		{[]string{"pd", ""}, 1, "mwan pd"},
		{[]string{"install", "--apply"}, 2, "mwan install"},
		{[]string{"version", "extra"}, 2, "mwan version"},
		{[]string{"report"}, 1, "mwan"},
		{[]string{"debug", "ping4", "one", "two"}, 1, "mwan debug ping4"},
		{[]string{"debug", "check-network"}, 64, "mwan debug check-network"},
	} {
		output, diagnostics, code := runCLIProcess(t, binary, test.arguments...)
		if code != test.code || strings.Count(diagnostics, "Usage:") != 1 ||
			!strings.Contains(diagnostics, test.usage) ||
			output != "" {
			t.Fatalf(
				"%v exit=%d output=%s diagnostics=%s",
				test.arguments,
				code,
				output,
				diagnostics,
			)
		}

		if strings.Contains(diagnostics, "read config") {
			t.Fatalf("usage attempted configuration: %s", diagnostics)
		}
	}

	_, diagnostics, code := runCLIProcess(t, binary, "agent")
	if code != 1 || !strings.Contains(diagnostics, "read config") ||
		strings.Contains(diagnostics, "Usage:") {
		t.Fatalf("runtime exit=%d diagnostics=%s", code, diagnostics)
	}
}

func TestCLIDebugPingUsesRequestedInterface(t *testing.T) {
	binary := testMwanBinary(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	iface := "mwan-no-iface"
	output, diagnostics, code := runCLIProcess(t, binary, "debug", "ping4", iface, "--config", path)
	if code != 0 || !strings.Contains(output, "via "+iface) ||
		strings.Count(output, "attempt ") != 6 ||
		strings.Count(output, "3 transmitted, 0 received") != 2 {
		t.Fatalf("debug ping exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
}

func TestCLICheckReleaseConfigurationFailuresUseStdout(t *testing.T) {
	binary := testMwanBinary(t)
	missing := filepath.Join(t.TempDir(), "missing.toml")
	invalid := filepath.Join(t.TempDir(), "invalid-ifmgr.toml")
	if err := os.WriteFile(
		invalid,
		[]byte("[ifmgr]\nnetworkd_timeout_seconds = 'invalid'\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{missing, invalid} {
		output, diagnostics, code := runCLIProcess(
			t,
			binary,
			"debug",
			"check-release",
			"provider-1",
			"mwan",
			"--config",
			path,
		)
		if code != 1 || !strings.HasPrefix(output, "release configuration rejected: ") ||
			strings.Contains(output, "Usage:") ||
			strings.Contains(diagnostics, "Usage:") ||
			strings.Contains(diagnostics, "mwan debug check-release:") {
			t.Fatalf(
				"release configuration %s exit=%d output=%s diagnostics=%s",
				path,
				code,
				output,
				diagnostics,
			)
		}
	}
}

func TestCLIDebugCheckEmptyFilePathsPreserveRuntimeFailures(t *testing.T) {
	binary := testMwanBinary(t)
	validConfig := filepath.Join(t.TempDir(), "valid.toml")
	if err := os.WriteFile(validConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	schema := filepath.Join("..", "..", "internal", "yangpub", "schema")

	for _, parent := range []string{"debug"} {
		for _, test := range []struct {
			action    string
			arguments []string
			stdout    string
			stderr    string
		}{
			{"check-network", []string{"", schema}, "network configuration rejected: ", ""},
			{
				"check-networkd",
				[]string{"", schema, t.TempDir()},
				"network configuration rejected: ",
				"",
			},
			{"check-firewall", []string{"", schema}, "", "no such file or directory"},
			{"inspect-firewall", []string{"", schema}, "", "no such file or directory"},
		} {
			t.Run(
				parent+"/"+test.action,
				func(t *testing.T) {
					arguments := append([]string{parent, test.action}, test.arguments...)
					command := exec.CommandContext(t.Context(), binary, arguments...)
					command.Env = append(os.Environ(), "MWAN_CONFIG="+validConfig)
					var output, diagnostics bytes.Buffer
					command.Stdout, command.Stderr = &output, &diagnostics
					err := command.Run()
					var exitError *exec.ExitError
					if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
						t.Fatalf(
							"empty path %v returned %v: stdout=%s stderr=%s",
							arguments,
							err,
							output.String(),
							diagnostics.String(),
						)
					}

					if test.stdout != "" && !strings.HasPrefix(output.String(), test.stdout) {
						t.Fatalf("stdout=%q, want prefix %q", output.String(), test.stdout)
					}

					if test.stdout == "" && output.Len() != 0 {
						t.Fatalf("unexpected stdout: %s", output.String())
					}

					if test.stderr != "" && !strings.Contains(diagnostics.String(), test.stderr) {
						t.Fatalf("stderr=%q, want %q", diagnostics.String(), test.stderr)
					}

					if strings.Contains(output.String(), "Usage:") ||
						strings.Contains(diagnostics.String(), "Usage:") {
						t.Fatalf(
							"runtime failure appended help: stdout=%s stderr=%s",
							output.String(),
							diagnostics.String(),
						)
					}
				},
			)
			_, diagnostics, code := runCLIProcess(t, binary, parent, test.action)
			if code != 64 || strings.Count(diagnostics, "Usage:") != 1 {
				t.Fatalf(
					"invalid argument count for %s %s returned %d: %s",
					parent,
					test.action,
					code,
					diagnostics,
				)
			}
		}
	}
}
