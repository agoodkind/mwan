//go:build linux && ovs

package commandline_test

import (
	"bytes"
	"fmt"
	"math/bits"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	ovsPacketRetry   = 100
	ovsReconcileWait = 8 * time.Second
)

func TestOVSRuleUpdateMovesPackets(t *testing.T) {
	socket := os.Getenv("MWAN_TEST_OVS_SOCKET")
	if socket == "" {
		t.Fatal("run make test-ovs to start a disposable OVS instance")
	}

	input := ovsPacketLink(t, "mwan-of-in", "mwan-peer-in")
	first := ovsPacketLink(t, "mwan-of-a", "mwan-peer-a")
	second := ovsPacketLink(t, "mwan-of-b", "mwan-peer-b")
	document, err := os.ReadFile("../../networkload/testdata/ovs-openflow.json")
	if err != nil {
		t.Fatal(err)
	}

	configuration := strings.ReplaceAll(
		string(document),
		"/run/openvswitch/",
		filepath.Dir(socket)+"/",
	)
	configuration = strings.Replace(
		configuration,
		filepath.Join(filepath.Dir(socket), "db.sock"),
		socket,
		1,
	)
	path := filepath.Join(t.TempDir(), "network.json")
	writeOVSPacketDocument(t, path, configuration)
	logPath := startOVSPacketService(t, path)
	checkOVSIPPacket(t, input, first, second, layers.EthernetTypeIPv4, "initial IPv4")
	checkOVSIPPacket(t, input, second, first, layers.EthernetTypeIPv6, "initial IPv6")
	updated := strings.Replace(
		configuration,
		`"output": ["mwan-of-a"]`,
		`"output": ["mwan-of-b"]`,
		1,
	)
	updated = strings.Replace(
		updated,
		`"ether-type": 34525,
            "output": ["mwan-of-b"]`,
		`"ether-type": 34525,
            "output": ["mwan-of-a"]`,
		1,
	)
	writeOVSPacketDocument(t, path, updated)
	checkOVSIPPacket(t, input, second, first, layers.EthernetTypeIPv4, "updated IPv4")
	checkOVSIPPacket(t, input, first, second, layers.EthernetTypeIPv6, "updated IPv6")
	checkOVSFlowAge(t, socket)
	checkOVSHostFirewall(t)

	// OpenFlow rejects a GOTO_TABLE instruction targeting its current table.
	rejected := strings.Replace(
		configuration,
		`"flow": [`,
		`"flow": [{
		"name": "invalid-goto", "priority": 1, "raw-instruction": ["AAEACAAAAAA="]
	},`,
		1,
	)
	writeOVSPacketDocument(t, path, rejected)
	waitForOVSBundleRejection(t, logPath)
	checkOVSIPPacket(t, input, second, first, layers.EthernetTypeIPv4, "retained IPv4")
	checkOVSIPPacket(t, input, first, second, layers.EthernetTypeIPv6, "retained IPv6")
	writeOVSPacketDocument(t, path, configuration)
	checkOVSIPPacket(t, input, first, second, layers.EthernetTypeIPv4, "restored IPv4")
	checkOVSIPPacket(t, input, second, first, layers.EthernetTypeIPv6, "restored IPv6")
}

func checkOVSHostFirewall(t *testing.T) {
	t.Helper()
	output, err := exec.CommandContext(
		t.Context(),
		"nft",
		"-n",
		"list",
		"chain",
		"inet",
		"filter",
		"input",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("read host firewall: %v: %s", err, output)
	}

	if !strings.Contains(string(output), "policy drop") {
		t.Fatalf("host input chain lacks a drop policy: %s", output)
	}
	for rule := range strings.Lines(string(output)) {
		if strings.Contains(rule, `iifname "mwan-of-b"`) &&
			strings.Contains(rule, "tcp dport 22 accept") {
			return
		}
	}
	t.Fatalf("host firewall lacks its configured management rule: %s", output)
}

