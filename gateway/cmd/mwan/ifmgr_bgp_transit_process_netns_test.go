//go:build linux && firewallnetns

package main

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

const bgpUpstreamProcessEnv = "MWAN_BGP_UPSTREAM_ROUTER_PROCESS"

func TestBGPUpstreamRouterProcess(t *testing.T) {
	if os.Getenv(bgpUpstreamProcessEnv) != "1" {
		t.Skip("runs only as the upstream router process of TestBGPTunnelVPSPeerDaemonRuntime")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	namespace, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = namespace.Close() }()
	address := netip.MustParseAddr(bgpUpstreamAddress)
	router := startBGPTransitRouter(t, namespace, "198.51.100.9", bgpUpstreamASN, []netip.Addr{address},
		bgpTransitNeighbor{address: netip.MustParseAddr(bgpUpstreamVPSAddress), asn: bgpRuntimeRemoteASN, active: true})
	router.announce(t, netip.MustParsePrefix("::/0"), address)
	router.announce(t, netip.MustParsePrefix(bgpRuntimeUndeclared), address)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

// The router with the active side of a session runs in a separate process
// because GoBGP dials on a goroutine without a thread in the router namespace.
func startBGPUpstreamProcess(t *testing.T, upstream, gateway netns.NsHandle) {
	t.Helper()
	setRuntimeNamespace(t, upstream)
	defer setRuntimeNamespace(t, gateway)
	child := exec.Command(os.Args[0], "-test.run=^TestBGPUpstreamRouterProcess$", "-test.v")
	child.Env = append(os.Environ(), bgpUpstreamProcessEnv+"=1")
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
