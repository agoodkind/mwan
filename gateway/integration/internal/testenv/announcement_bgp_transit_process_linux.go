//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"bytes"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/vishvananda/netns"
)

const BgpUpstreamProcessEnv = "MWAN_BGP_UPSTREAM_ROUTER_PROCESS"

func TestBGPUpstreamRouterProcess(t *testing.T) {
	if os.Getenv(BgpUpstreamProcessEnv) != "1" {
		t.Skip("runs only as the upstream router process of TestBGPTunnelVPSPeerDaemonRuntime")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	namespace, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = namespace.Close() }()
	address := netip.MustParseAddr(BgpUpstreamAddress)
	router := StartBGPTransitRouter(
		t,
		namespace,
		"198.51.100.9",
		BgpUpstreamASN,
		[]netip.Addr{address},
		BgpTransitNeighbor{
			Address: netip.MustParseAddr(BgpUpstreamVPSAddress),
			Asn:     BgpRuntimeRemoteASN,
			Active:  true,
		},
	)
	router.Announce(t, netip.MustParsePrefix("::/0"), address)
	router.Announce(t, netip.MustParsePrefix(BgpRuntimeUndeclared), address)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

// Run the active peer in its own process. The GoBGP dial goroutine does not inherit the
// test thread's namespace.
func StartBGPUpstreamProcess(t *testing.T, upstream, gateway netns.NsHandle) {
	t.Helper()
	SetRuntimeNamespace(t, upstream)
	defer SetRuntimeNamespace(t, gateway)
	child := exec.Command(os.Args[0], "-test.run=^TestBGPUpstreamRouterProcess$", "-test.v")
	child.Env = append(os.Environ(), BgpUpstreamProcessEnv+"=1")
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatalf("start the upstream router process: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := child.Wait(); err != nil || t.Failed() {
			t.Logf("upstream router process: %v: %s", err, output.String())
		}
	})
}
