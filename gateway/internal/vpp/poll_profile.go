package vpp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"go.fd.io/govpp/adapter"
	"go.fd.io/govpp/adapter/statsclient"
	"go.fd.io/govpp/api"
	"go.fd.io/govpp/binapi/vlib"

	pollapi "goodkind.io/mwan/internal/vpp/pollapi/virtio_poll"
)

const (
	pollStart    uint8 = 1
	pollSnapshot uint8 = 2
	pollStop     uint8 = 3
	pollRelease  uint8 = 4
	pollFields         = 6
	pollRoot           = "/mwan/virtio-poll/"
)

// ProfileOptions keeps optional receive instrumentation separate from CPU sampling.
type ProfileOptions struct {
	QueuePolling bool
}

// QueuePollCounter measures elapsed receive intervals, including preemption.
// Linux CPUClockTime measures scheduled CPU separately.
type QueuePollCounter struct {
	ThreadID        uint32        `json:"ThreadID"`
	InterfaceIndex  uint32        `json:"InterfaceIndex"`
	QueueID         uint16        `json:"QueueID"`
	SecondsPerTick  float64       `json:"SecondsPerTick"`
	EmptyCalls      uint64        `json:"EmptyCalls"`
	EmptyTicks      uint64        `json:"EmptyTicks"`
	EmptyElapsed    time.Duration `json:"EmptyElapsed"`
	NonemptyCalls   uint64        `json:"NonemptyCalls"`
	NonemptyTicks   uint64        `json:"NonemptyTicks"`
	NonemptyElapsed time.Duration `json:"NonemptyElapsed"`
	Packets         uint64        `json:"Packets"`
	RefillTicks     uint64        `json:"RefillTicks"`
	RefillElapsed   time.Duration `json:"RefillElapsed"`
}

// QueuePollProfile rejects missing, stale and reset snapshots instead of reporting zero.
type QueuePollProfile struct {
	Available bool               `json:"Available"`
	Reason    string             `json:"Reason"`
	Duration  time.Duration      `json:"Duration"`
	Session   uint64             `json:"Session"`
	Sequence  uint64             `json:"Sequence"`
	Queues    []QueuePollCounter `json:"Queues"`
}

type queuePollSession struct {
	client     *Client
	statistics *statsclient.StatsClient
	generation string
	token      uint64
	started    bool
	stopped    bool
	baseline   QueuePollProfile
	threads    []vlib.ThreadData
}

type queuePollSnapshot struct {
	metadata adapter.SimpleCounterStat
	counters adapter.SimpleCounterStat
	scales   adapter.SimpleCounterStat
	names    adapter.NameStat
}

type pollStatistic string

const (
	pollMetadata pollStatistic = pollRoot + "metadata"
	pollData     pollStatistic = pollRoot + "data"
	pollNames    pollStatistic = pollRoot + "names"
	pollScales   pollStatistic = pollRoot + "seconds-per-tick"
)

// ProfileQueuePolling uses the optional VPP extension without requiring perf permissions.
func ProfileQueuePolling(ctx context.Context, endpoints Endpoints, duration time.Duration) (result QueuePollProfile, failure error) {
	defer func() {
		if failure != nil {
			result.Available = false
			result.Reason = failure.Error()
		}
	}()
	if duration <= 0 {
		return result, fmt.Errorf("VPP queue profile duration must be positive")
	}
	session, err := openQueuePollSession(ctx, endpoints)
	if err != nil {
		result.Reason = err.Error()
		return result, err
	}
	defer func() {
		failure = errors.Join(failure, session.close(ctx))
	}()
	if err := session.start(ctx); err != nil {
		result.Reason = err.Error()
		return result, err
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		failure = fmt.Errorf("collect VPP queue profile: %w", ctx.Err())
		slog.WarnContext(ctx, "collect VPP queue profile interrupted", "error", failure)
	case <-timer.C:
	}
	result, err = session.finish(ctx)
	failure = errors.Join(failure, err)
	if failure != nil {
		result.Available = false
		result.Reason = failure.Error()
	}
	return result, failure
}

