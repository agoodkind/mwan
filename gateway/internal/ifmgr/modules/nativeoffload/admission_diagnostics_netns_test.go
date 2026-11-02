//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ti-mo/conntrack"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/offload"
)

type admissionDiagnosticRecord struct {
	Message  string                               `json:"msg"`
	Counters map[string]uint64                    `json:"counters"`
	Samples  map[string]admissionDiagnosticSample `json:"samples"`
}

type admissionDiagnosticSample struct {
	Outcome   admissionOutcome `json:"outcome"`
	Interface string           `json:"interface"`
	Ifindex   int              `json:"ifindex"`
	Sample    struct {
		ObservedAt  time.Time                 `json:"observed_at"`
		Age         time.Duration             `json:"age"`
		Observation offload.NativeObservation `json:"observation"`
		Connection  admissionConnection       `json:"connection"`
		Route       admissionRoute            `json:"route"`
		Error       struct {
			Present bool   `json:"present"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"sample"`
}

func TestNativeAdmissionDiagnostics(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, "")
	observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
	var output bytes.Buffer
	fixture.module.Log = slog.New(slog.NewJSONHandler(&output, nil))
	fixture.module.cfg.Settings.ObservationDiagnostics = true
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	if err := fixture.tracking.Update(conntrack.Flow{TupleOrig: flow.TupleOrig, Mark: admissionUnknownMark}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		fixture.requireRejected(t, observation, EligibilityPolicyUnavailable)
	}
	if err := fixture.tracking.Update(conntrack.Flow{TupleOrig: flow.TupleOrig, Mark: admissionMark}); err != nil {
		t.Fatal(err)
	}
	fixture.module.maintain(fixture.ctx)
	records := readAdmissionDiagnostics(t, &output)
	ready := requireAdmissionSample(t, records, "candidate_ready", 1)
	rejected := requireAdmissionSample(t, records, "wan_not_ready", 1)
	if ready.Outcome != admissionAccepted || rejected.Outcome != admissionRejected {
		t.Fatalf("candidate diagnostics changed the admission outcome: ready=%+v rejected=%+v", ready, rejected)
	}
	for _, record := range []admissionDiagnosticSample{ready, rejected} {
		connection := record.Sample.Connection
		if record.Ifindex != fixture.input.Index || record.Interface != fixture.input.Name || !record.Sample.ObservedAt.Equal(observation.at) || record.Sample.Age < 0 || record.Sample.Observation.Tuple != observation.Tuple || !connection.Valid || connection.ID != flow.ID || connection.Original != flow.TupleOrig || connection.Reply != flow.TupleReply || connection.Timeout == 0 || connection.Status != flow.Status || !connection.TCPPresent || connection.TCPState != conntrackEstablished {
			t.Fatalf("candidate diagnostic lost the observed packet or kernel evidence: %+v", record)
		}
	}
	if ready.Sample.Connection.Mark != admissionMark || rejected.Sample.Connection.Mark != admissionUnknownMark {
		t.Fatalf("later kernel updates changed the diagnostic sample: ready=%+v rejected=%+v", ready, rejected)
	}
	if !ready.Sample.Route.Valid || !ready.Sample.Route.Usable || ready.Sample.Route.OutputIndex != fixture.output.Index {
		t.Fatalf("eligible candidate omitted its actual route: %+v", ready)
	}
	requireAdmissionCount(t, records, "wan_not_ready", 2)
	fixture.module.maintain(fixture.ctx)
	records = readAdmissionDiagnostics(t, &output)
	requireAdmissionSample(t, records, "wan_not_ready", 0)
	requireAdmissionSample(t, records, "candidate_ready", 0)
	requireAdmissionCount(t, records, "wan_not_ready", 2)
	if err := fixture.tracking.Delete(flow); err != nil {
		t.Fatal(err)
	}
	fixture.requireRejected(t, observation, EligibilityStale)
	fixture.module.maintain(fixture.ctx)
	records = readAdmissionDiagnostics(t, &output)
	missing := requireAdmissionSample(t, records, "conntrack_lookup_failed", 1)
	if missing.Sample.Connection.Valid || !missing.Sample.Error.Present || missing.Sample.Error.Message == "" {
		t.Fatalf("missing conntrack evidence appeared successful: %+v", missing)
	}
	fixture.module.cfg.Settings.ObservationMaxAge, fixture.module.cfg.Settings.CounterInterval = admissionTimeout, admissionTimeout
	fixture.module.attempts = observationCache{entries: make(map[observationKey]*list.Element), capacity: nativeCaptureCapacity}
	fixture.module.admit(fixture.ctx, observation)
	fixture.module.admit(fixture.ctx, observation)
	fixture.module.admissionPaused.Store(true)
	fixture.module.admit(fixture.ctx, observation)
	fixture.module.admissionPaused.Store(false)
	fixture.module.cfg.Settings.ObservationMaxAge = 0
	fixture.module.admit(fixture.ctx, observation)
	if fixture.module.stats.Fallback != 2 || fixture.module.stats.ObservationSkipped != 1 || fixture.module.stats.Installed != 0 {
		t.Fatalf("diagnostics changed rejection or suppression outcomes: %+v", fixture.module.stats)
	}
	fixture.module.maintain(fixture.ctx)
	records = readAdmissionDiagnostics(t, &output)
	for _, stage := range []string{"candidate_rejected", "attempt_throttled", "paused", "observation_age"} {
		requireAdmissionSample(t, records, stage, 1)
		requireAdmissionCount(t, records, stage, 1)
	}
	fixture.module.cfg.Settings.ObservationDiagnostics = false
	fixture.requireRejected(t, observation, EligibilityStale)
	fixture.module.maintain(fixture.ctx)
	requireAdmissionSample(t, readAdmissionDiagnostics(t, &output), "conntrack_lookup_failed", 0)
	fixture.module.cfg.Settings.ObservationDiagnostics = true
	fixture.module.maintain(fixture.ctx)
	records = readAdmissionDiagnostics(t, &output)
	requireAdmissionCount(t, records, "conntrack_lookup_failed", 2)
	requireAdmissionSample(t, records, "conntrack_lookup_failed", 0)
}

func TestNativeAdmissionDiagnosticsDisabled(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, "")
	observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
	var output bytes.Buffer
	fixture.module.Log = slog.New(slog.NewJSONHandler(&output, nil))
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	fixture.module.maintain(fixture.ctx)
	if fixture.module.admissionDiagnostics != nil || len(readAdmissionDiagnostics(t, &output)) != 0 {
		t.Fatal("disabled admission diagnostics retained samples or emitted records")
	}
}

