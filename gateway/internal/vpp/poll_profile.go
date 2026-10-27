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
	baseline   QueuePollProfile
	threads    []vlib.ThreadData
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
			slog.WarnContext(ctx, "collect VPP receive queue profile failed", "error", failure)
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
	client, err := Open(ctx, endpoints, &pollapi.VirtioPollProfile{}, &pollapi.VirtioPollProfileReply{}, &vlib.ShowThreads{}, &vlib.ShowThreadsReply{})
	if err != nil {
		return nil, fmt.Errorf("open VPP queue profiling extension: %w", err)
	}
	requestContext, cancel := client.requestContext(ctx)
	threads, err := vlib.NewServiceClient(client.connection).ShowThreads(requestContext, &vlib.ShowThreads{})
	cancel()
	if err != nil {
		client.Close()
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
		return nil, fmt.Errorf("connect VPP queue statistics: %w", err)
	}
	var tokenBytes [8]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		cleanupError := statistics.Disconnect()
		client.Close()
		return nil, errors.Join(fmt.Errorf("create VPP queue profile owner token: %w", err), cleanupError)
	}
	token := binary.BigEndian.Uint64(tokenBytes[:])
	if token == 0 {
		cleanupError := statistics.Disconnect()
		client.Close()
		return nil, errors.Join(fmt.Errorf("VPP queue profile owner token must be nonzero"), cleanupError)
	}
	return &queuePollSession{client: client, statistics: statistics, generation: generation, token: token, threads: threads.ThreadData}, nil
}

func (session *queuePollSession) control(ctx context.Context, action uint8) (*pollapi.VirtioPollProfileReply, error) {
	requestContext, cancel := session.client.requestContext(ctx)
	defer cancel()
	reply, err := pollapi.NewServiceClient(session.client.connection).VirtioPollProfile(requestContext, &pollapi.VirtioPollProfile{Session: session.token, Action: action})
	if err != nil {
		return nil, fmt.Errorf("VPP queue profile action %d: %w", action, err)
	}
	if reply.Session != session.token {
		return nil, fmt.Errorf("VPP queue profile action %d returned another session", action)
	}
	return reply, nil
}

func (session *queuePollSession) start(ctx context.Context) error {
	// A timed out start can still acquire ownership. Cleanup must attempt this token.
	session.started = true
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
		return QueuePollProfile{}, fmt.Errorf("read VPP queue profiling snapshot: %w", err)
	}
	return decodeQueuePoll(entries, reply, session.threads)
}

func decodeQueuePoll(entries []adapter.StatEntry, reply *pollapi.VirtioPollProfileReply, threads []vlib.ThreadData) (QueuePollProfile, error) {
	result := QueuePollProfile{Session: reply.Session, Sequence: reply.Sequence}
	var metadata, counters, scales adapter.SimpleCounterStat
	var names adapter.NameStat
	for _, entry := range entries {
		switch pollStatistic(entry.Name) {
		case pollMetadata:
			metadata, _ = entry.Data.(adapter.SimpleCounterStat)
		case pollData:
			counters, _ = entry.Data.(adapter.SimpleCounterStat)
		case pollNames:
			names, _ = entry.Data.(adapter.NameStat)
		case pollScales:
			scales, _ = entry.Data.(adapter.SimpleCounterStat)
		}
	}
	if len(metadata) != 1 || len(metadata[0]) != pollFields || len(names) == 0 || len(counters) != len(threads) || len(scales) != len(threads) {
		return result, fmt.Errorf("VPP queue profiling snapshot omits required metadata, queues or thread counters")
	}
	meta := metadata[0]
	if uint64(meta[0]) != reply.Session || uint64(meta[1]) != reply.Sequence || uint64(meta[2]) != reply.ElapsedNs || (meta[3] != 0) != reply.Active || (meta[4] != 0) != reply.Valid || uint64(meta[5]) != uint64(len(names)) || meta[3] > 1 || meta[4] > 1 {
		return result, fmt.Errorf("VPP queue profiling snapshot differs from its binary API receipt")
	}
	if !reply.Valid {
		return result, fmt.Errorf("VPP queue topology changed during profiling")
	}
	if reply.ElapsedNs > math.MaxInt64 {
		return result, fmt.Errorf("VPP queue profiling duration exceeds the supported range")
	}
	result.Duration = time.Duration(reply.ElapsedNs)
	seenThreads := make(map[uint32]bool, len(threads))
	for _, thread := range threads {
		if uint64(thread.ID) >= uint64(len(counters)) || seenThreads[thread.ID] {
			return result, fmt.Errorf("VPP queue profile omits or repeats thread %d", thread.ID)
		}
		seenThreads[thread.ID] = true
		if len(scales[thread.ID]) != 1 {
			return result, fmt.Errorf("VPP queue clock scale omits thread %d", thread.ID)
		}
		// Counter vectors preserve the IEEE 754 bits without scalar integer truncation.
		scale := math.Float64frombits(uint64(scales[thread.ID][0]))
		if math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
			return result, fmt.Errorf("VPP queue clock scale for thread %d is invalid", thread.ID)
		}
		values := counters[thread.ID]
		if len(values) != len(names)*pollFields {
			return result, fmt.Errorf("VPP queue profile thread %d has an incomplete counter vector", thread.ID)
		}
		seenQueues := make(map[string]bool, len(names))
		for queue, raw := range names {
			name := strings.TrimRight(string(raw), "\x00")
			identity := strings.Split(name, "/")
			if len(identity) != 2 || seenQueues[name] {
				return result, fmt.Errorf("VPP queue profile contains an invalid or repeated queue %q", name)
			}
			seenQueues[name] = true
			index, indexError := strconv.ParseUint(identity[0], 10, 32)
			queueID, queueError := strconv.ParseUint(identity[1], 10, 16)
			if err := errors.Join(indexError, queueError); err != nil {
				return result, fmt.Errorf("parse VPP queue identity %q: %w", name, err)
			}
			value := values[queue*pollFields : (queue+1)*pollFields]
			counter := QueuePollCounter{ThreadID: thread.ID, InterfaceIndex: uint32(index), QueueID: uint16(queueID), SecondsPerTick: scale, EmptyCalls: uint64(value[0]), EmptyTicks: uint64(value[1]), NonemptyCalls: uint64(value[2]), NonemptyTicks: uint64(value[3]), Packets: uint64(value[4]), RefillTicks: uint64(value[5])}
			var err error
			counter.EmptyElapsed, err = tickDuration(counter.EmptyTicks, scale)
			if err != nil {
				return result, err
			}
			counter.NonemptyElapsed, err = tickDuration(counter.NonemptyTicks, scale)
			if err != nil {
				return result, err
			}
			counter.RefillElapsed, err = tickDuration(counter.RefillTicks, scale)
			if err != nil {
				return result, err
			}
			result.Queues = append(result.Queues, counter)
		}
	}
	return result, nil
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
		_, stopError := session.control(cleanupContext, pollStop)
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