func openQueuePollSession(ctx context.Context, endpoints Endpoints) (*queuePollSession, error) {
	client, err := Open(ctx, endpoints, &pollapi.VirtioPollProfile{Session: 0, Action: 0}, &pollapi.VirtioPollProfileReply{Retval: 0, Session: 0, Sequence: 0, ElapsedNs: 0, Active: false, Valid: false}, &vlib.ShowThreads{}, &vlib.ShowThreadsReply{})
	if err != nil {
		return nil, err
	}
	requestContext, cancel := client.requestContext(ctx)
	threads, err := vlib.NewServiceClient(client.connection).ShowThreads(requestContext, &vlib.ShowThreads{})
	cancel()
	if err != nil {
		client.Close()
		slog.WarnContext(ctx, "read VPP queue profiling threads failed", "error", err)
		return nil, fmt.Errorf("read VPP queue profiling threads: %w", err)
	}
	if len(threads.ThreadData) == 0 {
		client.Close()
		return nil, fmt.Errorf("VPP queue profiling requires thread identities")
	}
	generation, err := socketGeneration(ctx, endpoints.StatsSocket)
	if err != nil {
		client.Close()
		return nil, err
	}
	statistics := statsclient.NewStatsClient(endpoints.StatsSocket, statsclient.SetSocketRetryTimeout(endpoints.Timeout))
	if err := statistics.Connect(); err != nil {
		client.Close()
		slog.WarnContext(ctx, "connect VPP queue statistics failed", "socket", endpoints.StatsSocket, "error", err)
		return nil, fmt.Errorf("connect VPP queue statistics: %w", err)
	}
	var tokenBytes [8]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		cleanupError := statistics.Disconnect()
		client.Close()
		slog.WarnContext(ctx, "create VPP queue profile owner token failed", "error", errors.Join(err, cleanupError))
		return nil, errors.Join(fmt.Errorf("create VPP queue profile owner token: %w", err), cleanupError)
	}
	token := binary.BigEndian.Uint64(tokenBytes[:])
	if token == 0 {
		cleanupError := statistics.Disconnect()
		client.Close()
		return nil, errors.Join(fmt.Errorf("VPP queue profile owner token must be nonzero"), cleanupError)
	}
	return &queuePollSession{client: client, statistics: statistics, generation: generation, token: token, started: false, stopped: false, baseline: QueuePollProfile{Available: false, Reason: "", Duration: 0, Session: 0, Sequence: 0, Queues: nil}, threads: threads.ThreadData}, nil
}

func (session *queuePollSession) control(ctx context.Context, action uint8) (*pollapi.VirtioPollProfileReply, error) {
	requestContext, cancel := session.client.requestContext(ctx)
	defer cancel()
	reply, err := pollapi.NewServiceClient(session.client.connection).VirtioPollProfile(requestContext, &pollapi.VirtioPollProfile{Session: session.token, Action: action})
	if err != nil {
		slog.WarnContext(ctx, "control VPP queue profiling failed", "session", session.token, "action", action, "error", err)
		return nil, fmt.Errorf("VPP queue profile action %d: %w", action, err)
	}
	if reply.Session != session.token {
		return nil, fmt.Errorf("VPP queue profile action %d returned another session", action)
	}
	if action == pollStop && !reply.Active {
		session.stopped = true
	}
	return reply, nil
}

func (session *queuePollSession) start(ctx context.Context) error {
	// A timed out start can still acquire ownership. Cleanup must attempt this token.
	session.started = true
	session.stopped = false
	reply, err := session.control(ctx, pollStart)
	if err != nil {
		var rejected api.VPPApiError
		if errors.As(err, &rejected) {
			session.started = false
		}
		return err
	}
	baseline, err := session.read(ctx, reply)
	if err != nil {
		return err
	}
	if !reply.Active || baseline.Sequence != 1 || baseline.Duration != 0 {
		return fmt.Errorf("VPP queue profiling did not publish a fresh active baseline")
	}
	for _, counter := range baseline.Queues {
		if counter.EmptyCalls != 0 || counter.EmptyTicks != 0 || counter.NonemptyCalls != 0 || counter.NonemptyTicks != 0 || counter.Packets != 0 || counter.RefillTicks != 0 {
			return fmt.Errorf("VPP queue profiling baseline contains counters from an earlier session")
		}
	}
	session.baseline = baseline
	return nil
}

func (session *queuePollSession) finish(ctx context.Context) (result QueuePollProfile, failure error) {
	// Stop and release must remain possible after the sample context expires.
	cleanupContext := context.WithoutCancel(ctx)
	snapshot, err := session.control(cleanupContext, pollSnapshot)
	if err != nil {
		result.Reason = err.Error()
		return result, err
	}
	current, err := session.read(cleanupContext, snapshot)
	if err != nil {
		result.Reason = err.Error()
		return result, err
	}
	if !snapshot.Active {
		return result, fmt.Errorf("VPP queue profiling stopped before the requested snapshot")
	}
	if err := validateQueueDelta(session.baseline, current); err != nil {
		result.Reason = err.Error()
		return result, err
	}
	reply, err := session.control(cleanupContext, pollStop)
	if err != nil {
		result.Reason = err.Error()
		return result, err
	}
	result, err = session.read(cleanupContext, reply)
	if err == nil && (reply.Active || result.Sequence <= session.baseline.Sequence || result.Duration <= session.baseline.Duration) {
		err = fmt.Errorf("VPP queue profiling did not publish a fresh stopped snapshot")
	}
	if err == nil {
		err = validateQueueDelta(current, result)
	}
	if err != nil {
		result.Available = false
		result.Reason = err.Error()
		return result, err
	}
	result.Available = true
	return result, nil
}