func checkOVSIPPacket(
	t *testing.T,
	input int,
	selected int,
	other int,
	family layers.EthernetType,
	label string,
) {
	t.Helper()
	receiver := ovsPacketSocket(t, selected, uint16(family))
	sender := ovsPacketSocket(t, input, uint16(family))
	address := &unix.SockaddrLinklayer{
		Ifindex:  input,
		Protocol: bits.ReverseBytes16(uint16(family)),
	}
	marker := gopacket.Payload("mwan OVS " + label)
	packet := ovsIPFrame(t, family, marker)
	deadline := time.Now().Add(ovsReconcileWait)
	for time.Now().Before(deadline) {
		if err := unix.Sendto(sender, packet, 0, address); err != nil {
			t.Fatal(err)
		}
		if ovsReceivedMarker(t, receiver, marker, ovsPacketRetry) {
			unselected := ovsPacketSocket(t, other, uint16(family))
			if err := unix.Sendto(sender, packet, 0, address); err != nil {
				t.Fatal(err)
			}
			if ovsReceivedMarker(t, unselected, marker, ovsPacketRetry) {
				t.Fatalf("%q packet arrived on both output ports", label)
			}
			return
		}
	}
	t.Fatalf("%q packet did not arrive on its configured output port", label)
}

func ovsReceivedMarker(t *testing.T, receiver int, marker []byte, timeout int) bool {
	t.Helper()
	receive := []unix.PollFd{{Fd: int32(receiver), Events: unix.POLLIN}}
	var frame [1500]byte
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for time.Now().Before(deadline) && pollOVSPackets(t, receive, timeout) != 0 {
		length, _, err := unix.Recvfrom(receiver, frame[:], 0)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(frame[:length], marker) {
			packet := gopacket.NewPacket(frame[:length], layers.LayerTypeEthernet, gopacket.Default)
			if err := packet.ErrorLayer(); err != nil {
				t.Fatalf("decode forwarded test packet: %v", err.Error())
			}
			if network, ok := packet.NetworkLayer().(*layers.IPv4); ok &&
				!network.SrcIP.Equal(net.IPv4(203, 0, 113, 45)) {
				t.Fatalf("OVS set IPv4 source to %s, want 203.0.113.45", network.SrcIP)
			}
			return true
		}
	}
	return false
}

func ovsIPFrame(t *testing.T, family layers.EthernetType, marker gopacket.Payload) []byte {
	t.Helper()
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{2, 0, 0, 0, 0, 2},
		DstMAC:       net.HardwareAddr{2, 0, 0, 0, 0, 1},
		EthernetType: family,
	}
	var ip interface {
		gopacket.NetworkLayer
		gopacket.SerializableLayer
	} = &layers.IPv4{
		Version:  4,
		TTL:      64,
		Protocol: layers.IPProtocolUDP,
		SrcIP:    net.IPv4(192, 0, 2, 2),
		DstIP:    net.IPv4(192, 0, 2, 128),
	}
	if family == layers.EthernetTypeIPv6 {
		ip = &layers.IPv6{
			Version:    6,
			HopLimit:   64,
			NextHeader: layers.IPProtocolUDP,
			SrcIP:      net.ParseIP("2001:db8:b01::10"),
			DstIP:      net.ParseIP("2001:db8:ffff::1"),
		}
	}
	udp := &layers.UDP{SrcPort: 4444, DstPort: 5555}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	packet := gopacket.NewSerializeBuffer()
	options := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(packet, options, ethernet, ip, udp, marker); err != nil {
		t.Fatal(err)
	}
	return packet.Bytes()
}

func waitForOVSBundleRejection(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(ovsReconcileWait)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("OpenFlow bundle")) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("mwan ifmgr did not report an OpenFlow bundle rejection")
}

