//go:build linux && firewallnetns

package main

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"golang.org/x/net/ipv6"
)

type dhcpv6TimerMode int

const (
	dhcpv6TimerLateReply dhcpv6TimerMode = iota
	dhcpv6TimerSilentUntilExpiry
)

// dhcpv6TimerObservation records one client message as the test server saw it.
type dhcpv6TimerObservation struct {
	messageType   dhcpv6.MessageType
	transactionID dhcpv6.TransactionID
	elapsed       time.Duration
	received      time.Time
	answered      bool
	validUntil    time.Time
	replied       time.Time
}

// dhcpv6TimerLog stores the server observations and wakes a waiting test on each one.
type dhcpv6TimerLog struct {
	mu      sync.Mutex
	entries []dhcpv6TimerObservation
	changed chan struct{}
}

func (log *dhcpv6TimerLog) add(observation dhcpv6TimerObservation) int {
	log.mu.Lock()
	log.entries = append(log.entries, observation)
	index := len(log.entries) - 1
	log.mu.Unlock()
	log.notify()
	return index
}

// markReplied stores the time the server wrote the reply to one observed message.
func (log *dhcpv6TimerLog) markReplied(index int, replied time.Time) {
	log.mu.Lock()
	log.entries[index].replied = replied
	log.mu.Unlock()
	log.notify()
}

func (log *dhcpv6TimerLog) notify() {
	select {
	case log.changed <- struct{}{}:
	default:
	}
}

func (log *dhcpv6TimerLog) snapshot() []dhcpv6TimerObservation {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]dhcpv6TimerObservation(nil), log.entries...)
}

func (log *dhcpv6TimerLog) wait(t *testing.T, timeout time.Duration, match func(dhcpv6TimerObservation) bool) dhcpv6TimerObservation {
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
			t.Fatalf("expected DHCPv6 message did not arrive within %s: messages=%v", timeout, log.snapshot())
		}
	}
}

// startDHCPv6TimerServer answers Solicit and Request at once. In late-reply mode it
// answers Renew and Rebind after dhcpv6TimerReplyDelay. In silent mode it ignores
// Renew, Rebind, and the first Solicit after the lease expires.
func startDHCPv6TimerServer(t *testing.T, mode dhcpv6TimerMode) *dhcpv6TimerLog {
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
	if err := ipv6.NewPacketConn(listener).JoinGroup(link, &net.UDPAddr{IP: net.ParseIP("ff02::1:2")}); err != nil {
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
	observations := &dhcpv6TimerLog{mu: sync.Mutex{}, entries: nil, changed: make(chan struct{}, 1)}
	go func() {
		var expiry time.Time
		ignoredAfterExpiry := false
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
				messageType: request.MessageType, transactionID: request.TransactionID,
				elapsed: dhcpv6TimerElapsed(request), received: received, answered: false, validUntil: time.Time{},
			}
			delay := time.Duration(0)
			switch request.MessageType {
			case dhcpv6.MessageTypeSolicit:
				if mode == dhcpv6TimerSilentUntilExpiry && !expiry.IsZero() && received.After(expiry) && !ignoredAfterExpiry {
					ignoredAfterExpiry = true
					observations.add(observation)
					continue
				}
			case dhcpv6.MessageTypeRequest:
				expiry = received.Add(dhcpv6TimerValid)
				ignoredAfterExpiry = false
			case dhcpv6.MessageTypeRenew, dhcpv6.MessageTypeRebind:
				if mode == dhcpv6TimerSilentUntilExpiry {
					observations.add(observation)
					continue
				}
				delay = dhcpv6TimerReplyDelay
			default:
				continue
			}
			response := dhcpv6TimerResponse(request, serverID, prefix)
			observation.answered = true
			observation.validUntil = received.Add(delay + dhcpv6TimerValid)
			index := observations.add(observation)
			time.AfterFunc(delay, func() {
				if _, err := listener.WriteTo(response.ToBytes(), peer); err == nil {
					observations.markReplied(index, time.Now())
				}
			})
		}
	}()
	return observations
}

func dhcpv6TimerResponse(request *dhcpv6.Message, serverID dhcpv6.DUID, prefix *net.IPNet) *dhcpv6.Message {
	response := &dhcpv6.Message{MessageType: dhcpv6.MessageTypeReply, TransactionID: request.TransactionID}
	if request.MessageType == dhcpv6.MessageTypeSolicit {
		response.MessageType = dhcpv6.MessageTypeAdvertise
	}
	dhcpv6.WithClientID(request.Options.ClientID())(response)
	dhcpv6.WithServerID(serverID)(response)
	prefixes := dhcpv6.Options{}
	prefixes.Add(&dhcpv6.OptIAPrefix{Prefix: prefix, PreferredLifetime: dhcpv6TimerPreferred, ValidLifetime: dhcpv6TimerValid})
	response.AddOption(&dhcpv6.OptIAPD{IaId: request.Options.IAPD()[0].IaId, T1: dhcpv6TimerT1, T2: dhcpv6TimerT2, Options: dhcpv6.PDOptions{Options: prefixes}})
	return response
}

func dhcpv6TimerElapsed(message *dhcpv6.Message) time.Duration {
	option := message.GetOneOption(dhcpv6.OptionElapsedTime)
	if option == nil || len(option.ToBytes()) != 2 {
		return -1
	}
	return time.Duration(binary.BigEndian.Uint16(option.ToBytes())) * 10 * time.Millisecond
}
