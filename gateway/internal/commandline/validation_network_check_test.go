package commandline

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	minNetworkDocument = "../../yang/instances/network-min.json"
	lxcNetworkDocument = "../../yang/instances/network-lxc.json"
)

type networkReplacement struct {
	old string
	new string
}

func modifiedNetwork(t *testing.T, replacements ...networkReplacement) string {
	t.Helper()

	return modifiedNetworkFrom(t, minNetworkDocument, replacements...)
}

func modifiedNetworkFrom(t *testing.T, base string, replacements ...networkReplacement) string {
	t.Helper()
	body, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read %s: %v", base, err)
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

	return modifiedNetwork(
		t,
		networkReplacement{
			old: `          "goodkind-mwan-steering:dhcp": false,` + "\n",
			new: "",
		},
	)
}

func runCheckNetworkCommand(t *testing.T, arguments ...string) (int, string) {
	t.Helper()
	commandArguments := append([]string{"debug", "check-network"}, arguments...)
	command := exec.Command(os.Args[0], commandArguments...)
	command.Env = append(os.Environ(), childMainEnv+"=1")
	output, err := command.CombinedOutput()
	if err == nil {
		return exitCheckOK, string(output)
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run check-network: %v\n%s", err, output)
	}

	return exitError.ExitCode(), string(output)
}

func TestCheckNetwork(t *testing.T) {
	t.Parallel()

	removeManagementInterface := networkReplacement{
		old: "        \"management-interface\": \"enmgmt0\",\n",
		new: "",
	}

	cases := map[string]struct {
		document func(*testing.T) string
		want     int
		expect   []string
	}{
		"valid document with three providers": {
			document: func(*testing.T) string { return minNetworkDocument },
			want:     exitCheckOK,
			expect:   []string{"3 providers, 0 rejected"},
		},
		"valid document with one provider": {
			document: func(*testing.T) string { return lxcNetworkDocument },
			want:     exitCheckOK,
			expect:   []string{"1 providers, 0 rejected"},
		},
		"gateway with a management service and no management interface": {
			document: func(t *testing.T) string {
				return modifiedNetworkFrom(t, lxcNetworkDocument, removeManagementInterface)
			},
			want:   exitCheckFailed,
			expect: []string{"management interface: invalid interface name"},
		},
		"an ipv6 container with no dhcp": {
			document: webpassIPv6NoDHCP,
			want:     exitCheckFailed,
			expect: []string{
				"interface enwebpass0: ipv6/dhcp is required",
				"2 providers, 1 rejected",
			},
		},
		"two rejected providers": {
			document: func(t *testing.T) string {
				t.Helper()

				return modifiedNetwork(
					t,
					networkReplacement{
						old: `          "goodkind-mwan-steering:dhcp": false,` + "\n",
						new: "",
					},
					networkReplacement{
						old: "          \"from-prio\": 57,\n",
						new: "",
					},
				)
			},
			want: exitCheckFailed,
			expect: []string{
				"interface enwebpass0: ipv6/dhcp is required",
				"interface enmbrains0: wan monkeybrains: from-prio is required",
				"1 providers, 2 rejected",
			},
		},
		"schema-invalid provider": {
			document: func(t *testing.T) string {
				t.Helper()

				return modifiedNetwork(
					t,
					networkReplacement{
						old: "          \"name\": \"webpass\",\n",
						new: "",
					},
				)
			},
			want:   exitCheckFailed,
			expect: []string{"network configuration rejected"},
		},
		"duplicate routing table": {
			document: func(t *testing.T) string {
				t.Helper()

				return modifiedNetwork(
					t,
					networkReplacement{
						old: "          \"table-id\": 200,\n",
						new: "          \"table-id\": 100,\n",
					},
				)
			},
			want:   exitCheckFailed,
			expect: []string{"table-id 100 is already taken"},
		},
		"shared provider label with explicit connection pin": {
			document: func(t *testing.T) string {
				t.Helper()

				return modifiedNetwork(
					t,
					networkReplacement{
						old: `"name": "enwebpass0",`,
						new: `"name": "enwebpass0", "goodkind-mwan-steering:connection-id": "sonic-a",`,
					},
					networkReplacement{
						old: `"name": "enatt0",`,
						new: `"name": "enatt0", "goodkind-mwan-steering:connection-id": "sonic-b",`,
					},
					networkReplacement{old: `"name": "att",`, new: `"name": "webpass",`},
					networkReplacement{
						old: `"pinned-provider": "att"`,
						new: `"pinned-connection-id": "sonic-b"`,
					},
				)
			},
			want:   exitCheckOK,
			expect: []string{"3 providers, 0 rejected"},
		},
		"ambiguous legacy provider pin": {
			document: func(t *testing.T) string {
				t.Helper()

				return modifiedNetwork(
					t,
					networkReplacement{
						old: `"name": "enwebpass0",`,
						new: `"name": "enwebpass0", "goodkind-mwan-steering:connection-id": "sonic-a",`,
					},
					networkReplacement{
						old: `"name": "enatt0",`,
						new: `"name": "enatt0", "goodkind-mwan-steering:connection-id": "sonic-b",`,
					},
					networkReplacement{old: `"name": "att",`, new: `"name": "webpass",`},
					networkReplacement{
						old: `"pinned-provider": "att"`,
						new: `"pinned-provider": "webpass"`,
					},
				)
			},
			want:   exitCheckFailed,
			expect: []string{"matches multiple connections; use pinned-connection-id"},
		},
		"no file at the path": {
			document: func(t *testing.T) string {
				t.Helper()

				return filepath.Join(t.TempDir(), "absent.json")
			},
			want:   exitCheckFailed,
			expect: []string{"network configuration rejected"},
		},
	}
	for name, testCase := range cases {
		t.Run(
			name,
			func(t *testing.T) {
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
			},
		)
	}
}

func TestCheckNetworkRequiresTwoArguments(t *testing.T) {
	t.Parallel()

	code, _ := runCheckNetworkCommand(t, minNetworkDocument)

	if code != exitCheckUsage {
		t.Fatalf("exit code = %d, want %d", code, exitCheckUsage)
	}
}

func TestCheckNetworkTranslation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		document func(*testing.T) string
		want     int
		expect   string
	}{
		"configured and native policies": {
			document: func(*testing.T) string { return minNetworkDocument },
			want:     exitCheckOK,
			expect:   "3 providers, 0 rejected",
		},
		"delegated policy": {
			document: func(*testing.T) string { return "../../yang/instances/network-freeform.json" },
			want:     exitCheckOK,
			expect:   "1 providers, 0 rejected",
		},
		"IPv4 mode on IPv6": {
			document: func(t *testing.T) string {
				t.Helper()

				return modifiedNetwork(
					t,
					networkReplacement{
						old: `"mode": "ietf-nat:nptv6"`,
						new: `"mode": "ietf-nat:napt44"`,
					},
				)
			},
			want:   exitCheckFailed,
			expect: `wan webpass ipv6: translation mode "ietf-nat:napt44" is not supported`,
		},
	}
	for name, testCase := range cases {
		t.Run(
			name,
			func(t *testing.T) {
				t.Parallel()
				code, output := runCheckNetworkCommand(
					t,
					testCase.document(t),
					networkSchemaDirForTest(t),
				)
				if code != testCase.want || !strings.Contains(output, testCase.expect) {
					t.Fatalf(
						"check-network exit = %d, want %d; output must contain %q:\n%s",
						code,
						testCase.want,
						testCase.expect,
						output,
					)
				}
			},
		)
	}
}
