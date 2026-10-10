package wanstate

import (
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// PathHistoryLimit bounds the served readiness history of one provider address family.
const PathHistoryLimit = 16

// PathState is the readiness a provider address family reports to an operator.
type PathState string

const (
	// PathStarting means a dependency has no completed evaluation in this daemon run.
	PathStarting PathState = "starting"
	// PathReady means every dependency is met and the family forwards traffic.
	PathReady PathState = "ready"
	// PathUnavailable means an evaluated dependency is not met.
	PathUnavailable PathState = "unavailable"
)

// DependencyState is the evaluation result of one dependency of a provider address family.
type DependencyState string

const (
	// DependencyMet means the evaluation found the dependency satisfied.
	DependencyMet DependencyState = "met"
	// DependencyPending means the first evaluation of the dependency has not completed.
	DependencyPending DependencyState = "pending"
	// DependencyUnmet means the evaluation found the dependency not satisfied.
	DependencyUnmet DependencyState = "unmet"
	// DependencyNotEvaluated means an earlier dependency in the order is not met.
	DependencyNotEvaluated DependencyState = "not-evaluated"
)

// DependencyResult is the state of one named dependency with the reason for a pending or unmet state.
type DependencyResult struct {
	Name   string
	State  DependencyState
	Reason string
}

// PathVerdict is one evaluation, and Dependency and Reason identify the first unmet dependency.
type PathVerdict struct {
	State      PathState
	Dependency string
	Reason     string
}

// PathTransition records one change of the state, the dependency, or the reason.
type PathTransition struct {
	ID       string
	At       time.Time
	Previous PathState
	Current  PathVerdict
}

// PathReadiness is the current verdict of one provider address family, and Since is the start of the current state.
type PathReadiness struct {
	PathVerdict
	Since   time.Time
	History []PathTransition
}

func noPath() PathReadiness {
	return PathReadiness{PathVerdict: PathVerdict{State: "", Dependency: "", Reason: ""}, Since: time.Time{}, History: nil}
}

func familyState(current *ConnectionState, family string) *FamilyState {
	switch addressFamily(family) {
	case familyIPv4:
		return &current.IPv4
	case familyIPv6:
		return &current.IPv6
	}
	return nil
}

func (s *Store) applyPathLocked(state *FamilyState, verdict PathVerdict) *PathTransition {
	if state.Path.PathVerdict == verdict {
		return nil
	}
	s.transitionSequence++
	at := s.clock.Now().UTC()
	transition := PathTransition{
		ID: fmt.Sprintf("%s:%d", s.runID, s.transitionSequence), At: at, Previous: state.Path.State, Current: verdict,
	}
	history := append(slices.Clone(state.Path.History), transition)
	if len(history) > PathHistoryLimit {
		history = history[len(history)-PathHistoryLimit:]
	}
	since := state.Path.Since
	if state.Path.State != verdict.State {
		since = at
	}
	state.Path = PathReadiness{PathVerdict: verdict, Since: since, History: history}
	return &transition
}

func logPathTransition(logger *slog.Logger, id, family string, transition *PathTransition) {
	if logger != nil && transition != nil {
		logger.Info("ifmgr: path readiness transition", "connection_id", id, "family", family, "transition", *transition)
	}
}

// SetPathReadiness records a verdict and appends a transition when the verdict differs from the current one.
func (s *Store) SetPathReadiness(id, family string, verdict PathVerdict) {
	s.mu.Lock()
	current, ok := s.connections[id]
	state := familyState(&current, family)
	if !ok || state == nil {
		s.mu.Unlock()
		return
	}
	transition := s.applyPathLocked(state, verdict)
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logPathTransition(logger, id, family, transition)
}

// SetFamilyReadiness records the forwarding result and the path verdict of one provider address family in one write.
func (s *Store) SetFamilyReadiness(id, family, readiness string, verdict PathVerdict) {
	s.mu.Lock()
	current, ok := s.connections[id]
	state := familyState(&current, family)
	if !ok || state == nil {
		s.mu.Unlock()
		return
	}
	forwarding := s.applyReadinessLocked(&current, state, family, readiness)
	path := s.applyPathLocked(state, verdict)
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logOwnershipTransition(logger, id, forwarding)
	logPathTransition(logger, id, family, path)
}

func (s *Store) applyReadinessLocked(current *ConnectionState, state *FamilyState, family, readiness string) *Transition {
	previous := state.Readiness
	state.Readiness = readiness
	if previous == readiness {
		return nil
	}
	value := s.addTransitionLocked(current, family, previous, readiness, "evaluate-forwarding", "routing", "forwarding readiness changed", s.clock.Now())
	return &value
}
