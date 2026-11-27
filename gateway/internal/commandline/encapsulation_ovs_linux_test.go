//go:build linux && ovs

package commandline_test

// Two Linux network namespaces provide the endpoints for a software VXLAN test.
//
//	tn-client <-> br-tun-tests <-> tn-underlay <-> vx73
//
// An IPv4 echo checks both directions of the VNI 73 tunnel. An IPv6 echo
// checks that the remote endpoint can use the translated LAN address.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOVSReadableTunnel(t *testing.T) {
	socket := os.Getenv("MWAN_TEST_OVS_SOCKET")
	if socket == "" {
		t.Fatal("run make test-ovs to start a disposable OVS instance")
	}
	run := func(program string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), program, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", program, args, err, out)
		}
		return string(out)
	}
	for _, namespace := range []string{"tn-local", "tn-remote"} {
		run("ip", "netns", "add", namespace)
		t.Cleanup(func() {
			if out, err := exec.Command(
				"ip",
				"netns",
				"delete",
				namespace,
			).CombinedOutput(); err != nil {
				t.Errorf("remove %s: %v: %s", namespace, err, out)
			}
		})
	}
	ovsPacketLink(t, "tn-wire", "tn-endpoint")
	ovsPacketLink(t, "tn-in", "tn-client")
	run("ip", "link", "set", "tn-client", "netns", "tn-local")
	run("ip", "link", "set", "tn-endpoint", "netns", "tn-remote")

	// Only the underlay bridge uses ordinary Ethernet switching.
	// mwan installs the two rules on br-tun-tests from the network document.
	run(
		"ovs-vsctl",
		"add-br",
		"tn-underlay",
		"--",
		"set",
		"bridge",
		"tn-underlay",
		"datapath_type=netdev",
		"--",
		"add-port",
		"tn-underlay",
		"tn-wire",
	)
	t.Cleanup(func() {
		if out, err := exec.Command(
			"ovs-vsctl",
			"del-br",
			"tn-underlay",
		).CombinedOutput(); err != nil {
			t.Errorf("remove tunnel underlay: %v: %s", err, out)
		}
	})
	run("ip", "link", "set", "tn-underlay", "up")
	run("ip", "addr", "add", "192.0.2.1/30", "dev", "tn-underlay")
	run("ip", "-n", "tn-remote", "addr", "add", "192.0.2.2/30", "dev", "tn-endpoint")
	run("ip", "-n", "tn-remote", "link", "set", "tn-endpoint", "up")
	// IPv4 VXLAN permits the Linux endpoint to send a zero UDP checksum.
	// This mode exercises the return path without hardware checksum offload.
	run(
		"ip",
		"-n",
		"tn-remote",
		"link",
		"add",
		"vx73",
		"type",
		"vxlan",
		"id",
		"73",
		"local",
		"192.0.2.2",
		"remote",
		"192.0.2.1",
		"dev",
		"tn-endpoint",
		"dstport",
		"4789",
		"noudpcsum",
	)
	run("ip", "-n", "tn-remote", "addr", "add", "198.51.100.2/30", "dev", "vx73")
	run("ip", "-n", "tn-remote", "link", "set", "vx73", "up")
	run("ip", "-n", "tn-local", "addr", "add", "198.51.100.1/30", "dev", "tn-client")
	run("ip", "-n", "tn-local", "link", "set", "tn-client", "up")
	run("ovs-appctl", "ovs/route/add", "192.0.2.2/32", "tn-underlay")

	document, err := os.ReadFile("testdata/ovs-readable-tunnel.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(socket)
	config := strings.ReplaceAll(string(document), "/run/openvswitch/", dir+"/")
	path := filepath.Join(t.TempDir(), "network.json")
	writeOVSPacketDocument(
		t,
		path,
		strings.Replace(config, filepath.Join(dir, "db.sock"), socket, 1),
	)
	startOVSPacketService(t, path, "tn-in", time.Hour)

	// Each namespace owns a single overlay endpoint. Only VNI 73 can deliver
	// the request to vx73, and only the return rule can deliver the echo reply.
	result := run(
		"ip",
		"netns",
		"exec",
		"tn-local",
		"ping",
		"-n",
		"-w",
		"10",
		"-c",
		"5",
		"-p",
		"6d77616e",
		"198.51.100.2",
	)
	if !strings.Contains(result, "bytes from 198.51.100.2:") {
		t.Fatalf("missing echo reply from the remote VXLAN interface: %s", result)
	}

	const internal = "fd01:203:405:1::1234"
	const external = "2001:db8:1:d550::1234"
	const remote = "2001:db8:ffff::7"
	const localMAC = "02:00:00:00:73:01"
	const remoteMAC = "02:00:00:00:73:02"
	run("ip", "-n", "tn-local", "link", "set", "tn-client", "address", localMAC)
	run("ip", "-n", "tn-remote", "link", "set", "vx73", "address", remoteMAC)
	run("ip", "-n", "tn-local", "addr", "add", internal+"/128", "dev", "tn-client", "nodad")
	run("ip", "-n", "tn-remote", "addr", "add", remote+"/128", "dev", "vx73", "nodad")
	run("ip", "-n", "tn-local", "route", "add", remote+"/128", "dev", "tn-client", "src", internal)
	run("ip", "-n", "tn-remote", "route", "add", external+"/128", "dev", "vx73", "src", remote)
	run(
		"ip",
		"-n",
		"tn-local",
		"neigh",
		"replace",
		remote,
		"lladdr",
		remoteMAC,
		"nud",
		"permanent",
		"dev",
		"tn-client",
	)
	run(
		"ip",
		"-n",
		"tn-remote",
		"neigh",
		"replace",
		external,
		"lladdr",
		localMAC,
		"nud",
		"permanent",
		"dev",
		"vx73",
	)
	// Only the LAN endpoint owns the internal address. The remote endpoint
	// can send the echo and accept its reply only under the external address.
	result = run(
		"ip",
		"netns",
		"exec",
		"tn-remote",
		"ping",
		"-6",
		"-n",
		"-w",
		"3",
		"-c",
		"1",
		external,
	)
	if !strings.Contains(result, "bytes from "+external+":") {
		t.Fatalf("missing translated IPv6 reply over VXLAN: %s", result)
	}
}
