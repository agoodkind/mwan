//go:build linux && vpp_integration

package vpp_test

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"go.fd.io/govpp"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/tapv2"

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
