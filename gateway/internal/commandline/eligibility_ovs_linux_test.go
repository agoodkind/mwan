//go:build linux && ovs

package commandline_test

import (
	"bytes"
	"fmt"
	"math/bits"
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
	firstTarget  = "198.51.100.1"
	secondTarget = "203.0.113.1"
)

func prepareOVSHealthNetwork(t *testing.T) (int, [2]int, string) {
	t.Helper()
	input := ovsPacketLink(t, "hc-in", "hc-client")
	outputs := [2]int{ovsPacketLink(t, "hc-a", "hc-peer-a"), ovsPacketLink(t, "hc-b", "hc-peer-b")}
	addOVSProbeAddress(t, "hc-a", firstTarget)
	addOVSProbeAddress(t, "hc-b", secondTarget)
	ovsProbeFilter(t, "-f", "testdata/ovs-health.nft")
	t.Cleanup(func() {
		if output, err := exec.Command(
			"nft",
			"delete",
			"table",
			"ip",
			"ovs_health_test",
		).CombinedOutput(); err != nil {
			t.Errorf("remove probe filter: %v: %s", err, output)
		}
	})
	document, err := os.ReadFile("testdata/ovs-health.json")
	if err != nil {
		t.Fatal(err)
	}
	return input, outputs, string(document)
}

func TestOVSHealthConditions(t *testing.T) {
	socket := os.Getenv("MWAN_TEST_OVS_SOCKET")
	if socket == "" {
		t.Fatal("run make test-ovs to start a disposable OVS instance")
	}
	input, outputs, document := prepareOVSHealthNetwork(t)
	dir := filepath.Dir(socket)
	config := strings.ReplaceAll(document, "/run/openvswitch/", dir+"/")
	config = strings.Replace(config, filepath.Join(dir, "db.sock"), socket, 1)
	path := filepath.Join(t.TempDir(), "network.json")
	writeOVSPacketDocument(t, path, config)
	service := startOVSPacketService(t, path, "hc-in", time.Hour)
	bridge := "unix:" + filepath.Join(dir, "br-hc.mgmt")
	t.Run(
		"incoming connection memory",
		func(t *testing.T) {
			waitForOVSHealth(t, bridge, [2]bool{true, true})
			checkIncomingOVSMemory(t, bridge, input, outputs)
		},
	)
	scenarios := []struct {
		name   string
		change string
		target string
		active [2]bool
	}{
		{"both healthy", "", "", [2]bool{true, true}},
		{"first failed", "add", firstTarget, [2]bool{false, true}},
		{"both failed", "add", secondTarget, [2]bool{false, false}},
		{"first recovered", "delete", firstTarget, [2]bool{true, false}},
		{"both recovered", "delete", secondTarget, [2]bool{true, true}},
	}
	for index, scenario := range scenarios {
		if scenario.change != "" {
			changeOVSBlockedProbe(t, scenario.change, scenario.target)
		}
		waitForOVSHealth(t, bridge, scenario.active)
		firstPort := layers.UDPPort(20000 + 256*index)
		checkOVSHealthPackets(
			t,
			input,
			outputs,
			layers.EthernetTypeIPv4,
			firstPort,
			scenario.active,
			scenario.name,
		)
		checkOVSHealthPackets(
			t,
			input,
			outputs,
			layers.EthernetTypeIPv6,
			firstPort,
			scenario.active,
			scenario.name,
		)
	}

	changeOVSBlockedProbe(t, "add", secondTarget)
	waitForOVSHealth(t, bridge, [2]bool{true, false})
	stream := startOVSRestartTraffic(t, input, outputs[0])
	started := stream.sample(t)
	service.stop()
	stoppedAt := stream.received.Load()
	whileStopped := stream.sample(t) - stoppedAt
	changeOVSBlockedProbe(t, "add", firstTarget)
	config = strings.ReplaceAll(config, `"failure-threshold": 1`, `"failure-threshold": 6`)
	config = strings.Replace(
		config,
		`"flow": [`,
		`"flow": [
          {"name":"restart-witness","priority":321,
           "match":[{"field":"eth_type","value":"34997"}],
           "action":[{"id":1,"name":"output","argument":"hc-b"}]},`,
		1,
	)
	writeOVSPacketDocument(t, path, config)
	launchOVSPacketService(t, path, service.config)
	waitForOVSRestartFlow(t, bridge)
	statePath := filepath.Join(filepath.Dir(service.config), "health.state")
	checkOVSProbeState(t, statePath, "unknown", "unknown")
	waitForOVSHealth(t, bridge, [2]bool{true, false})
	unknownAt := stream.received.Load()
	startedUnknown := time.Now()
	whileUnknown := stream.sample(t) - unknownAt
	checkOVSProbeState(t, statePath, "unknown", "unknown")
	unknownDuration := time.Since(startedUnknown)
	changeOVSBlockedProbe(t, "delete", firstTarget)
	checkOVSProbeState(t, statePath, "healthy", "unhealthy")
	waitForOVSHealth(t, bridge, [2]bool{true, false})
	stream.stop()
	t.Logf(
		"restart: sent %d IPv6 packets, received %d on hc-a, %d before shutdown, %d while stopped, %d in %s of confirmed unknown health",
		stream.sent,
		stream.received.Load(),
		started,
		whileStopped,
		whileUnknown,
		unknownDuration,
	)
}

