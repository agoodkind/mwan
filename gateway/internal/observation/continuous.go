package observation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/observation/contract"
)

const continuousSettingsLimit = 1024 * 1024

// ScheduledCheck waits its interval after each bounded execution completes.
type ScheduledCheck struct {
	Check           CheckSpec `json:"check"`
	IntervalSeconds int       `json:"interval_seconds"`
}

// ContinuousSettings uses the same executor identities and checks as deployment acceptance.
type ContinuousSettings struct {
	Runtime RuntimeConfig    `json:"runtime"`
	Checks  []ScheduledCheck `json:"checks"`
}

// CheckCounts separates completed verdicts from availability and recovery transitions.
type CheckCounts struct {
	Pass        uint64 `json:"pass"`
	Fail        uint64 `json:"fail"`
	Unknown     uint64 `json:"unknown"`
	Failures    uint64 `json:"failures"`
	Unavailable uint64 `json:"unavailable"`
	Recoveries  uint64 `json:"recoveries"`
}

// Summary records every consumed verdict before worker cancellation completes.
type Summary struct {
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt time.Time              `json:"finished_at"`
	Checks     map[string]CheckCounts `json:"checks"`
}

// LoadContinuousSettings rejects unknown fields and implicit runtime identities.
func LoadContinuousSettings(path string) (ContinuousSettings, error) {
	var settings ContinuousSettings
	if !filepath.IsAbs(path) {
		return settings, errors.New("observation settings path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		slog.Warn("recurring observation settings could not be opened", "path", path, "err", err)
		return settings, fmt.Errorf("open observation settings: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > continuousSettingsLimit {
		return settings, errors.New("observation settings require a regular file below 1 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, continuousSettingsLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		slog.Warn("recurring observation settings could not be decoded", "path", path, "err", err)
		return settings, fmt.Errorf("decode observation settings: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return settings, errors.New("observation settings require exactly one JSON object")
	}
	return settings, settings.Validate()
}

// Validate requires distinct checks, bounded schedules, and explicit executable identities.
func (settings ContinuousSettings) Validate() error {
	if len(settings.Checks) == 0 {
		return errors.New("recurring observation checks are required")
	}
	ids := make(map[string]bool, len(settings.Checks))
	for _, scheduled := range settings.Checks {
		if strings.ContainsAny(scheduled.Check.ID, "'\"") {
			return errors.New("recurring check IDs cannot contain XPath quotation marks")
		}
		if scheduled.IntervalSeconds <= 0 || ids[scheduled.Check.ID] {
			return errors.New("recurring checks require positive intervals and unique IDs")
		}
		if err := Validate(scheduled.Check); err != nil {
			slog.Warn("recurring observation check rejected", "check_id", scheduled.Check.ID, "err", err)
			return fmt.Errorf("check %q: %w", scheduled.Check.ID, err)
		}
		ids[scheduled.Check.ID] = true
	}
	if settings.Runtime.MachineIDPath == "" || settings.Runtime.ProbeBinary == "" {
		return errors.New("recurring observations require identity and probe executable paths")
	}
	for _, path := range []string{settings.Runtime.MachineIDPath, settings.Runtime.ProbeBinary} {
		if !filepath.IsAbs(path) {
			return errors.New("recurring identity and executable paths must be absolute")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("recurring identity or executable is unavailable")
		}
		if path == settings.Runtime.ProbeBinary && info.Mode().Perm()&0o111 == 0 {
			return errors.New("recurring probe binary is not executable")
		}
	}
	if settings.Runtime.CloudflareTokenFile != "" && !filepath.IsAbs(settings.Runtime.CloudflareTokenFile) {
		return errors.New("recurring credential file path must be absolute")
	}
	return nil
}

type checkCompletion struct {
	spec   CheckSpec
	result Result
	err    error
}

// RunContinuous joins independent workers before returning the terminal counts.
func RunContinuous(ctx context.Context, settings ContinuousSettings, notifier notify.Notifier, log *slog.Logger, consume func(CheckSpec, Result) error) (Summary, error) {
	var summary Summary
	summary.StartedAt = internalclock.Real{}.Now().UTC()
	summary.Checks = make(map[string]CheckCounts, len(settings.Checks))
	if err := settings.Validate(); err != nil {
		return summary, err
	}
	if consume == nil || notifier == nil {
		return summary, errors.New("recurring observations require a consumer and notifier")
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	executor := NewExecutor(settings.Runtime, log)
	completions := make(chan checkCompletion, len(settings.Checks))
	var workers sync.WaitGroup
	for _, scheduled := range settings.Checks {
		copied, err := contract.CloneCheck(scheduled.Check)
		if err != nil {
			log.ErrorContext(ctx, "recurring check could not be copied", "check_id", scheduled.Check.ID, "err", err)
			cancel()
			workers.Wait()
			return summary, fmt.Errorf("copy recurring check: %w", err)
		}
		scheduled.Check = copied
		var counts CheckCounts
		summary.Checks[scheduled.Check.ID] = counts
		workers.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					err := fmt.Errorf("recurring check panicked: %v", recovered)
					log.ErrorContext(runContext, "recurring observation worker panicked", "check_id", scheduled.Check.ID, "err", err)
					var missing Result
					select {
					case completions <- checkCompletion{spec: scheduled.Check, result: missing, err: err}:
					case <-runContext.Done():
					}
				}
			}()
			runScheduledCheck(runContext, executor, scheduled, completions)
		})
	}
	go func() {
		defer close(completions)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(runContext, "recurring observation join panicked", "err", recovered)
				cancel()
			}
		}()
		workers.Wait()
	}()
	return consumeContinuous(runContext, cancel, log, notifier, completions, consume, summary)
}

