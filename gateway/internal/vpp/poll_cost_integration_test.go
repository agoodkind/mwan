//go:build linux && vpp_integration

package vpp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.fd.io/govpp"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"

	vppclient "goodkind.io/mwan/internal/vpp"
	pollapi "goodkind.io/mwan/internal/vpp/pollapi/virtio_poll"
)

type queueProfileCostMeasurement struct {
	IdleWaitUsec         uint32                  `json:"IdleWaitUsec"`
	RXMode               string                  `json:"RXMode"`
	QueuePolling         bool                    `json:"QueuePolling"`
	Repetition           int                     `json:"Repetition"`
	RequestedDuration    time.Duration           `json:"RequestedDuration"`
	CallWallDuration     time.Duration           `json:"CallWallDuration"`
	DurationUnit         string                  `json:"DurationUnit"`
	MainThreadID         uint32                  `json:"MainThreadID"`
	PacketWorkerThreadID uint32                  `json:"PacketWorkerThreadID"`
	Profile              vppclient.ProfileResult `json:"Profile"`
}

func verifyQueueProfileCost(t *testing.T, endpoints vppclient.Endpoints, executable string, idleWaitUsec uint32) {
	t.Helper()
	connection, err := govpp.Connect(endpoints.APISocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	if err := connection.CheckCompatibility(&pollapi.VirtioPollProfile{Session: 0, Action: 0}, &pollapi.VirtioPollProfileReply{Retval: 0, Session: 0, Sequence: 0, ElapsedNs: 0, Active: false, Valid: false}); err != nil {
		t.Skipf("the optional local VPP queue accounting patch is unavailable: %v", err)
	}
	index, _ := createPollingTAP(t, connection, endpoints.Timeout, 908, "gvpp-cpu-poll")
	for _, mode := range []interface_types.RxMode{interface_types.RX_MODE_API_POLLING, interface_types.RX_MODE_API_ADAPTIVE} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), endpoints.Timeout)
			defer cancel()
			if _, err := interfaces.NewServiceClient(connection).SwInterfaceSetRxMode(ctx, &interfaces.SwInterfaceSetRxMode{SwIfIndex: index, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			verifyQueueModeCost(t, endpoints, executable, idleWaitUsec, index, mode)
		})
	}
}

func verifyQueueModeCost(t *testing.T, endpoints vppclient.Endpoints, executable string, idleWaitUsec uint32, index interface_types.InterfaceIndex, mode interface_types.RxMode) {
	t.Helper()
	// Native instruction sampling remains enabled in both queue modes.
	// An already saturated worker cannot establish zero instrumentation cost.
	for repetition := 1; repetition <= 3; repetition++ {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("repetition %d queue polling %t", repetition, enabled), func(t *testing.T) {
				const duration = 3 * time.Second
				ctx, cancel := context.WithTimeout(t.Context(), duration+endpoints.Timeout)
				defer cancel()
				started := time.Now()
				profile, err := vppclient.ProfileWithOptions(ctx, endpoints, executable, duration, vppclient.ProfileOptions{QueuePolling: enabled})
				observed := time.Since(started)
				measurement := queueProfileCostMeasurement{
					IdleWaitUsec: idleWaitUsec, RXMode: mode.String(), QueuePolling: enabled, Repetition: repetition,
					RequestedDuration: duration, CallWallDuration: observed, DurationUnit: "nanoseconds",
					MainThreadID: 0, PacketWorkerThreadID: 1, Profile: profile,
				}
				encoded, encodeError := json.Marshal(measurement)
				if encodeError != nil {
					t.Fatal(encodeError)
				}
				t.Logf("VPP_QUEUE_PROFILE_COST %s", encoded)
				if err != nil || !profile.Available {
					t.Fatalf("collect the real VPP CPU and queue profile: %v, %+v", err, profile)
				}
				if profile.Duration < duration || observed < profile.Duration {
					t.Fatalf("profile intervals do not cover the requested sample: requested=%s, sample=%s, call=%s", duration, profile.Duration, observed)
				}
				verifyProfileCPUClocks(t, profile)
				verifyIdleQueueCost(t, profile.QueuePolling, enabled, index, mode)
			})
		}
	}
}

func verifyProfileCPUClocks(t *testing.T, profile vppclient.ProfileResult) {
	t.Helper()
	var mainFound, workerFound bool
	for _, thread := range profile.Threads {
		if !thread.Available || !thread.CPUClockAvailable {
			t.Fatalf("the main and packet worker require measured CPU clocks: %+v", thread)
		}
		switch thread.Thread.ID {
		case 0:
			mainFound = true
		case 1:
			workerFound = true
		default:
			t.Fatalf("the fixture must retain its main thread and one packet worker: %+v", thread.Thread)
		}
	}
	if !mainFound || !workerFound || len(profile.Threads) != 2 {
		t.Fatalf("the CPU profile omits or repeats the main or packet worker: %+v", profile.Threads)
	}
}

func verifyIdleQueueCost(t *testing.T, polling *vppclient.QueuePollProfile, enabled bool, index interface_types.InterfaceIndex, mode interface_types.RxMode) {
	t.Helper()
	if !enabled {
		if polling != nil {
			t.Fatalf("disabled queue accounting returned an active profile: %+v", polling)
		}
		return
	}
	if polling == nil || !polling.Available {
		t.Fatalf("enabled queue accounting requires a valid snapshot: %+v", polling)
	}
	for _, queue := range polling.Queues {
		if queue.ThreadID != 1 || queue.InterfaceIndex != uint32(index) || queue.QueueID != 0 {
			continue
		}
		if queue.NonemptyCalls != 0 || queue.Packets != 0 {
			t.Fatalf("the isolated polling TAP did not remain idle: %+v", queue)
		}
		if mode == interface_types.RX_MODE_API_POLLING && queue.EmptyCalls == 0 {
			t.Fatalf("the forced polling queue did not record empty checks: %+v", queue)
		}
		return
	}
	t.Fatalf("queue accounting omitted the isolated polling TAP %d: %+v", index, polling.Queues)
}
