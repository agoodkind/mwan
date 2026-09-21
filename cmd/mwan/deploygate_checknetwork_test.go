package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/networkjson"
)

// minNetworkDocument is the checked-in three-provider instance the repository
// keeps for the model, named from the package directory the test runs in.
const minNetworkDocument = "../../yang/instances/network-min.json"

// webpassIPv6NoDHCP reproduces the 2026-09-20 testbed outage in one document:
// an ipv6 container with accept-ra and no dhcp. The model leaves dhcp
// optional, so yanglint accepts the file and the loader refuses the entry.
func webpassIPv6NoDHCP(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(minNetworkDocument)
	if err != nil {
		t.Fatalf("read %s: %v", minNetworkDocument, err)
	}
	const anchor = `        "goodkind-mwan-steering:wan": {
          "name": "webpass",`
	mutated := strings.Replace(string(body), anchor,
		`        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:accept-ra": false
        },
`+anchor, 1)
	if mutated == string(body) {
		t.Fatal("the webpass wan container is absent from the instance")
	}
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	return path
}

// TestCheckNetwork runs the deploy-time check over the three documents a
// deploy meets: the instance the model ships, one the schema accepts and the
// loader refuses, and a path with no file. Each case calls the real loader,
// which is the program the daemon runs at startup.
func TestCheckNetwork(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		document func(*testing.T) string
		want     int
		expect   []string
	}{
		"the checked-in instance": {
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

			var out strings.Builder
			deps := newTestDeps(&out, &fakeClock{now: time.Unix(1000, 0)})
			deps.loadNetworkFrom = networkjson.Load

			code := checkNetwork(deps, testCase.document(t), networkSchemaDirForTest(t))

			if code != testCase.want {
				t.Fatalf("exit code = %d, want %d\noutput: %s", code, testCase.want, out.String())
			}
			for _, want := range testCase.expect {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output omits %q: %s", want, out.String())
				}
			}
		})
	}
}

func TestRunDeployGateRejectsCheckNetworkWithoutTwoArguments(t *testing.T) {
	t.Parallel()

	code := runDeployGate([]string{"check-network", minNetworkDocument})

	if code != exitDeployGateUsage {
		t.Fatalf("exit code = %d, want %d", code, exitDeployGateUsage)
	}
}
