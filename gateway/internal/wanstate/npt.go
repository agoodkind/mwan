package wanstate

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"goodkind.io/mwan/gateway/internal/nptpolicy"
)

// BeginNPTChange keeps registration stable until the returned completion runs.
// Callbacks may read Snapshot; the writer must publish success or failed readiness
// before completion. An unchanged semantic policy does not acquire an admission fence.
func (s *Store) BeginNPTChange(
	ctx context.Context,
	next map[string]nptpolicy.PrefixPair,
) (changed []string, finish func(), resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(
				ctx,
				"NPT mutation guard rejected policy change",
				"connections",
				changed,
				"err",
				resultErr,
			)
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

	return changed, func() {}, nil
}

// SetNPTTranslation commits readiness and the applied pairs in one snapshot.
// Only a semantic pair change advances the policy generation.
func (s *Store) SetNPTTranslation(
	members map[string]MemberTranslation,
	pairs map[string]nptpolicy.PrefixPair,
) bool {
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
