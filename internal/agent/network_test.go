package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/yangpub"
)

// gatewayConfigTOML is the gateway's runtime configuration as the deploy
// renders it now that the network tree moved out: a BGP speaker that declares
// it uses the wanconfig network configuration, an interface to install learned
// routes on, and the wan module sections reduced to the two filesystem paths
// TOML still owns. The legacy provider keys are present on purpose, because a
// gateway upgraded in place still carries them and none of them may reach the
// parsed configuration.
const gatewayConfigTOML = `
[bgp]
enabled = true
use_wanconfig = true
asn = 4200000001
router_id = "192.0.2.3"
learned_route_iface = "enmwanbr0"

[ifmgr]
role = "wan"
internal_prefix = "2001:db8:999::/60"
opnsense_edge_v6 = "2001:db8:999::2"
mwanbr_edge_v6 = "2001:db8:999::3"

[ifmgr.iface.enmbrains0]
name = "enmbrains0"

[ifmgr.wan.att]
iface = "stale0"
table_id = 999

[ifmgr.modules.wan.routes]
internal_iface = "stale0"
internal_net_v4 = "192.0.2.240/29"
health_state_file = "/run/mwan-health.state"

[ifmgr.modules.health]
state_file = "/run/mwan-health.state"
persist_state_file = "/var/lib/mwan/health-state"
`

// gatewayNetworkJSON is one gateway's network tree carrying all three
// providers. The table ids match the inventory the deploy renders from, so the
// tables this test expects are the tables a gateway really owns. Addresses are
// documentation prefixes.
const gatewayNetworkJSON = `{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored",
        "goodkind-mwan-steering:steering": { "tier": 0, "weight": 1 },
        "goodkind-mwan-steering:wan": {
          "name": "att",
          "table-id": 100,
          "fw-mark": 1,
          "fw-mark-prio": 100,
          "from-prio": 55,
          "npt-prefix": "2001:db8:beef:100::/60"
        }
      },
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "rendered",
        "goodkind-mwan-steering:link": { "match": { "driver": "igc" } },
        "ietf-ip:ipv4": {
          "address": [{ "ip": "203.0.113.2", "prefix-length": 29 }],
          "goodkind-mwan-steering:dhcp": false
        },
        "goodkind-mwan-steering:steering": { "tier": 0, "weight": 1 },
        "goodkind-mwan-steering:wan": {
          "name": "webpass",
          "table-id": 200,
          "fw-mark": 2,
          "fw-mark-prio": 200,
          "from-prio": 56,
          "npt-prefix": "2001:db8:beef:200::/60"
        }
      },
      {
        "name": "enmbrains0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "rendered",
        "goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:03" } },
        "goodkind-mwan-steering:steering": { "tier": 0, "weight": 1 },
        "goodkind-mwan-steering:wan": {
          "name": "monkeybrains",
          "table-id": 300,
          "fw-mark": 3,
          "fw-mark-prio": 300,
          "from-prio": 57,
          "npt-prefix": "2001:db8:beef:300::/60"
        }
      },
      { "name": "enmwanbr0", "type": "iana-if-type:other" }
    ],
    "goodkind-mwan-steering:steering-group": {
      "hash-mode": "source",
      "translation": {
        "internal-prefix": "2001:db8:b01::/60",
        "opnsense-edge-v6": "2001:db8:b01:fe::2",
        "mwanbr-edge-v6": "2001:db8:b01:fe::3"
      },
      "routes": {
        "internal-iface": "enmwanbr0",
        "internal-net-v4": "192.0.2.0/29"
      },
      "health": { "probe-timeout": 2000 }
    }
  }
}`

// networkSchemaDirForTest materialises the model set the gateway installs,
// from the bytes the binary embeds, so this test validates against the schema
// that ships.
func networkSchemaDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := yangpub.WriteSchema(dir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	return dir
}

// writeNetworkDocument puts body where the loader can read it.
func writeNetworkDocument(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write network document: %v", err)
	}
	return path
}

// parseGatewayConfig decodes the gateway's TOML the way the binary does, and
// proves the network keys it still carries reach nothing.
func parseGatewayConfig(t *testing.T) *config.Config {
	t.Helper()
	var cfg config.Config
	if err := toml.Unmarshal([]byte(gatewayConfigTOML), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}
	if len(cfg.IfMgr.WAN) != 0 {
		t.Fatalf("[ifmgr.wan] still decodes from TOML: %#v", cfg.IfMgr.WAN)
	}
	if !cfg.BGP.UseWanconfig {
		t.Fatal("use_wanconfig did not decode from the gateway TOML")
	}
	return &cfg
}

