package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const minNetworkDocument = "../../yang/instances/network-min.json"

type networkReplacement struct {
	old string
	new string
}

func modifiedNetwork(t *testing.T, replacements ...networkReplacement) string {
	t.Helper()
	body, err := os.ReadFile(minNetworkDocument)
	if err != nil {
		t.Fatalf("read %s: %v", minNetworkDocument, err)
	}
	mutated := string(body)
	for _, replacement := range replacements {
		updated := strings.Replace(mutated, replacement.old, replacement.new, 1)
		if updated == mutated {
			t.Fatalf("network document omits %q", replacement.old)
		}
		mutated = updated
	}
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	return path
}

func webpassIPv6NoDHCP(t *testing.T) string {
	t.Helper()
	const anchor = `        "goodkind-mwan-steering:wan": {
          "name": "webpass",`
	return modifiedNetwork(t, networkReplacement{
		old: anchor,
		new: `        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:accept-ra": false
        },
` + anchor,
	})
}

func runCheckNetworkCommand(t *testing.T, arguments ...string) (int, string) {
	t.Helper()
	commandArguments := append([]string{"deploy-gate", "check-network"}, arguments...)
	command := exec.Command(os.Args[0], commandArguments...)
	command.Env = append(os.Environ(), childMainEnv+"=1")
	output, err := command.CombinedOutput()
	if err == nil {
		return exitDeployGateOK, string(output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run check-network: %v\n%s", err, output)
	}
	return exitError.ExitCode(), string(output)
}

func TestCheckNetwork(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		document func(*testing.T) string
		want     int
		expect   []string
	}{
		"valid document with three providers": {
			document: func(*testing.T) string { return minNetworkDocument },
			want:     exitDeployGateOK,
			expect:   []string{"3 providers, 0 rejected"},
		},
		"an ipv6 container with no dhcp": {
			document: webpassIPv6NoDHCP,
			want:     exitDeployGateFailed,
			expect: []string{
				"interface enwebpass0: ipv6/dhcp is required",
				"2 providers, 1 rejected",
			},
		},
		"two rejected providers": {
			document: func(t *testing.T) string {
				t.Helper()
				const anchor = `        "goodkind-mwan-steering:wan": {
          "name": "webpass",`
				return modifiedNetwork(t,
					networkReplacement{
						old: anchor,
						new: `        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:accept-ra": false
        },
` + anchor,
					},
					networkReplacement{
						old: "          \"from-prio\": 57,\n",
						new: "",
					},
				)
			},
			want: exitDeployGateFailed,
			expect: []string{
				"interface enwebpass0: ipv6/dhcp is required",
				"interface enmbrains0: wan monkeybrains: from-prio is required",
				"1 providers, 2 rejected",
			},
		},
		"schema-invalid provider": {
			document: func(t *testing.T) string {
				t.Helper()
				return modifiedNetwork(t, networkReplacement{
					old: "          \"name\": \"webpass\",\n",
					new: "",
				})
			},
			want:   exitDeployGateFailed,
			expect: []string{"network configuration rejected"},
		},
		"duplicate routing table": {
			document: func(t *testing.T) string {
				t.Helper()
				return modifiedNetwork(t, networkReplacement{
					old: "          \"table-id\": 200,\n",
					new: "          \"table-id\": 100,\n",
				})
			},
			want:   exitDeployGateFailed,
			expect: []string{"table-id 100 is already taken"},
		},
		"no file at the path": {
			document: func(t *testing.T) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "absent.json")
			},
			want:   exitDeployGateFailed,
			expect: []string{"network configuration rejected"},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, output := runCheckNetworkCommand(
				t,
				testCase.document(t),
				networkSchemaDirForTest(t),
			)

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

func TestCheckNetworkRequiresTwoArguments(t *testing.T) {
	t.Parallel()

	code, _ := runCheckNetworkCommand(t, minNetworkDocument)

	if code != exitDeployGateUsage {
		t.Fatalf("exit code = %d, want %d", code, exitDeployGateUsage)
	}
}