func checkOVSFlowAge(t *testing.T, socket string) {
	t.Helper()
	bridge := "unix:" + filepath.Join(filepath.Dir(socket), "br-mwan-of.mgmt")
	age := func() time.Duration {
		output, err := exec.CommandContext(
			t.Context(),
			"ovs-ofctl",
			"-O",
			"OpenFlow15",
			"dump-flows",
			bridge,
			"ip",
		).CombinedOutput()
		if err != nil {
			t.Fatalf("read IPv4 flow: %v: %s", err, output)
		}
		_, value, _ := strings.Cut(string(output), "duration=")
		value, _, _ = strings.Cut(value, ",")
		duration, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("read IPv4 flow duration: %v: %s", err, output)
		}
		return duration
	}
	before := age()
	time.Sleep(500 * time.Millisecond)
	if elapsed := age() - before; elapsed < 300*time.Millisecond {
		t.Fatalf("reconciliation replaced an unchanged IPv4 flow: age increased by %v", elapsed)
	}
}

func writeOVSPacketDocument(t *testing.T, path, configuration string) {
	t.Helper()

	pending := path + ".new"
	if err := os.WriteFile(pending, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(pending, path); err != nil {
		t.Fatal(err)
	}
}

func startOVSPacketService(t *testing.T, networkPath string) string {
	t.Helper()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	configuration := fmt.Sprintf(
		`[ifmgr]
reconcile_interval = "100ms"
lease_directory = %q
[ifmgr.iface.mwan-of-in]
[ifmgr.modules.links]
state_file = %q
[ifmgr.modules.addresses]
state_file = %q
[ifmgr.modules.autoconfiguration]
state_file = %q
`,
		root,
		filepath.Join(root, "links.json"),
		filepath.Join(root, "addresses.json"),
		filepath.Join(root, "policy.json"),
	)
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}

	logs, err := os.CreateTemp(t.TempDir(), "ifmgr.log")
	if err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(
		t.Context(),
		testMwanBinary(t),
		"ifmgr",
		"--role",
		"wan",
		"--config",
		configPath,
		"--network",
		networkPath,
		"--schema-dir",
		"../../yangpub/schema",
	)
	command.Stdout, command.Stderr = logs, logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = command.Process.Signal(unix.SIGTERM)
		_ = command.Wait()
		_ = logs.Close()
		if t.Failed() {
			data, err := os.ReadFile(logs.Name())
			if err != nil {
				t.Error(err)
			} else {
				t.Logf("interface manager log:\n%s", data)
			}
		}
	})
	return logs.Name()
}

func ovsPacketLink(t *testing.T, iface, peerName string) int {
	t.Helper()

	link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: iface}, PeerName: peerName}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("create test port %s: %v", iface, err)
	}

	t.Cleanup(func() {
		if err := netlink.LinkDel(link); err != nil {
			t.Errorf("remove test port %s: %v", iface, err)
		}
	})

	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatal(err)
	}

	for _, device := range []netlink.Link{link, peer} {
		if err := netlink.LinkSetUp(device); err != nil {
			t.Fatal(err)
		}
	}

	return peer.Attrs().Index
}

func ovsPacketSocket(t *testing.T, interfaceIndex int, etherType uint16) int {
	t.Helper()

	protocol := bits.ReverseBytes16(etherType)
	socket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK, int(protocol))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unix.Close(socket); err != nil {
			t.Error(err)
		}
	})

	if err := unix.Bind(
		socket,
		&unix.SockaddrLinklayer{Ifindex: interfaceIndex, Protocol: protocol},
	); err != nil {
		t.Fatal(err)
	}

	return socket
}

func pollOVSPackets(t *testing.T, pending []unix.PollFd, timeout int) int {
	t.Helper()

	for {
		ready, err := unix.Poll(pending, timeout)
		switch err {
		case unix.EINTR:
			continue
		case nil:
			return ready
		default:
			t.Fatal(err)
		}
	}
}
