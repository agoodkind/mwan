//go:build linux && ovs

package commandline_test

// TestOVSHealthConditions uses these helpers to check packet delivery while
// mwan stops and starts against a running software switch. Each UDP source port
// identifies one IPv6 packet. Packet capture checks for duplicates and counts
// lost packets. A new switch rule identifies the restarted process, and
// health.state shows when probe results are still unknown.

import (
	"bytes"
	"context"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"golang.org/x/sys/unix"
)

func waitForOVSRestartFlow(t *testing.T, bridge string) {
	t.Helper()
	deadline := time.Now().Add(ovsReconcileWait)
	var output []byte
	for time.Now().Before(deadline) {
		var err error
		output, err = exec.CommandContext(
			t.Context(),
			"ovs-ofctl",
			"-O",
			"OpenFlow15",
			"dump-flows",
			bridge,
			"dl_type=0x88b5",
		).CombinedOutput()
		if err != nil {
			t.Fatalf("read restart flow: %v: %s", err, output)
		}
		if bytes.Contains(output, []byte("priority=321,")) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("restarted service did not install priority 321: %s", output)
}

func checkOVSProbeState(t *testing.T, path, first, second string) {
	t.Helper()
	deadline := time.Now().Add(ovsReconcileWait)
	var contents []byte
	for time.Now().Before(deadline) {
		var err error
		contents, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), "wan-a:"+first+"\n") &&
			strings.Contains(string(contents), "wan-b:"+second+"\n") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wanted wan-a:%s wan-b:%s, read %s", first, second, contents)
}

type ovsRestartTraffic struct {
	sent     int
	received atomic.Int32
	stop     func()
}

func (stream *ovsRestartTraffic) sample(t *testing.T) int32 {
	t.Helper()
	start := stream.received.Load()
	deadline := time.Now().Add(ovsReconcileWait)
	for time.Now().Before(deadline) {
		if count := stream.received.Load(); count >= start+10 {
			return count
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("IPv6 output stopped at %d packets", stream.received.Load())
	return 0
}

func startOVSRestartTraffic(t *testing.T, input, selected int) *ovsRestartTraffic {
	t.Helper()
	const family = layers.EthernetTypeIPv6
	const firstPort = 30000
	const marker = "mwan continuous restart traffic"
	const capacity = 4096
	var packets [capacity][]byte
	for index := range packets {
		packets[index] = ovsIPFrame(
			t,
			family,
			layers.UDPPort(firstPort+index),
			gopacket.Payload(marker),
		)
	}
	sender := ovsPacketSocket(t, input, uint16(family))
	receiver := ovsPacketSocket(t, selected, uint16(family))
	address := &unix.SockaddrLinklayer{
		Ifindex:  input,
		Protocol: bits.ReverseBytes16(uint16(family)),
	}
	stream := &ovsRestartTraffic{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		var seen [capacity]bool
		var buffer [1500]byte
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		var drainUntil time.Time
		for range ticker.C {
			if ctx.Err() == nil {
				if stream.sent == len(packets) {
					done <- fmt.Errorf("restart exceeded %d packets", stream.sent)
					return
				}
				if err := unix.Sendto(sender, packets[stream.sent], 0, address); err != nil {
					done <- err
					return
				}
				stream.sent++
			} else if drainUntil.IsZero() {
				drainUntil = time.Now().Add(300 * time.Millisecond)
			}
			for {
				length, _, err := unix.Recvfrom(receiver, buffer[:], 0)
				if err == unix.EAGAIN {
					break
				}
				if err != nil {
					done <- err
					return
				}
				packet := gopacket.NewPacket(
					buffer[:length],
					layers.LayerTypeEthernet,
					gopacket.Default,
				)
				udp, ok := packet.TransportLayer().(*layers.UDP)
				if !ok || !bytes.Equal(udp.Payload, []byte(marker)) {
					continue
				}
				index := int(udp.SrcPort) - firstPort
				if index < 0 || index >= stream.sent || seen[index] {
					done <- fmt.Errorf("unexpected IPv6 packet on source port %d", udp.SrcPort)
					return
				}
				seen[index] = true
				stream.received.Add(1)
			}
			if !drainUntil.IsZero() && time.Now().After(drainUntil) {
				done <- nil
				return
			}
		}
	}()
	stream.stop = sync.OnceFunc(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		if int(stream.received.Load()) != stream.sent {
			t.Errorf(
				"restart forwarded %d of %d IPv6 packets on hc-a",
				stream.received.Load(),
				stream.sent,
			)
		}
	})
	t.Cleanup(stream.stop)
	return stream
}