// discardLogger keeps the daemon's log lines out of the test output while the
// code under test logs exactly as it does in production.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestSpeaker builds the speaker the agent builds, unstarted. Configuring
// the FIB opens no session, so the speaker never touches the network here.
func newTestSpeaker(t *testing.T, cfg *config.Config, log *slog.Logger) *bgp.Speaker {
	t.Helper()
	speaker, err := newBGPSpeaker(cfg, log)
	if err != nil {
		t.Fatalf("newBGPSpeaker: %v", err)
	}
	if speaker == nil {
		t.Fatal("newBGPSpeaker returned no speaker for an enabled [bgp] section")
	}
	return speaker
}

// TestConfigureBGPFIBOwnsEveryProviderTable drives the agent's real startup
// path: the gateway's TOML, which carries no network tree, then the network
// file through the loader the daemon uses, and asserts the installer ends up
// owning the main table plus one table per provider. The agent runs as a second
// process that never loaded the network file, so a regression here is a
// silently degraded gateway rather than a failed start.
func TestConfigureBGPFIBOwnsEveryProviderTable(t *testing.T) {
	t.Parallel()

	cfg := parseGatewayConfig(t)
	log := discardLogger()
	speaker := newTestSpeaker(t, cfg, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := configureBGPFIB(
		ctx,
		cfg,
		speaker,
		log,
		writeNetworkDocument(t, gatewayNetworkJSON),
		networkSchemaDirForTest(t),
	)
	if err != nil {
		t.Fatalf("configureBGPFIB: %v", err)
	}

	// tablesFromConfig orders the provider tables by provider name, so att,
	// monkeybrains, and webpass yield 100, 300, 200.
	want := []int{unix.RT_TABLE_MAIN, 100, 300, 200}
	if got := tablesFromConfig(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("owned tables = %v, want %v", got, want)
	}
}

// TestConfigureBGPFIBRefusesAnUnreadableNetworkFile pins the other half of the
// contract: a gateway that cannot read its network file stops instead of
// starting a speaker that would own the main table alone.
func TestConfigureBGPFIBRefusesAnUnreadableNetworkFile(t *testing.T) {
	t.Parallel()

	cfg := parseGatewayConfig(t)
	log := discardLogger()
	speaker := newTestSpeaker(t, cfg, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := configureBGPFIB(
		ctx,
		cfg,
		speaker,
		log,
		filepath.Join(t.TempDir(), "absent.json"),
		networkSchemaDirForTest(t),
	)
	if err == nil {
		t.Fatal("configureBGPFIB accepted a missing network file")
	}
}

// failoverConfigTOML is the failover container's configuration: a speaker that
// installs learned routes on an interface, declares it does not use the
// wanconfig network configuration, and carries no provider section. The deploy
// now renders the key explicitly, and the absent form below covers a container
// still carrying the config.toml it had before this key existed.
const failoverConfigTOML = `
[bgp]
enabled = true
use_wanconfig = false
asn = 4200000001
router_id = "192.0.2.4"
learned_route_iface = "eth1"

[ifmgr]
role = "failover"

[ifmgr.iface.eth0]
name = "eth0"
`

// TestConfigureBGPFIBSkipsTheLoadWhereWanconfigIsOff covers the failover
// container, which receives no network file and must configure exactly as it
// does today: no load attempted, no startup failure, and the main table alone.
// Both the explicitly false key the deploy renders and the absent key an
// un-redeployed container still carries must behave that way.
func TestConfigureBGPFIBSkipsTheLoadWhereWanconfigIsOff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configTOML string
	}{
		{name: "key rendered false", configTOML: failoverConfigTOML},
		{
			name: "key absent",
			configTOML: strings.Replace(
				failoverConfigTOML,
				"use_wanconfig = false\n",
				"",
				1,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var cfg config.Config
			if err := toml.Unmarshal([]byte(test.configTOML), &cfg); err != nil {
				t.Fatalf("toml.Unmarshal: %v", err)
			}
			if cfg.BGP.UseWanconfig {
				t.Fatal("failover configuration must not turn the wanconfig load on")
			}
			log := discardLogger()
			speaker := newTestSpeaker(t, &cfg, log)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Both paths point at nothing readable. Reaching the loader at all
			// is the failure this test exists to catch.
			err := configureBGPFIB(
				ctx,
				&cfg,
				speaker,
				log,
				filepath.Join(t.TempDir(), "absent.json"),
				filepath.Join(t.TempDir(), "absent-schema"),
			)
			if err != nil {
				t.Fatalf("configureBGPFIB rejected a speaker that uses no wanconfig: %v", err)
			}
			want := []int{unix.RT_TABLE_MAIN}
			if got := tablesFromConfig(&cfg); !reflect.DeepEqual(got, want) {
				t.Fatalf("owned tables = %v, want %v", got, want)
			}
		})
	}
}
