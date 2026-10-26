package wanstate

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"goodkind.io/mwan/internal/nptpolicy"
)

// NPTGuard removes stale rules and fences admission for the changed connections.
// Its completion function releases that fence after the writer publishes the result.
type NPTGuard func(context.Context, []string) (func(), error)

type nptGuardRegistration struct {
	guard NPTGuard
}

// RegisterNPTGuard rejects a second owner. Unregistration waits for active changes.
func (s *Store) RegisterNPTGuard(guard NPTGuard) (func(), error) {
	if guard == nil {
		return nil, fmt.Errorf("NPT mutation guard is required")
	}
	s.nptGuardMu.Lock()
	defer s.nptGuardMu.Unlock()
	if s.nptGuard != nil {
		return nil, fmt.Errorf("NPT mutation guard already has an owner")
	}
	registration := &nptGuardRegistration{guard: guard}
	s.nptGuard = registration
	var once sync.Once
	return func() {
		once.Do(func() {
			s.nptGuardMu.Lock()
			defer s.nptGuardMu.Unlock()
			if s.nptGuard == registration {
				s.nptGuard = nil
			}
		})
	}, nil
}

// BeginNPTChange keeps registration stable until the returned completion runs.
// Callbacks may read Snapshot; the writer must publish success or failed readiness
// before completion. An unchanged semantic policy does not acquire an admission fence.
func (s *Store) BeginNPTChange(ctx context.Context, next map[string]nptpolicy.PrefixPair) (changed []string, finish func(), resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "NPT mutation guard rejected policy change", "connections", changed, "err", resultErr)
		}
	}()
	s.nptGuardMu.RLock()
	locked := true
	defer func() {
		if locked {
			s.nptGuardMu.RUnlock()
		}
	}()
	s.mu.RLock()
	changed = changedNPTPolicies(s.nptPolicies, next)
	s.mu.RUnlock()
	if len(changed) == 0 {
		return nil, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return changed, nil, fmt.Errorf("begin NPT policy change: %w", err)
	}
	var release func()
	if s.nptGuard != nil {
		var err error
		release, err = s.nptGuard.guard(ctx, slices.Clone(changed))
		if err != nil {
			if release != nil {
				release()
			}
			return changed, nil, fmt.Errorf("prepare NPT policy change: %w", err)
		}
		if release == nil {
			return changed, nil, fmt.Errorf("NPT mutation guard did not supply its completion function")
		}
	}
	if err := ctx.Err(); err != nil {
		if release != nil {
			release()
		}
		return changed, nil, fmt.Errorf("prepare NPT policy change: %w", err)
	}
	var once sync.Once
	finish = func() {
		once.Do(func() {
			defer s.nptGuardMu.RUnlock()
			if release != nil {
				release()
			}
		})
	}
	locked = false
	return changed, finish, nil
}

// SetNPTTranslation commits readiness and the applied pairs in one snapshot.
// Only a semantic pair change advances the policy generation.
func (s *Store) SetNPTTranslation(members map[string]MemberTranslation, pairs map[string]nptpolicy.PrefixPair) bool {
	copiedMembers := maps.Clone(members)
	copiedPairs := cloneNPTPolicies(pairs)
	s.mu.Lock()
	defer s.mu.Unlock()
	policyChanged := len(changedNPTPolicies(s.nptPolicies, copiedPairs)) != 0
	changed := policyChanged || !maps.Equal(s.translation, copiedMembers)
	s.translation = copiedMembers
	s.nptPolicies = copiedPairs
	if policyChanged {
		s.nptPolicyGeneration++
	}
	return changed
}

func cloneNPTPolicies(pairs map[string]nptpolicy.PrefixPair) map[string]nptpolicy.PrefixPair {
	copied := make(map[string]nptpolicy.PrefixPair, len(pairs))
	for id, pair := range pairs {
		pair.SourceExceptions = slices.Clone(pair.SourceExceptions)
		pair.DestinationExceptions = slices.Clone(pair.DestinationExceptions)
		copied[id] = pair
	}
	return copied
}

func changedNPTPolicies(previous, next map[string]nptpolicy.PrefixPair) []string {
	var changed []string
	for id, pair := range previous {
		current, found := next[id]
		if !found || !nptpolicy.EqualPair(pair, current) {
			changed = append(changed, id)
		}
	}
	for id := range next {
		if _, found := previous[id]; !found {
			changed = append(changed, id)
		}
	}
	slices.Sort(changed)
	return changed
}
