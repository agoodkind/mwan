package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/gateway/internal/bgp"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/yangpub"
)

// gatewayConfigTOML contains the gateway's retained BGP settings.
const gatewayConfigTOML = `
[bgp]
enabled = true
asn = 4200000001
router_id = "192.0.2.3"
learned_route_iface = "enmwanbr0"

`

// gatewayNetworkJSON contains three providers with documentation addresses.
const gatewayNetworkJSON = `{
  "ietf-interfaces:interfaces": {
    "interface": [
      {
        "name": "enatt0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "hand-authored",
        "goodkind-mwan-steering:steering": {
          "tier": 0,
          "weight": 1
        },
        "goodkind-mwan-steering:wan": {
          "name": "att",
          "table-id": 100,
          "fw-mark": 1,
          "fw-mark-prio": 100,
          "from-prio": 55
        },
        "ietf-ip:ipv4": {
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:napt44"
          }
        },
        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:dhcp": true,
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:nptv6",
            "nptv6": {
              "internal-prefix": "2001:db8:b01::/60",
              "external-source": "delegated",
              "expected-prefix": "2001:db8:beef:100::/60"
            }
          },
          "goodkind-mwan-steering:delegation": {
            "hint": "::/56"
          }
        }
      },
      {
        "name": "enwebpass0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "rendered",
        "goodkind-mwan-steering:link": {
          "match": {
            "driver": "igc"
          }
        },
        "ietf-ip:ipv4": {
          "address": [
            {
              "ip": "203.0.113.2",
              "prefix-length": 29
            }
          ],
          "goodkind-mwan-steering:dhcp": false,
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:napt44"
          }
        },
        "goodkind-mwan-steering:steering": {
          "tier": 0,
          "weight": 1
        },
        "goodkind-mwan-steering:wan": {
          "name": "webpass",
          "table-id": 200,
          "fw-mark": 2,
          "fw-mark-prio": 200,
          "from-prio": 56
        },
        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:dhcp": true,
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:nptv6",
            "nptv6": {
              "internal-prefix": "2001:db8:b01::/60",
              "external-source": "delegated",
              "expected-prefix": "2001:db8:beef:200::/60"
            }
          },
          "goodkind-mwan-steering:delegation": {
            "hint": "::/56"
          }
        }
      },
      {
        "name": "enmbrains0",
        "type": "iana-if-type:other",
        "goodkind-mwan-steering:link-files": "rendered",
        "goodkind-mwan-steering:link": {
          "match": {
            "hardware-address": "02:00:5e:00:53:03"
          }
        },
        "goodkind-mwan-steering:steering": {
          "tier": 0,
          "weight": 1
        },
        "goodkind-mwan-steering:wan": {
          "name": "monkeybrains",
          "table-id": 300,
          "fw-mark": 3,
          "fw-mark-prio": 300,
          "from-prio": 57
        },
        "ietf-ip:ipv4": {
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:napt44"
          },
          "goodkind-mwan-steering:dhcp": true
        },
        "ietf-ip:ipv6": {
          "goodkind-mwan-steering:dhcp": true,
          "goodkind-mwan-steering:translation": {
            "mode": "ietf-nat:nptv6",
            "nptv6": {
              "internal-prefix": "2001:db8:b01::/60",
              "external-source": "delegated",
              "expected-prefix": "2001:db8:beef:300::/60"
            }
          },
          "goodkind-mwan-steering:delegation": {
            "hint": "::/56"
          }
        }
      },
      {
        "name": "enmwanbr0",
        "type": "iana-if-type:other"
      }
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
      "health": {
        "probe-timeout": 2000
      }
    }
  }
}`

// networkSchemaDirForTest writes the embedded YANG modules to a temporary directory.
func networkSchemaDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := yangpub.WriteSchema(dir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}
	return dir
}

// writeNetworkDocument writes a network document to a temporary file and returns its path.
func writeNetworkDocument(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write network document: %v", err)
	}
	return path
}

// parseGatewayConfig parses gateway TOML and verifies provider inventory is not defined there.
func parseGatewayConfig(t *testing.T) *config.Config {
	t.Helper()
	var cfg config.Config
	if err := toml.Unmarshal([]byte(gatewayConfigTOML), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}
	return &cfg
}

// discardLogger directs test logs to io.Discard.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestSpeaker creates a BGP speaker without starting its sessions.
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

// TestConfigureBGPFIBOwnsEveryProviderTable loads WANCONFIG provider tables into the BGP FIB.
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

// TestConfigureBGPFIBRefusesAnUnreadableNetworkFile rejects missing provider routing data.
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
