//go:build linux && ovs

package commandline_test

import (
	"net/netip"
	"testing"

	"github.com/gopacket/gopacket/layers"
)

func checkIncomingOVSMemory(
	t *testing.T,
	bridge string,
	lan int,
	wans [2]int,
) {
	t.Helper()
	probeTargets := [2]string{firstTarget, secondTarget}
	for _, tc := range []struct {
		name   string
		client string
		server string
		wan    int
	}{
		{"IPv4 request on WAN B", "203.0.113.33", "192.0.2.44:8053", 1},
		{"IPv6 request on WAN A", "2001:db8:3::33", "[2001:db8:4::44]:8053", 0},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) {
				client := netip.MustParseAddr(tc.client)
				server := netip.MustParseAddrPort(tc.server)
				family := layers.EthernetTypeIPv6
				if client.Is4() {
					family = layers.EthernetTypeIPv4
				}
				checkPacket := func(sender, receiver int, tuple packetTuple, label string) {
					t.Helper()
					if got := sendReceiveUDP(t, sender, receiver, tuple, label); got != tuple {
						t.Fatalf("%s: received %v, want %v", label, got, tuple)
					}
				}
				request := func(port uint16, label string) packetTuple {
					t.Helper()
					tuple := packetTuple{netip.AddrPortFrom(client, port), server}
					checkPacket(wans[tc.wan], lan, tuple, label)
					return tuple.reply()
				}
				reply := func(selected int, tuple packetTuple, label string) {
					t.Helper()
					unselected := ovsPacketSocket(t, wans[1-selected], uint16(family))
					checkPacket(lan, wans[selected], tuple, label)
					if ovsPacketWithMarker(t, unselected, []byte(label), ovsPacketRetry) != nil {
						t.Fatalf("%s also appeared on the other WAN", label)
					}
				}

				// Unmarked replies use the other WAN in this configuration. A reply
				// on the incoming WAN therefore demonstrates a saved connection mark.
				other := 1 - tc.wan
				before := request(34001, "incoming before outage")
				reply(other, before, "reply before outage")

				changeOVSBlockedProbe(t, "add", probeTargets[other])
				remembered := func() packetTuple {
					defer changeOVSBlockedProbe(t, "delete", probeTargets[other])
					var active [2]bool
					active[tc.wan] = true
					waitForOVSHealth(t, bridge, active)
					tuple := request(34002, "incoming during outage")
					reply(tc.wan, tuple, "reply during outage")
					return tuple
				}()
				waitForOVSHealth(t, bridge, [2]bool{true, true})
				reply(tc.wan, remembered, "reply after recovery")
				fresh := request(34003, "incoming after recovery")
				reply(other, fresh, "new reply after recovery")
			},
		)
	}
}
