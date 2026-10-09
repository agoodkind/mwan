package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	exitOK     = 0
	exitFailed = 1

	validFailoverDocument = `mwan_vmid = "301"

[watchdog]
mwan_agent_tcp_addr = "10.0.0.1:50052"

[failover]
lxc_id = "203"
agent_tcp_addr = "10.0.0.2:50052"
`
	invalidFailoverDocument = `mwan_vmid = "301"

[failover]
lxc_id = "301"
agent_tcp_addr = "10.0.0.2"
`
)

func buildMwan(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mwan")
	build := exec.Command("go", "build", "-o", binary, ".")
	output, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("build mwan: %v\n%s", err, output)
	}
	return binary
}

func runCheckFailover(t *testing.T, binary string, document string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	command := exec.Command(binary, "deploy-gate", "check-failover", path)
	output, err := command.CombinedOutput()
	if err == nil {
		return exitOK, string(output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run check-failover: %v\n%s", err, output)
	}
	return exitError.ExitCode(), string(output)
}

func TestCheckFailover(t *testing.T) {
	t.Parallel()

	binary := buildMwan(t)
	cases := map[string]struct {
		document string
		want     int
		expect   []string
	}{
		"valid configuration without bgp section": {
			document: validFailoverDocument,
			want:     exitOK,
			expect:   nil,
		},
		"several failed preconditions": {
			document: invalidFailoverDocument,
			want:     exitFailed,
			expect: []string{
				"must differ",
				"agent_tcp_addr",
				"no primary endpoint",
			},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, output := runCheckFailover(t, binary, testCase.document)

			if code != testCase.want {
				t.Fatalf("exit code = %d, want %d\noutput: %s", code, testCase.want, output)
			}
			for _, want := range testCase.expect {
				if !strings.Contains(output, want) {
					t.Fatalf("output omits %q: %s", want, output)
				}
			}
		})
	}
}
