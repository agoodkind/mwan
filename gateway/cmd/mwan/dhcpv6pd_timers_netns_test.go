//go:build linux && firewallnetns

package main

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	dhcpv6TimerPrefix         = "2001:db8:30::/60"
	dhcpv6TimerAddress        = "2001:db8:30::1/128"
	dhcpv6TimerT1             = 4 * time.Second
	dhcpv6TimerT2             = 8 * time.Second
	dhcpv6TimerPreferred      = 12 * time.Second
	dhcpv6TimerValid          = 16 * time.Second
	dhcpv6TimerReplyDelay     = 3 * time.Second
	dhcpv6TimerWANTable       = 100
	dhcpv6TimerSolicitDelay   = time.Second
	dhcpv6TimerSolicitMaxRT   = 1100 * time.Millisecond
	dhcpv6TimerScheduleMargin = 500 * time.Millisecond
)

func TestOwnedDHCPv6PDLateRenewReplyRuntime(t *testing.T) {
	testOwnedDHCPv6PDTimerRuntime(t, dhcpv6TimerLateReply)
}

func TestOwnedDHCPv6PDSolicitAfterExpiryRuntime(t *testing.T) {
	testOwnedDHCPv6PDTimerRuntime(t, dhcpv6TimerSilentUntilExpiry)
}

func testOwnedDHCPv6PDTimerRuntime(t *testing.T, mode dhcpv6TimerMode) {
	if os.Getenv(dhcpv6RuntimeChildEnv) == "1" {
		runOwnedDHCPv6PDTimerRuntime(t, mode)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpv6RuntimeChildEnv+"=1", dhcpv6RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv6 timer daemon test: %v: %s", err, output)
	}
}

func runOwnedDHCPv6PDTimerRuntime(t *testing.T, mode dhcpv6TimerMode) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	topology := buildDHCPv6PDRuntime(t, "solicit")
	observations := startDHCPv6TimerServer(t, mode)
	setRuntimeNamespace(t, topology.gateway)
	daemon := startRuntimeDaemon(t, os.Getenv(dhcpv6RuntimeBinaryEnv), topology.configPath, topology.root, "dhcpv6pd-timers")
	defer stopRuntimeDaemon(t, daemon)
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpv6TimerAddress, true)
	setRuntimeNamespace(t, topology.provider)
	// The gateway removes its RA default route after a Neighbor Advertisement
	// without the Router flag. A forwarding provider link sets that flag.
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/wan-vlan/forwarding", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addMappedRuntimeRoute(t, dhcpv6TimerPrefix, "2001:db8:2::1", "wan-vlan")
	stopRouter := startDHCPv6RuntimeRouter(t, false)
	defer stopRouter()
	setRuntimeNamespace(t, topology.gateway)
	waitDHCPv6RuntimeDefault(t, daemon)
	waitDHCPv6TimerTableDefault(t, daemon)
	if mode == dhcpv6TimerLateReply {
		assertDHCPv6TimerLeaseHeld(t, daemon, observations)
		return
	}
	assertDHCPv6TimerSolicitAfterExpiry(t, daemon, observations)
}

// assertDHCPv6TimerLeaseHeld requires the address and the WAN table default for two
// valid lifetimes while every Renew or Rebind reply arrives late.
func assertDHCPv6TimerLeaseHeld(t *testing.T, daemon *runtimeDaemon, observations *dhcpv6TimerLog) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(2*dhcpv6TimerValid + dhcpv6TimerT1)
	for time.Now().Before(deadline) {
		if !dhcpv6TimerAddressReady() {
			t.Fatalf("delegated address %s disappeared %s after the lease check started: messages=%v daemon=%s", dhcpv6TimerAddress, time.Since(started), observations.snapshot(), runtimeLogTail(t, daemon, 40))
		}
		if !dhcpv6TimerTableDefault() {
			t.Fatalf("WAN table default disappeared %s after the lease check started: messages=%v daemon=%s", time.Since(started), observations.snapshot(), runtimeLogTail(t, daemon, 40))
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	lateReplies := 0
	solicits := 0
	for _, observation := range observations.snapshot() {
		if observation.received.Before(started) {
			continue
		}
		switch observation.messageType {
		case dhcpv6.MessageTypeRenew, dhcpv6.MessageTypeRebind:
			if observation.answered {
				lateReplies++
			}
		case dhcpv6.MessageTypeSolicit:
			solicits++
		}
	}
	if lateReplies < 2 || solicits != 0 {
		t.Fatalf("lease did not stay renewed through late replies: late_replies=%d solicits=%d messages=%v", lateReplies, solicits, observations.snapshot())
	}
}

// assertDHCPv6TimerSolicitAfterExpiry requires the first Solicit within SOL_MAX_DELAY of
// expiry and its retransmission within the first Solicit RT.
func assertDHCPv6TimerSolicitAfterExpiry(t *testing.T, daemon *runtimeDaemon, observations *dhcpv6TimerLog) {
	t.Helper()
	request := observations.wait(t, dhcpv6TimerValid, func(observation dhcpv6TimerObservation) bool {
		return observation.messageType == dhcpv6.MessageTypeRequest && observation.answered
	})
	expiry := request.validUntil
	first := observations.wait(t, time.Until(expiry)+dhcpv6TimerSolicitDelay+dhcpv6TimerScheduleMargin, func(observation dhcpv6TimerObservation) bool {
		return observation.messageType == dhcpv6.MessageTypeSolicit && observation.received.After(request.received)
	})
	if first.received.Before(expiry.Add(-dhcpv6TimerScheduleMargin)) {
		t.Fatalf("Solicit at %s preceded lease expiry at %s", first.received, expiry)
	}
	second := observations.wait(t, dhcpv6TimerSolicitMaxRT+dhcpv6TimerScheduleMargin, func(observation dhcpv6TimerObservation) bool {
		return observation.messageType == dhcpv6.MessageTypeSolicit && observation.received.After(first.received)
	})
	gap := second.received.Sub(first.received)
	if second.transactionID != first.transactionID || second.elapsed <= first.elapsed ||
		gap < dhcpv6TimerSolicitDelay-dhcpv6TimerScheduleMargin || gap > dhcpv6TimerSolicitMaxRT+dhcpv6TimerScheduleMargin {
		t.Fatalf("Solicit retransmission broke RFC 8415 timing: first=%+v second=%+v gap=%s", first, second, gap)
	}
	observations.wait(t, dhcpv6TimerSolicitMaxRT+dhcpv6TimerScheduleMargin, func(observation dhcpv6TimerObservation) bool {
		return observation.messageType == dhcpv6.MessageTypeRequest && observation.answered && observation.received.After(second.received)
	})
	waitDHCPv6RuntimeAddress(t, daemon, dhcpv6TimerAddress)
}

func waitDHCPv6TimerTableDefault(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if dhcpv6TimerTableDefault() {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Table: dhcpv6TimerWANTable}, netlink.RT_FILTER_TABLE)
	t.Fatalf("WAN table IPv6 default missing: routes=%v err=%v daemon=%s", routes, err, runtimeLogTail(t, daemon, 40))
}

func dhcpv6TimerTableDefault() bool {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Table: dhcpv6TimerWANTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return false
	}
	for _, route := range routes {
		if route.Dst == nil || route.Dst.String() == "::/0" {
			return true
		}
	}
	return false
}

func dhcpv6TimerAddressReady() bool {
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		return false
	}
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		return false
	}
	for _, address := range addresses {
		if address.IPNet.String() == dhcpv6TimerAddress && address.Flags&unix.IFA_F_TENTATIVE == 0 {
			return true
		}
	}
	return false
}