func ovsProbeFilter(t *testing.T, args ...string) {
	t.Helper()
	if output, err := exec.CommandContext(
		t.Context(),
		"nft",
		args...,
	).CombinedOutput(); err != nil {
		t.Fatalf("nft %v: %v: %s", args, err, output)
	}
}

func changeOVSBlockedProbe(t *testing.T, change, target string) {
	t.Helper()
	ovsProbeFilter(t, change, "element", "ip", "ovs_health_test", "blocked", "{", target, "}")
}

func addOVSProbeAddress(t *testing.T, name, ip string) {
	t.Helper()
	device, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(ip + "/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(device, address); err != nil {
		t.Fatal(err)
	}
}

func waitForOVSHealth(t *testing.T, bridge string, active [2]bool) {
	t.Helper()
	query := func(args ...string) (string, error) {
		output, err := exec.CommandContext(
			t.Context(),
			"ovs-ofctl",
			append([]string{"-O", "OpenFlow15"}, args...)...,
		).CombinedOutput()
		return string(output), err
	}
	var groups, flows string
	deadline := time.Now().Add(ovsReconcileWait)
	for time.Now().Before(deadline) {
		var err error
		groups, err = query("dump-groups", bridge)
		if err == nil && strings.Contains(groups, "type=select") &&
			strings.Contains(groups, "bucket_id:1,") == active[0] &&
			strings.Contains(groups, "bucket_id:2,") == active[1] {
			flows, err = query("dump-flows", bridge, "ipv6")
			if err == nil && strings.Contains(flows, "priority=100,") == active[0] &&
				strings.Contains(flows, "priority=90,") == !active[0] {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("OVS did not apply health %v: groups=%s flows=%s", active, groups, flows)
}

func checkOVSHealthPackets(
	t *testing.T,
	input int,
	outputs [2]int,
	family layers.EthernetType,
	firstPort layers.UDPPort,
	active [2]bool,
	label string,
) {
	t.Helper()
	const count = 64
	var receivers []unix.PollFd
	for _, output := range outputs {
		socket := ovsPacketSocket(t, output, uint16(family))
		receivers = append(receivers, unix.PollFd{Fd: int32(socket), Events: unix.POLLIN})
	}
	sender := ovsPacketSocket(t, input, uint16(family))
	destination := &unix.SockaddrLinklayer{
		Ifindex:  input,
		Protocol: bits.ReverseBytes16(uint16(family)),
	}
	marker := gopacket.Payload(fmt.Sprintf("mwan: %s %s", family, label))
	for index := range count {
		frame := ovsIPFrame(t, family, firstPort+layers.UDPPort(index), marker)
		if err := unix.Sendto(sender, frame, 0, destination); err != nil {
			t.Fatal(err)
		}
	}

	var seen [count]bool
	var received [2]int
	var frame [1500]byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ready := pollOVSPackets(t, receivers, 200)
		if ready == 0 {
			if (!active[0] && !active[1]) || received[0]+received[1] == count {
				break
			}
			continue
		}
		for index, socket := range receivers {
			if socket.Revents&unix.POLLIN == 0 {
				continue
			}
			length, _, err := unix.Recvfrom(int(socket.Fd), frame[:], 0)
			if err != nil {
				t.Fatal(err)
			}
			packet := gopacket.NewPacket(frame[:length], layers.LayerTypeEthernet, gopacket.Default)
			udp, ok := packet.TransportLayer().(*layers.UDP)
			if !ok || !bytes.Equal(udp.Payload, marker) {
				continue
			}
			sequence := int(udp.SrcPort) - int(firstPort)
			if sequence < 0 || sequence >= count || seen[sequence] {
				t.Fatalf(
					"%s %s received duplicate or unexpected flow on UDP source port %d",
					label,
					family,
					udp.SrcPort,
				)
			}
			seen[sequence] = true
			received[index]++
		}
	}
	for index, enabled := range active {
		if (received[index] > 0) != enabled {
			t.Fatalf("%s %s: active outputs %v, packet counts %v", label, family, active, received)
		}
	}
	if (active[0] || active[1]) && received[0]+received[1] != count {
		t.Fatalf("%s %s forwarded %v of %d flows", label, family, received, count)
	}
	t.Logf(
		"%s %s: sent %d flows, received %d on hc-a and %d on hc-b",
		label,
		family,
		count,
		received[0],
		received[1],
	)
}