func TestNativeAdmissionDiagnosticsNotReady(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	module := fixture.module
	module.cfg.Settings.ObservationDiagnostics = true
	module.cfg.Settings.CounterInterval, module.cfg.Settings.ResyncInterval = admissionTimeout, admissionTimeout
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	var err error
	module.events, err = offload.NewConntrackEvents(ctx, nativeCaptureCapacity, nativeCaptureSocketBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := module.events.Close(); err != nil {
			t.Error(err)
		}
	})
	module.observations, module.failures = make(chan packetObservation, nativeCaptureCapacity), make(chan error, 1)
	capture, err := openPacketCapture(ctx, fixture.input, nativeCaptureSocketBytes, module.observations, module.failures, &module.admissionPaused, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := capture.close(); err != nil {
			t.Error(err)
		}
		<-capture.done
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		module.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	frame := nativeCaptureFrameSelectors(t, fixture.sender, fixture.input, [2]string{admissionClientV4, admissionRemoteV4}, unix.IPPROTO_TCP, admissionTrafficClass)
	sentAt := time.Now()
	nativeCaptureSend(t, fixture.sender, frame)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		module.Lock()
		recorded := module.admissionDiagnostics != nil && module.admissionDiagnostics.counts[admissionNotReady] != 0
		module.Unlock()
		if recorded {
			break
		}
		select {
		case <-fixture.ctx.Done():
			t.Fatal("admission loop did not report the captured packet before runtime readiness")
		case <-done:
			t.Fatal("admission loop stopped before reporting the captured packet")
		case <-ticker.C:
		}
	}
	cancel()
	<-done
	var output bytes.Buffer
	module.Log = slog.New(slog.NewJSONHandler(&output, nil))
	module.maintain(fixture.ctx)
	record := requireAdmissionSample(t, readAdmissionDiagnostics(t, &output), "not_ready", 1)
	if record.Ifindex != fixture.input.Index || record.Sample.Observation.Tuple.IP.SourceAddress.String() != admissionClientV4 || record.Sample.ObservedAt.Before(sentAt) || record.Sample.ObservedAt.After(time.Now()) || record.Sample.Age < 0 || record.Outcome != admissionRejected || module.stats.Installed != 0 || module.stats.Fallback != 0 {
		t.Fatalf("readiness rejection changed admission behavior or lost the captured tuple: %+v stats=%+v", record, module.stats)
	}
}

func readAdmissionDiagnostics(t *testing.T, output *bytes.Buffer) []admissionDiagnosticRecord {
	t.Helper()
	decoder := json.NewDecoder(output)
	var records []admissionDiagnosticRecord
	for {
		var record admissionDiagnosticRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			if len(records) > 1 {
				t.Fatalf("one reporting interval emitted %d admission summaries", len(records))
			}
			return records
		}
		if err != nil {
			t.Fatal(err)
		}
		if record.Message == "native admission diagnostics" {
			records = append(records, record)
		}
	}
}

func requireAdmissionSample(t *testing.T, records []admissionDiagnosticRecord, stage string, expected int) admissionDiagnosticSample {
	t.Helper()
	count := 0
	var sample admissionDiagnosticSample
	for _, record := range records {
		if current, present := record.Samples[stage]; present {
			sample = current
			count++
		}
	}
	if count != expected {
		t.Fatalf("stage %s emitted %d samples, expected %d: %+v", stage, count, expected, records)
	}
	return sample
}

func requireAdmissionCount(t *testing.T, records []admissionDiagnosticRecord, stage string, expected uint64) {
	t.Helper()
	for _, record := range records {
		if record.Message == "native admission diagnostics" {
			if record.Counters[stage] != expected {
				t.Fatalf("stage %s counted %d outcomes, expected %d", stage, record.Counters[stage], expected)
			}
			return
		}
	}
	t.Fatal("periodic maintenance omitted admission counters")
}
