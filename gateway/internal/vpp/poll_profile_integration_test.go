//go:build linux && vpp_integration

package vpp_test

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	"go.fd.io/govpp"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/tapv2"
	"go.fd.io/govpp/core"
	"golang.org/x/sys/unix"

	vppclient "goodkind.io/mwan/internal/vpp"
	pollapi "goodkind.io/mwan/internal/vpp/pollapi/virtio_poll"
)

func verifyQueuePolling(t *testing.T, endpoints vppclient.Endpoints, counters map[string]vppclient.InterfaceCounters) {
	t.Helper()
	connection, err := govpp.Connect(endpoints.APISocket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	if err := connection.CheckCompatibility(&pollapi.VirtioPollProfile{}, &pollapi.VirtioPollProfileReply{}); err != nil {
		t.Skipf("the optional local VPP queue accounting patch is unavailable: %v", err)
	}
	for _, name := range []string{"tap901", "tap902"} {
		index := interface_types.InterfaceIndex(counters[name].Counters.InterfaceIndex)
		request := &interfaces.SwInterfaceSetRxMode{SwIfIndex: index, Mode: interface_types.RX_MODE_API_POLLING}
		if _, err := interfaces.NewServiceClient(connection).SwInterfaceSetRxMode(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		defer func() {
			request.Mode = interface_types.RX_MODE_API_ADAPTIVE
			if _, err := interfaces.NewServiceClient(connection).SwInterfaceSetRxMode(context.WithoutCancel(t.Context()), request); err != nil {
				t.Errorf("restore adaptive receive mode: %v", err)
			}
		}()
	}

	t.Run("fresh quarter second counters", func(t *testing.T) {
		profile, err := vppclient.ProfileQueuePolling(t.Context(), endpoints, 250*time.Millisecond)
		if err != nil || !profile.Available || profile.Duration < 250*time.Millisecond || profile.Sequence < 3 {
			t.Fatalf("collect a fresh receive snapshot without the standard collector: %v, %+v", err, profile)
		}
		var empty uint64
		for _, queue := range profile.Queues {
			empty += queue.EmptyCalls
			if queue.SecondsPerTick <= 0 || queue.EmptyElapsed < 0 || queue.NonemptyElapsed < 0 || queue.RefillElapsed < 0 {
				t.Fatalf("queue clock conversion is invalid: %+v", queue)
			}
		}
		if empty == 0 {
			t.Fatal("polling TAP queues did not record strict empty checks")
		}
	})

	t.Run("busy and empty queues on one worker", func(t *testing.T) {
		verifyMixedQueuePolling(t, connection, endpoints)
	})

	service := pollapi.NewServiceClient(connection)
	t.Run("owner conflict and queue topology", func(t *testing.T) {
		token := queueProfileToken(t)
		if _, err := service.VirtioPollProfile(t.Context(), &pollapi.VirtioPollProfile{Session: token, Action: 1}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			cleanup := context.WithoutCancel(t.Context())
			if _, err := service.VirtioPollProfile(cleanup, &pollapi.VirtioPollProfile{Session: token, Action: 3}); err != nil {
				t.Errorf("stop the owned receive profile: %v", err)
			}
			if _, err := service.VirtioPollProfile(cleanup, &pollapi.VirtioPollProfile{Session: token, Action: 4}); err != nil {
				t.Errorf("release the owned receive profile: %v", err)
			}
		}()
		if profile, err := vppclient.ProfileQueuePolling(t.Context(), endpoints, time.Millisecond); err == nil || profile.Available {
			t.Fatalf("a second collector replaced an active owner: %v, %+v", err, profile)
		}
		created, err := tapv2.NewServiceClient(connection).TapCreateV3(t.Context(), &tapv2.TapCreateV3{ID: 904, UseRandomMac: true, NumRxQueues: 1, NumTxQueues: 1, RxRingSz: 256, TxRingSz: 256, HostIfNameSet: true, HostIfName: "gvpp-poll"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := tapv2.NewServiceClient(connection).TapDeleteV2(context.WithoutCancel(t.Context()), &tapv2.TapDeleteV2{SwIfIndex: created.SwIfIndex}); err != nil {
				t.Errorf("remove the queue topology test TAP: %v", err)
			}
		}()
		snapshot, err := service.VirtioPollProfile(t.Context(), &pollapi.VirtioPollProfile{Session: token, Action: 2})
		if err != nil || snapshot.Valid {
			t.Fatalf("a changed receive topology did not invalidate the snapshot: %v, %+v", err, snapshot)
		}
	})

	t.Run("disconnect and reopen", func(t *testing.T) {
		owner, err := govpp.Connect(endpoints.APISocket)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pollapi.NewServiceClient(owner).VirtioPollProfile(t.Context(), &pollapi.VirtioPollProfile{Session: queueProfileToken(t), Action: 1})
		owner.Disconnect()
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(endpoints.Timeout)
		for {
			profile, err := vppclient.ProfileQueuePolling(t.Context(), endpoints, time.Millisecond)
			if err == nil && profile.Available {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the disconnected API owner did not release its profile: %v, %+v", err, profile)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("cancel and reopen", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		profile, err := vppclient.ProfileQueuePolling(ctx, endpoints, time.Second)
		if !errors.Is(err, context.DeadlineExceeded) || profile.Available {
			t.Fatalf("a canceled profile must retain its failure: %v, %+v", err, profile)
		}
		profile, err = vppclient.ProfileQueuePolling(t.Context(), endpoints, 250*time.Millisecond)
		if err != nil || !profile.Available {
			t.Fatalf("cancellation did not release profile ownership: %v, %+v", err, profile)
		}
	})
}

func createPollingTAP(t *testing.T, connection *core.Connection, timeout time.Duration, id uint32, host string) (interface_types.InterfaceIndex, netlink.Link) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	service := tapv2.NewServiceClient(connection)
	created, err := service.TapCreateV3(ctx, &tapv2.TapCreateV3{ID: id, Tag: "mwan-queue-profile-test", UseRandomMac: true, NumRxQueues: 1, NumTxQueues: 1, RxRingSz: 256, TxRingSz: 256, HostIfNameSet: true, HostIfName: host})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), timeout)
		defer stop()
		if _, err := service.TapDeleteV2(cleanup, &tapv2.TapDeleteV2{SwIfIndex: created.SwIfIndex}); err != nil {
			t.Errorf("remove the mixed queue TAP %s: %v", host, err)
		}
	})
	link, err := netlink.LinkByName(host)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("/proc/sys/net/ipv6/conf", host, "disable_ipv6"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	interfaceService := interfaces.NewServiceClient(connection)
	if _, err := interfaceService.SwInterfaceSetFlags(ctx, &interfaces.SwInterfaceSetFlags{SwIfIndex: created.SwIfIndex, Flags: interface_types.IF_STATUS_API_FLAG_ADMIN_UP}); err != nil {
		t.Fatal(err)
	}
	if _, err := interfaceService.SwInterfaceSetRxPlacement(ctx, &interfaces.SwInterfaceSetRxPlacement{SwIfIndex: created.SwIfIndex, QueueID: 0, WorkerID: 0, IsMain: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := interfaceService.SwInterfaceSetRxMode(ctx, &interfaces.SwInterfaceSetRxMode{SwIfIndex: created.SwIfIndex, Mode: interface_types.RX_MODE_API_POLLING}); err != nil {
		t.Fatal(err)
	}
	return created.SwIfIndex, link
}

func verifyMixedQueuePolling(t *testing.T, connection *core.Connection, endpoints vppclient.Endpoints) {
	t.Helper()
	busyIndex, busyLink := createPollingTAP(t, connection, endpoints.Timeout, 906, "gvpp-poll-busy")
	idleIndex, _ := createPollingTAP(t, connection, endpoints.Timeout, 907, "gvpp-poll-idle")
	const etherType = 0x88b5
	var protocolBytes [2]byte
	binary.BigEndian.PutUint16(protocolBytes[:], etherType)
	protocol := binary.NativeEndian.Uint16(protocolBytes[:])
	socket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, int(protocol))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(socket)
	address := &unix.SockaddrLinklayer{Ifindex: busyLink.Attrs().Index, Protocol: protocol}
	if err := unix.Bind(socket, address); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 64)
	for index := range 6 {
		frame[index] = 0xff
	}
	copy(frame[6:12], busyLink.Attrs().HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], etherType)
	ctx, cancel := context.WithTimeout(t.Context(), endpoints.Timeout)
	defer cancel()
	senderContext, stopSender := context.WithCancel(ctx)
	senderResult := make(chan error, 1)
	go func() { senderResult <- sendQueueFrames(senderContext, socket, address, frame) }()
	defer func() {
		stopSender()
		select {
		case err := <-senderResult:
			if err != nil {
				t.Errorf("send mixed queue Ethernet frames: %v", err)
			}
		case <-time.After(endpoints.Timeout):
			t.Error("the mixed queue sender did not stop after cancellation")
		}
	}()
	profile, err := vppclient.ProfileQueuePolling(ctx, endpoints, 250*time.Millisecond)
	if err != nil || !profile.Available {
		t.Fatalf("profile a busy queue beside an empty queue: %v, %+v", err, profile)
	}
	var busy, idle vppclient.QueuePollCounter
	var busyFound, idleFound bool
	for _, queue := range profile.Queues {
		if queue.QueueID != 0 {
			continue
		}
		if queue.InterfaceIndex == uint32(busyIndex) && queue.Packets > 0 {
			busy, busyFound = queue, true
		}
		if queue.InterfaceIndex == uint32(idleIndex) && queue.EmptyCalls > 0 {
			idle, idleFound = queue, true
		}
	}
	if !busyFound || !idleFound || busy.ThreadID != idle.ThreadID || busy.NonemptyCalls == 0 || idle.Packets != 0 || idle.NonemptyCalls != 0 {
		t.Fatalf("receive queues did not separate busy and empty work: busy=%+v, idle=%+v, queues=%+v", busy, idle, profile.Queues)
	}
	t.Logf("same-worker queue counters: busy=%+v, idle=%+v", busy, idle)
}

func sendQueueFrames(ctx context.Context, socket int, address *unix.SockaddrLinklayer, frame []byte) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for range 8 {
				if err := unix.Sendto(socket, frame, unix.MSG_DONTWAIT, address); err != nil {
					return fmt.Errorf("send a raw Ethernet frame: %w", err)
				}
			}
		}
	}
}

func queueProfileToken(t *testing.T) uint64 {
	t.Helper()
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	token := binary.BigEndian.Uint64(bytes[:])
	if token == 0 {
		t.Fatal("the generated session token must be nonzero")
	}
	return token
}