func (session *queuePollSession) read(ctx context.Context, reply *pollapi.VirtioPollProfileReply) (QueuePollProfile, error) {
	generation, err := socketGeneration(ctx, session.client.settings.StatsSocket)
	if err != nil {
		return QueuePollProfile{}, err
	}
	if generation != session.generation {
		return QueuePollProfile{}, fmt.Errorf("VPP queue statistics socket changed during profiling")
	}
	entries, err := session.statistics.DumpStats("^/mwan/virtio-poll/")
	if err != nil {
		slog.WarnContext(ctx, "read VPP queue profiling snapshot failed", "session", session.token, "error", err)
		return QueuePollProfile{}, fmt.Errorf("read VPP queue profiling snapshot: %w", err)
	}
	return decodeQueuePoll(entries, reply, session.threads)
}

func decodeQueuePoll(entries []adapter.StatEntry, reply *pollapi.VirtioPollProfileReply, threads []vlib.ThreadData) (QueuePollProfile, error) {
	result := QueuePollProfile{Available: false, Reason: "", Duration: 0, Session: reply.Session, Sequence: reply.Sequence, Queues: nil}
	snapshot := decodeQueuePollStats(entries)
	if err := snapshot.validate(reply, len(threads)); err != nil {
		return result, err
	}
	if reply.ElapsedNs > math.MaxInt64 {
		return result, fmt.Errorf("VPP queue profiling duration exceeds the supported range")
	}
	result.Duration = time.Duration(reply.ElapsedNs)
	seenThreads := make(map[uint32]bool, len(threads))
	for _, thread := range threads {
		if uint64(thread.ID) >= uint64(len(snapshot.counters)) || seenThreads[thread.ID] {
			return result, fmt.Errorf("VPP queue profile omits or repeats thread %d", thread.ID)
		}
		seenThreads[thread.ID] = true
		counters, err := snapshot.threadCounters(thread.ID)
		result.Queues = append(result.Queues, counters...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func decodeQueuePollStats(entries []adapter.StatEntry) queuePollSnapshot {
	var snapshot queuePollSnapshot
	for _, entry := range entries {
		switch pollStatistic(entry.Name) {
		case pollMetadata:
			snapshot.metadata, _ = entry.Data.(adapter.SimpleCounterStat)
		case pollData:
			snapshot.counters, _ = entry.Data.(adapter.SimpleCounterStat)
		case pollNames:
			snapshot.names, _ = entry.Data.(adapter.NameStat)
		case pollScales:
			snapshot.scales, _ = entry.Data.(adapter.SimpleCounterStat)
		}
	}
	return snapshot
}

func (snapshot queuePollSnapshot) validate(reply *pollapi.VirtioPollProfileReply, threadCount int) error {
	if len(snapshot.metadata) != 1 || len(snapshot.metadata[0]) != pollFields || len(snapshot.names) == 0 || len(snapshot.counters) != threadCount || len(snapshot.scales) != threadCount {
		return fmt.Errorf("VPP queue profiling snapshot omits required metadata, queues or thread counters")
	}
	meta := snapshot.metadata[0]
	if uint64(meta[0]) != reply.Session || uint64(meta[1]) != reply.Sequence || uint64(meta[2]) != reply.ElapsedNs || (meta[3] != 0) != reply.Active || (meta[4] != 0) != reply.Valid || uint64(meta[5]) != uint64(len(snapshot.names)) || meta[3] > 1 || meta[4] > 1 {
		return fmt.Errorf("VPP queue profiling snapshot differs from its binary API receipt")
	}
	if !reply.Valid {
		return fmt.Errorf("VPP queue topology changed during profiling")
	}
	return nil
}

func (snapshot queuePollSnapshot) threadCounters(threadID uint32) ([]QueuePollCounter, error) {
	if len(snapshot.scales[threadID]) != 1 {
		return nil, fmt.Errorf("VPP queue clock scale omits thread %d", threadID)
	}
	// Counter vectors preserve the IEEE 754 bits without scalar integer truncation.
	scale := math.Float64frombits(uint64(snapshot.scales[threadID][0]))
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return nil, fmt.Errorf("VPP queue clock scale for thread %d is invalid", threadID)
	}
	values := snapshot.counters[threadID]
	if len(values) != len(snapshot.names)*pollFields {
		return nil, fmt.Errorf("VPP queue profile thread %d has an incomplete counter vector", threadID)
	}
	seenQueues := make(map[string]bool, len(snapshot.names))
	var counters []QueuePollCounter
	for queue, raw := range snapshot.names {
		name := strings.TrimRight(string(raw), "\x00")
		identity := strings.Split(name, "/")
		if len(identity) != 2 || seenQueues[name] {
			return counters, fmt.Errorf("VPP queue profile contains an invalid or repeated queue %q", name)
		}
		seenQueues[name] = true
		index, indexError := strconv.ParseUint(identity[0], 10, 32)
		queueID, queueError := strconv.ParseUint(identity[1], 10, 16)
		if err := errors.Join(indexError, queueError); err != nil {
			slog.Warn("parse VPP queue identity failed", "thread_id", threadID, "queue", name, "error", err)
			return counters, fmt.Errorf("parse VPP queue identity %q: %w", name, err)
		}
		value := values[queue*pollFields : (queue+1)*pollFields]
		counter := QueuePollCounter{ThreadID: threadID, InterfaceIndex: uint32(index), QueueID: uint16(queueID), SecondsPerTick: scale, EmptyCalls: uint64(value[0]), EmptyTicks: uint64(value[1]), EmptyElapsed: 0, NonemptyCalls: uint64(value[2]), NonemptyTicks: uint64(value[3]), NonemptyElapsed: 0, Packets: uint64(value[4]), RefillTicks: uint64(value[5]), RefillElapsed: 0}
		if err := counter.convertTicks(); err != nil {
			return counters, err
		}
		counters = append(counters, counter)
	}
	return counters, nil
}

func (counter *QueuePollCounter) convertTicks() error {
	var err error
	counter.EmptyElapsed, err = tickDuration(counter.EmptyTicks, counter.SecondsPerTick)
	if err != nil {
		return err
	}
	counter.NonemptyElapsed, err = tickDuration(counter.NonemptyTicks, counter.SecondsPerTick)
	if err != nil {
		return err
	}
	counter.RefillElapsed, err = tickDuration(counter.RefillTicks, counter.SecondsPerTick)
	return err
}

func tickDuration(ticks uint64, scale float64) (time.Duration, error) {
	nanoseconds := float64(ticks) * scale * float64(time.Second)
	if math.IsNaN(nanoseconds) || math.IsInf(nanoseconds, 0) || nanoseconds >= float64(math.MaxInt64) || nanoseconds < 0 {
		return 0, fmt.Errorf("VPP queue elapsed ticks exceed the supported duration")
	}
	return time.Duration(nanoseconds), nil
}

func validateQueueDelta(before, after QueuePollProfile) error {
	if before.Session != after.Session || len(before.Queues) != len(after.Queues) {
		return fmt.Errorf("VPP queue profile session or queue identities changed")
	}
	if after.Sequence <= before.Sequence || after.Duration <= before.Duration {
		return fmt.Errorf("VPP queue profile snapshot is stale")
	}
	for index, old := range before.Queues {
		current := after.Queues[index]
		if old.ThreadID != current.ThreadID || old.InterfaceIndex != current.InterfaceIndex || old.QueueID != current.QueueID || old.SecondsPerTick != current.SecondsPerTick {
			return fmt.Errorf("VPP queue profile identity or clock scale changed")
		}
		if current.EmptyCalls < old.EmptyCalls || current.EmptyTicks < old.EmptyTicks || current.NonemptyCalls < old.NonemptyCalls || current.NonemptyTicks < old.NonemptyTicks || current.Packets < old.Packets || current.RefillTicks < old.RefillTicks {
			return fmt.Errorf("VPP queue counters reset during profiling")
		}
	}
	return nil
}

func (session *queuePollSession) close(ctx context.Context) error {
	var failure error
	if session.started {
		cleanupContext := context.WithoutCancel(ctx)
		var stopError error
		if !session.stopped {
			_, stopError = session.control(cleanupContext, pollStop)
		}
		_, releaseError := session.control(cleanupContext, pollRelease)
		failure = errors.Join(stopError, releaseError)
	}
	failure = errors.Join(failure, session.statistics.Disconnect())
	session.client.Close()
	if failure != nil {
		slog.WarnContext(ctx, "release VPP queue profiling session failed", "error", failure)
	}
	return failure
}
