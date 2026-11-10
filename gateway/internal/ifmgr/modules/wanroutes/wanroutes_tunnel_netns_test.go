//go:build linux && netns

package wanroutes

import (
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
)

const (
	tunnelRouteChildEnv    = "MWAN_WANROUTES_TUNNEL_CHILD"
	tunnelRouteID          = "tunnel-6in4"
	tunnelRouteIface       = "tun6in4"
	tunnelRouteInternal    = "lan0"
	tunnelRouteUnderlay    = "isp0"
	tunnelRouteOther       = "other0"
	tunnelRouteLocal       = "192.0.2.10"
	tunnelRouteGateway     = "192.0.2.1"
	tunnelRouteOtherGW     = "203.0.113.1"
	tunnelRouteRemote      = "198.51.100.1"
	tunnelRouteEndpoint    = tunnelRouteRemote + "/32"
	tunnelRouteInnerGW     = "2001:db8:6::1"
	tunnelRouteInnerPeer   = "2001:db8:6::9"
	tunnelRouteUnderMAC    = "02:00:5e:00:53:01"
	tunnelRouteOtherMAC    = "02:00:5e:00:53:02"
	tunnelRouteUnderTbl    = 100
	tunnelRouteTunnelTbl   = 200
	tunnelRouteOtherTbl    = 300
	tunnelRouteUnderMetric = 200
	tunnelRouteOtherMetric = 100
	tunnelRouteUnderMark   = 1
	ipProtocolIPv6InIPv4   = 41
)

func enterTunnelRouteNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(tunnelRouteChildEnv) == t.Name() {
		return true
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), tunnelRouteChildEnv+"="+t.Name())
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated tunnel route test (needs CAP_SYS_ADMIN and CAP_NET_ADMIN): %v: %s", err, output)
	}
	return false
}

func tunnelRouteConfig(healthPath string, withTunnel bool) Config {
	cfg := Config{
		InternalIface: tunnelRouteInternal, OpnsenseEdgeV6: "2001:db8:b01:fe::2", InternalNetV4: "198.18.0.0/29",
		HealthStateFile: healthPath,
		WANs: []WAN{
			{
				WANRef:  ifmgr.WANRef{ID: "isp", Name: "isp", Iface: tunnelRouteUnderlay},
				TableID: tunnelRouteUnderTbl, FwMark: tunnelRouteUnderMark, FwMarkPrio: 100, FromPrio: 55,
				TranslationV4: &config.IPv4Translation{Mode: config.TranslationNAPT44}, Tier: 0, Weight: 1,
			},
			{
				WANRef:  ifmgr.WANRef{ID: "other", Name: "other", Iface: tunnelRouteOther},
				TableID: tunnelRouteOtherTbl, FwMark: 3, FwMarkPrio: 300, FromPrio: 57,
				TranslationV4: &config.IPv4Translation{Mode: config.TranslationNAPT44}, Tier: 0, Weight: 1,
			},
		},
	}
	if withTunnel {
		cfg.WANs = append(cfg.WANs, WAN{
			WANRef:  ifmgr.WANRef{ID: tunnelRouteID, Name: "tunnel", Iface: tunnelRouteIface},
			TableID: tunnelRouteTunnelTbl, FwMark: 2, FwMarkPrio: 200, FromPrio: 56,
			TranslationV6: &config.IPv6Translation{Mode: config.TranslationNative}, Tier: 0, Weight: 1,
			Tunnel: tunnelRouteIntent(),
		})
	}
	return cfg
}

func tunnelRouteIntent() *interfaceintent.Tunnel {
	return &interfaceintent.Tunnel{
		Protocol: interfaceintent.TunnelProtocol6in4, Underlay: tunnelRouteUnderlay,
		Remote: netip.MustParseAddr(tunnelRouteRemote), Local: netip.MustParseAddr(tunnelRouteLocal),
	}
}