func consumeContinuous(ctx context.Context, cancel context.CancelFunc, log *slog.Logger, notifier notify.Notifier, completions <-chan checkCompletion, consume func(CheckSpec, Result) error, summary Summary) (Summary, error) {
	latest := make(map[string]checkCompletion, len(summary.Checks))
	states := make(map[string]string, len(summary.Checks))
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var consumerError error
	for {
		select {
		case completed, open := <-completions:
			if !open {
				summary.FinishedAt = internalclock.Real{}.Now().UTC()
				return summary, consumerError
			}
			if completed.err != nil {
				consumerError = completed.err
				cancel()
				continue
			}
			latest[completed.spec.ID] = completed
			if consumerError == nil {
				consumerError = consumeCopiedCheck(log, completed, consume)
				if consumerError != nil {
					cancel()
				}
			}
			recordContinuousVerdict(ctx, completed, notifier, states, &summary)
			resetFreshnessTimer(timer, latest, states)
		case <-timer.C:
			if consumerError == nil {
				consumerError = consumeStaleChecks(ctx, log, notifier, latest, states, &summary, consume)
				if consumerError != nil {
					cancel()
				}
			}
			resetFreshnessTimer(timer, latest, states)
		}
	}
}

func consumeStaleChecks(ctx context.Context, log *slog.Logger, notifier notify.Notifier, latest map[string]checkCompletion, states map[string]string, summary *Summary, consume func(CheckSpec, Result) error) error {
	for id, completed := range latest {
		fresh := contract.FreshResult(completed.spec, completed.result, internalclock.Real{}.Now())
		if fresh.Availability != AvailabilityStale || states[id] == "stale/unknown" {
			continue
		}
		completed.result = fresh
		if err := consumeCopiedCheck(log, completed, consume); err != nil {
			return err
		}
		recordContinuousVerdict(ctx, completed, notifier, states, summary)
	}
	return nil
}

func consumeCopiedCheck(log *slog.Logger, completed checkCompletion, consume func(CheckSpec, Result) error) error {
	spec, err := contract.CloneCheck(completed.spec)
	if err != nil {
		log.Error("recurring check could not be copied", "check_id", completed.spec.ID, "err", err)
		return fmt.Errorf("copy recurring check: %w", err)
	}
	result, err := contract.CloneResult(completed.result)
	if err != nil {
		log.Error("recurring result could not be copied", "check_id", completed.spec.ID, "err", err)
		return fmt.Errorf("copy recurring result: %w", err)
	}
	return consume(spec, result)
}

func runScheduledCheck(ctx context.Context, executor *Executor, scheduled ScheduledCheck, completed chan<- checkCompletion) {
	for ctx.Err() == nil {
		result := executor.Run(ctx, scheduled.Check)
		select {
		case completed <- checkCompletion{spec: scheduled.Check, result: result, err: nil}:
		case <-ctx.Done():
			return
		}
		timer := time.NewTimer(time.Duration(scheduled.IntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func resetFreshnessTimer(timer *time.Timer, latest map[string]checkCompletion, states map[string]string) {
	var duration time.Duration
	now := internalclock.Real{}.Now()
	for _, completed := range latest {
		if states[completed.spec.ID] == "stale/unknown" || completed.result.ObservedAt.IsZero() {
			continue
		}
		expiry := completed.result.ObservedAt.Add(time.Duration(completed.spec.MaxAgeSeconds) * time.Second)
		remaining := expiry.Sub(now)
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		if duration == 0 || remaining < duration {
			duration = remaining
		}
	}
	if duration == 0 {
		timer.Stop()
		return
	}
	timer.Reset(duration)
}

func recordContinuousVerdict(ctx context.Context, completed checkCompletion, notifier notify.Notifier, states map[string]string, summary *Summary) {
	result := contract.FreshResult(completed.spec, completed.result, internalclock.Real{}.Now())
	counts := summary.Checks[result.CheckID]
	verdict := string(result.Availability) + "/" + string(result.Outcome)
	fields := []slog.Attr{slog.Any("result", result), slog.String("connection_id", completed.spec.ConnectionID)}
	passed := RequiredPassed(completed.spec, result, internalclock.Real{}.Now())
	switch {
	case passed:
		counts.Pass++
	case result.Availability == AvailabilityComplete && result.Outcome == OutcomeFail:
		counts.Fail++
	default:
		counts.Unknown++
	}
	previous := states[result.CheckID]
	if previous == verdict {
		summary.Checks[result.CheckID] = counts
		return
	}
	if passed && previous != "" && previous != "complete/pass" {
		counts.Recoveries++
	}
	if !passed {
		if result.Availability == AvailabilityComplete && result.Outcome == OutcomeFail {
			counts.Failures++
		} else {
			counts.Unavailable++
		}
	}
	notifyContinuousTransition(ctx, notifier, result, passed, fields)
	states[result.CheckID] = verdict
	summary.Checks[result.CheckID] = counts
}

func notifyContinuousTransition(ctx context.Context, notifier notify.Notifier, result Result, passed bool, fields []slog.Attr) {
	if ctx.Err() != nil {
		return
	}
	if passed {
		notifier.Resolve(ctx, "observation_failed", result.CheckID, "Required application observation recovered", fields...)
		notifier.Resolve(ctx, "observation_unavailable", result.CheckID, "Required observation evidence recovered", fields...)
		return
	}
	kind := "observation_unavailable"
	if result.Availability == AvailabilityComplete && result.Outcome == OutcomeFail {
		kind = "observation_failed"
	}
	notifier.Notify(ctx, notify.Event{Now: internalclock.Real{}.Now().UTC(), Level: slog.LevelWarn, Kind: kind, Key: result.CheckID, Message: result.Reason, Fields: fields, IsRecovery: false})
}
