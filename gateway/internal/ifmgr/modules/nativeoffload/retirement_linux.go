//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"log/slog"

	"goodkind.io/mwan/internal/offload"
)

func (module *Module) logRuleCounters(ctx context.Context, level slog.Level, message string, entry *admittedFlow, reason string, failure error) {
	log := module.ruleCounterLogger(entry, reason)
	if failure != nil {
		log = log.With("err", failure)
	}
	log.Log(ctx, level, message)
}

func (module *Module) ruleCounterLogger(entry *admittedFlow, reason string) *slog.Logger {
	counters, sampledAt, sampled := entry.counters.Latest()
	attributes := []any{
		slog.Uint64("connection_id", uint64(entry.flow.ID)), slog.Uint64("connection_zone", uint64(entry.flow.Zone)),
		slog.Any("original_tuple", entry.flow.TupleOrig), slog.Any("reply_tuple", entry.flow.TupleReply),
		slog.String("wan", entry.wan), slog.String("reason", reason), slog.Any("handle", entry.result.Handle),
		slog.Uint64("policy_generation", entry.spec.PolicyGeneration), slog.Any("result", entry.result),
		slog.Bool("cleanup_uncertain", entry.uncertain), slog.String("counter_scope", "current_rule_set"),
		slog.Bool("counter_sample_valid", sampled),
		slog.Group("hardware_counters",
			slog.Any("original", nativeCounterLogValue(entry.spec.RequestedDirections&1 != 0, counters[0], sampled)),
			slog.Any("reply", nativeCounterLogValue(entry.spec.RequestedDirections&2 != 0, counters[1], sampled))),
	}
	if sampled {
		attributes = append(attributes,
			slog.String("counter_observation", "latest_complete_hardware"), slog.Time("counter_sampled_at", sampledAt))
	}
	return module.Log.With(attributes...)
}

func nativeCounterLogValue(requested bool, counter offload.NativeCounter, sampled bool) slog.Value {
	valid := requested && sampled
	attributes := []slog.Attr{slog.Bool("requested", requested), slog.Bool("sample_valid", valid)}
	if valid {
		attributes = append(attributes,
			slog.Bool("packets_valid", counter.PacketsValid), slog.Bool("bytes_valid", counter.BytesValid),
			slog.Uint64("observed_monotonic_ns", counter.ObservedMonotonicNS),
			slog.Uint64("packets", counter.Packets), slog.Uint64("bytes", counter.Bytes))
	}
	return slog.GroupValue(attributes...)
}
