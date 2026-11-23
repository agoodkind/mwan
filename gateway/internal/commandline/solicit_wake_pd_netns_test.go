//go:build linux && protocol

package commandline

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/mdlayher/ndp"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const (
	dhcpv6WakeSolicitMaxRT  = 60
	dhcpv6WakeSolicitBound  = 5 * time.Second
	dhcpv6WakeSolicitWithin = 3 * time.Minute
)

func TestOwnedDHCPv6PDSolicitWakeRuntime(t *testing.T) {
	if os.Getenv(dhcpv6RuntimeChildEnv) == "1" {
		runOwnedDHCPv6PDSolicitWakeRuntime(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), dhcpv6RuntimeChildEnv+"=1", dhcpv6RuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DHCPv6 Solicit wake daemon test: %v: %s", err, output)
	}
}

// A managed router advertisement restarts Solicit acquisition after the lease expires.
func runOwnedDHCPv6PDSolicitWakeRuntime(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	topology := buildDHCPv6PDRuntime(t, "solicit")
	answering := &atomic.Bool{}
	observations := startDHCPv6WakeServer(t, answering)
	setRuntimeNamespace(t, topology.gateway)
	daemon := startRuntimeDaemon(
		t,
		os.Getenv(dhcpv6RuntimeBinaryEnv),
		topology.configPath,
		topology.root,
		"dhcpv6pd-solicit-wake",
	)
	defer stopRuntimeDaemon(t, daemon)
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpv6TimerAddress, true)
	request := observations.wait(
		t,
		dhcpv6TimerValid,
		func(observation dhcpv6WakeObservation) bool {
			return observation.messageType == dhcpv6.MessageTypeRequest && observation.answered
		},
	)
	observations.wait(
		t,
		time.Until(request.validUntil)+dhcpv6WakeSolicitWithin,
		func(observation dhcpv6WakeObservation) bool {
			return observation.messageType == dhcpv6.MessageTypeSolicit &&
				observation.received.After(request.validUntil)
		},
	)
	answering.Store(true)
	setRuntimeNamespace(t, topology.provider)
	advertised := sendDHCPv6WakeAdvertisement(t)
	setRuntimeNamespace(t, topology.gateway)
	solicit := observations.wait(
		t,
		dhcpv6WakeSolicitBound,
		func(observation dhcpv6WakeObservation) bool {
			return observation.messageType == dhcpv6.MessageTypeSolicit &&
				observation.received.After(advertised)
		},
	)
	t.Logf(
		"Solicit arrived %s after the managed router advertisement",
		solicit.received.Sub(advertised),
	)
	observations.wait(
		t,
		dhcpv6WakeSolicitBound,
		func(observation dhcpv6WakeObservation) bool {
			return observation.messageType == dhcpv6.MessageTypeRequest && observation.answered &&
				observation.received.After(advertised)
		},
	)
	waitDHCPv6RuntimeAddress(t, daemon, dhcpv6TimerAddress)
}

type dhcpv6WakeObservation struct {
	dhcpv6TimerObservation
}

type dhcpv6WakeLog struct {
	mu      sync.Mutex
	entries []dhcpv6WakeObservation
	changed chan struct{}
}

func (log *dhcpv6WakeLog) add(observation dhcpv6WakeObservation) {
	log.mu.Lock()
	log.entries = append(log.entries, observation)
	log.mu.Unlock()
	select {
	case log.changed <- struct{}{}:
	default:
	}
}

func (log *dhcpv6WakeLog) snapshot() []dhcpv6WakeObservation {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]dhcpv6WakeObservation(nil), log.entries...)
}

func (log *dhcpv6WakeLog) wait(
	t *testing.T,
	timeout time.Duration,
	match func(dhcpv6WakeObservation) bool,
) dhcpv6WakeObservation {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		for _, observation := range log.snapshot() {
			if match(observation) {
				return observation
			}
		}
		select {
		case <-log.changed:
		case <-timer.C:
			t.Fatalf(
				"expected DHCPv6 message did not arrive within %s: messages=%v",
				timeout,
				log.snapshot(),
			)
		}
	}
}

// Answer the first Solicit with SOL_MAX_RT set to 60 seconds. Ignore later Solicit messages
// until the test reenables replies.
func startDHCPv6WakeServer(t *testing.T, answering *atomic.Bool) *dhcpv6WakeLog {
	t.Helper()
	listener, err := net.ListenPacket("udp6", "[::]:547")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	link, err := net.InterfaceByName("wan-vlan")
	if err != nil {
		t.Fatal(err)
	}
	if err := ipv6.NewPacketConn(listener).JoinGroup(
		link,
		&net.UDPAddr{IP: net.ParseIP("ff02::1:2")},
	); err != nil {
		t.Fatal(err)
	}
	serverID, err := dhcpv6.DUIDFromBytes([]byte{0, 3, 0, 1, 2, 0, 94, 0, 83, 2})
	if err != nil {
		t.Fatal(err)
	}
	_, prefix, err := net.ParseCIDR(dhcpv6TimerPrefix)
	if err != nil {
		t.Fatal(err)
	}
	observations := &dhcpv6WakeLog{mu: sync.Mutex{}, entries: nil, changed: make(chan struct{}, 1)}
	go func() {
		leased := false
		buffer := make([]byte, 1500)
		for {
			count, peer, err := listener.ReadFrom(buffer)
			if err != nil {
				return
			}
			request, err := dhcpv6.MessageFromBytes(buffer[:count])
			if err != nil || request.Options.ClientID() == nil || len(request.Options.IAPD()) != 1 {
				continue
			}
			received := time.Now()
			observation := dhcpv6WakeObservation{
				dhcpv6TimerObservation: dhcpv6TimerObservation{
					messageType:   request.MessageType,
					transactionID: request.TransactionID,
					elapsed:       dhcpv6TimerElapsed(request),
					received:      received,
					answered:      false,
					validUntil:    time.Time{},
				},
			}
			switch request.MessageType {
			case dhcpv6.MessageTypeSolicit:
				if leased && !answering.Load() {
					observations.add(observation)
					continue
				}
			case dhcpv6.MessageTypeRequest:
				leased = true
			case dhcpv6.MessageTypeRenew, dhcpv6.MessageTypeRebind:
				observations.add(observation)
				continue
			default:
				continue
			}
			response := dhcpv6TimerResponse(request, serverID, prefix)
			response.AddOption(&dhcpv6.OptionGeneric{
				OptionCode: dhcpv6.OptionSolMaxRT,
				OptionData: []byte{0, 0, 0, dhcpv6WakeSolicitMaxRT},
			})
			observation.answered = true
			observation.validUntil = received.Add(dhcpv6TimerValid)
			observations.add(observation)
			_, _ = listener.WriteTo(response.ToBytes(), peer)
		}
	}()
	return observations
}

func sendDHCPv6WakeAdvertisement(t *testing.T) time.Time {
	t.Helper()
	link, err := net.InterfaceByName("wan-vlan")
	if err != nil {
		t.Fatal(err)
	}
	connection, _, err := ndp.Listen(link, ndp.LinkLocal)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	advertisement := &ndp.RouterAdvertisement{
		RouterLifetime:       30 * time.Minute,
		ManagedConfiguration: true,
	}
	sent := time.Now()
	if err := connection.WriteTo(advertisement, nil, netip.MustParseAddr("ff02::1")); err != nil {
		t.Fatal(err)
	}
	return sent
}
