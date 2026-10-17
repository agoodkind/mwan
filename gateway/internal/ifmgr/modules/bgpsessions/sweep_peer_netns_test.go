//go:build linux && netns

package bgpsessions_test

import (
	"runtime"
	"testing"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/server"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func setLinkAddress(t *testing.T, handle *netlink.Handle, name, address string) {
	t.Helper()
	link, err := handle.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := netlink.ParseAddr(address)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Flags = unix.IFA_F_NODAD
	if err := handle.AddrAdd(link, parsed); err != nil {
		t.Fatalf("add %s to %s: %v", address, name, err)
	}
	if err := handle.LinkSetUp(link); err != nil {
		t.Fatalf("set %s up: %v", name, err)
	}
}

// startSweepPeer creates the session link as a veth pair and starts a GoBGP
// peer with graceful restart in a second namespace at the far end.
func startSweepPeer(t *testing.T) (netlink.Link, *server.BgpServer) {
	t.Helper()
	runtime.LockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	peerNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peerNamespace.Close() })
	if err := netns.Set(original); err != nil {
		t.Fatal(err)
	}
	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: sweepDevice}, PeerName: sweepPeerDevice}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatal(err)
	}
	peerLink, err := netlink.LinkByName(sweepPeerDevice)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(peerLink, int(peerNamespace)); err != nil {
		t.Fatal(err)
	}
	local, err := netlink.NewHandle()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(local.Delete)
	setLinkAddress(t, local, sweepDevice, sweepLocalAddress+"/64")
	remote, err := netlink.NewHandleAt(peerNamespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remote.Delete)
	setLinkAddress(t, remote, sweepPeerDevice, sweepGateway+"/64")

	bgpServer := server.NewBgpServer()
	entered := make(chan error, 1)
	go func() {
		// GoBGP opens the listening socket on the goroutine that runs Serve.
		runtime.LockOSThread()
		enterErr := netns.Set(peerNamespace)
		entered <- enterErr
		if enterErr == nil {
			bgpServer.Serve()
		}
	}()
	if err := <-entered; err != nil {
		t.Fatalf("enter the peer namespace: %v", err)
	}
	global := &apipb.Global{Asn: sweepPeerASN, RouterId: "192.0.2.2", ListenPort: 179, ListenAddresses: []string{sweepGateway}}
	if err := bgpServer.StartBgp(t.Context(), &apipb.StartBgpRequest{Global: global}); err != nil {
		t.Fatalf("start the peer: %v", err)
	}
	t.Cleanup(bgpServer.Stop)
	peer := &apipb.Peer{
		Conf:            &apipb.PeerConf{NeighborAddress: sweepLocalAddress, PeerAsn: sweepLocalASN},
		Transport:       &apipb.Transport{PassiveMode: true},
		Timers:          &apipb.Timers{Config: &apipb.TimersConfig{KeepaliveInterval: 1, HoldTime: 3}},
		GracefulRestart: &apipb.GracefulRestart{Enabled: true, RestartTime: sweepRestartTime},
		AfiSafis: []*apipb.AfiSafi{{
			Config:            &apipb.AfiSafiConfig{Family: &apipb.Family{Afi: apipb.Family_AFI_IP6, Safi: apipb.Family_SAFI_UNICAST}, Enabled: true},
			MpGracefulRestart: &apipb.MpGracefulRestart{Config: &apipb.MpGracefulRestartConfig{Enabled: true}},
		}},
	}
	if err := bgpServer.AddPeer(t.Context(), &apipb.AddPeerRequest{Peer: peer}); err != nil {
		t.Fatalf("add the gateway as a peer neighbor: %v", err)
	}
	link, err := netlink.LinkByName(sweepDevice)
	if err != nil {
		t.Fatal(err)
	}
	return link, bgpServer
}

func waitSweepPeerEstablished(t *testing.T, peer *server.BgpServer) {
	t.Helper()
	deadline := time.Now().Add(sweepWait)
	for time.Now().Before(deadline) {
		established := false
		err := peer.ListPeer(t.Context(), &apipb.ListPeerRequest{Address: sweepLocalAddress}, func(listed *apipb.Peer) {
			established = listed.GetState().GetSessionState() == apipb.PeerState_SESSION_STATE_ESTABLISHED
		})
		if err != nil {
			t.Fatalf("list the peer neighbors: %v", err)
		}
		if established {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the gateway session did not establish within %s", sweepWait)
}
