//go:build linux && protocol

package commandline

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
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
	dhcpv6WakeSolicitMaxRT = 60
	dhcpv6WakeSolicitBound = 5 * time.Second
)

func TestOwnedDHCPv6PDSolicitWakeRuntime(t *testing.T) {
	if os.Getenv(dhcpv6RuntimeChildEnv) == "1" {
		runOwnedDHCPv6PDSolicitWakeRuntime(t)
		return
	}
	t.Parallel()
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

// A managed router advertisement starts a new Solicit exchange on the provider link.
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
	first := observations.wait(
		t,
		15*time.Second,
		func(observation dhcpv6TimerObservation) bool {
			return observation.messageType == dhcpv6.MessageTypeSolicit
		},
	)
	answering.Store(true)
	setRuntimeNamespace(t, topology.provider)
	advertised := sendDHCPv6WakeAdvertisement(t)
	setRuntimeNamespace(t, topology.gateway)
	solicit := observations.wait(
		t,
		dhcpv6WakeSolicitBound,
		func(observation dhcpv6TimerObservation) bool {
			return observation.messageType == dhcpv6.MessageTypeSolicit &&
				observation.transactionID != first.transactionID &&
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
		func(observation dhcpv6TimerObservation) bool {
			return observation.messageType == dhcpv6.MessageTypeRequest && observation.answered &&
				observation.received.After(advertised)
		},
	)
	waitDHCPv6RuntimeAddress(t, daemon, dhcpv6TimerAddress)
}

func startDHCPv6WakeServer(t *testing.T, answering *atomic.Bool) *dhcpv6TimerLog {
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
	observations := &dhcpv6TimerLog{changed: make(chan struct{}, 1)}
	go func() {
		var first *dhcpv6.TransactionID
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
			observation := dhcpv6TimerObservation{
				messageType:   request.MessageType,
				transactionID: request.TransactionID,
				elapsed:       dhcpv6TimerElapsed(request),
				received:      received,
				answered:      false,
				validUntil:    time.Time{},
			}
			switch request.MessageType {
			case dhcpv6.MessageTypeSolicit:
				if first == nil {
					first = &request.TransactionID
				}
				if request.TransactionID == *first || !answering.Load() {
					observations.add(observation)
					continue
				}
			case dhcpv6.MessageTypeRequest:
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
