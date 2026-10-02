package wanstate

import (
	"fmt"
	"log/slog"
	"slices"

	"goodkind.io/mwan/internal/observation/contract"
)

// CheckTransition retains the failed or recovered observation in bounded history.
type CheckTransition struct {
	ID       string
	Previous string
	Result   contract.Result
}

// CheckState serves application evidence separately from provider eligibility.
type CheckState struct {
	IntervalSeconds int
	Check           contract.CheckSpec
	Result          contract.Result
	Recent          []CheckTransition
}

// ConfigureCheck initializes unknown evidence before the first execution completes.
func (s *Store) ConfigureCheck(spec contract.CheckSpec, intervalSeconds int) error {
	copied, err := contract.CloneCheck(spec)
	if err != nil {
		slog.Error("observation definition could not be copied", "check_id", spec.ID, "err", err)
		return fmt.Errorf("copy configured observation: %w", err)
	}
	var initial contract.Result
	initial.CheckID = spec.ID
	initial.Dimension = spec.Dimension
	initial.Operation = spec.Operation
	initial.Target = spec.Target
	initial.Family = spec.Family
	initial.Observer = spec.Observer
	initial.Availability = contract.AvailabilityMissing
	initial.Outcome = contract.OutcomeUnknown
	initial.Reason = "no observation completed"
	s.mu.Lock()
	s.checks[spec.ID] = CheckState{Check: copied, IntervalSeconds: intervalSeconds, Recent: nil, Result: initial}
	s.mu.Unlock()
	return nil
}

// RecordCheck retains completed replies while recording freshness transitions separately.
func (s *Store) RecordCheck(spec contract.CheckSpec, result contract.Result) error {
	check, err := contract.CloneCheck(spec)
	if err != nil {
		slog.Error("observation definition could not be copied", "check_id", spec.ID, "err", err)
		return fmt.Errorf("copy check definition: %w", err)
	}
	copied, err := contract.CloneResult(result)
	if err != nil {
		slog.Error("observation result could not be copied", "check_id", spec.ID, "err", err)
		return fmt.Errorf("copy check result: %w", err)
	}
	s.mu.Lock()
	current := s.checks[spec.ID]
	previous := string(current.Result.Availability) + "/" + string(current.Result.Outcome)
	if len(current.Recent) != 0 {
		last := current.Recent[len(current.Recent)-1].Result
		previous = string(last.Availability) + "/" + string(last.Outcome)
	}
	verdict := string(copied.Availability) + "/" + string(copied.Outcome)
	var transition *CheckTransition
	if previous != verdict {
		s.transitionSequence++
		value := CheckTransition{ID: fmt.Sprintf("%s:%d", s.runID, s.transitionSequence), Previous: previous, Result: copied}
		current.Recent = append(current.Recent, value)
		if len(current.Recent) > RecentTransitionLimit {
			current.Recent = slices.Clone(current.Recent[len(current.Recent)-RecentTransitionLimit:])
		}
		transition = &value
	}
	current.Check = check
	// Freshness events retain the completed reply in storage; reads classify a copy.
	if copied.Availability != contract.AvailabilityStale || current.Result.ObservedAt.IsZero() {
		current.Result = copied
	}
	s.checks[spec.ID] = current
	logger := s.transitionLog
	s.mu.Unlock()
	if transition != nil && logger != nil {
		logger.Info("application observation transition", "transition_id", transition.ID, "previous", transition.Previous,
			"check_id", spec.ID, "connection_id", spec.ConnectionID, "result", transition.Result)
	}
	return nil
}

func cloneCheckState(value CheckState) (CheckState, error) {
	var copied CheckState
	check, err := contract.CloneCheck(value.Check)
	if err != nil {
		slog.Error("stored observation definition could not be copied", "check_id", value.Check.ID, "err", err)
		return copied, fmt.Errorf("copy stored observation definition: %w", err)
	}
	result, err := contract.CloneResult(value.Result)
	if err != nil {
		slog.Error("stored observation result could not be copied", "check_id", value.Check.ID, "err", err)
		return copied, fmt.Errorf("copy stored observation result: %w", err)
	}
	copied = CheckState{Check: check, Result: result, IntervalSeconds: value.IntervalSeconds, Recent: make([]CheckTransition, 0, len(value.Recent))}
	for _, transition := range value.Recent {
		result, err := contract.CloneResult(transition.Result)
		if err != nil {
			slog.Error("stored observation transition could not be copied", "check_id", value.Check.ID, "err", err)
			return copied, fmt.Errorf("copy stored observation transition: %w", err)
		}
		copied.Recent = append(copied.Recent, CheckTransition{ID: transition.ID, Previous: transition.Previous, Result: result})
	}
	return copied, nil
}
