// Package wanstate holds the live steering state the management surface
// serves. Modules write a snapshot of what they just reconciled; the
// operational-datastore provider reads it at request time. The store is
// the seam between the two: writers never wait on a reader, readers
// never touch a reconcile lock, and a reader always sees a complete
// snapshot from some recent pass rather than a half-written one.
package wanstate

import (
	"maps"
	"net/netip"
	"sync"
	"time"
)

// Health is a member's combined verdict, mirroring the health module's
// states.
type Health string

const (
	// HealthUnknown means no verdict has been recorded.
	HealthUnknown Health = "unknown"
	// HealthHealthy means the member passed its probes.
	HealthHealthy Health = "healthy"
	// HealthUnhealthy means the member failed its probes.
	HealthUnhealthy Health = "unhealthy"
)

// ProbeResult is one family's most recent probe outcome.
type ProbeResult string

const (
	// ProbeNone means no probe has completed yet.
	ProbeNone ProbeResult = "none"
	// ProbePass means the most recent probe succeeded.
	ProbePass ProbeResult = "pass"
	// ProbeFail means the most recent probe failed.
	ProbeFail ProbeResult = "fail"
)

// MemberHealth is what the health module knows about one member.
type MemberHealth struct {
	Verdict             Health
	ConsecutiveFailures uint32
	LastTransition      time.Time
	V4                  ProbeResult
	V6                  ProbeResult
}

// MemberRouting is what the routing module decided for one member on
// its last pass.
type MemberRouting struct {
	Carrying bool
	// OwnedAddresses are the mapped addresses the routing module holds on the
	// member's link as host addresses, in configuration order.
	OwnedAddresses []netip.Addr
}

// MemberTranslation is what the translation module holds for one member.
type MemberTranslation struct {
	// Delegated is the live delegation, zero when the provider holds
	// none.
	Delegated netip.Prefix
	// KernelPresent reports whether the member's translation rules were
	// found in the kernel after the last apply.
	KernelPresent bool
}

// BGPPeer is one routing session as last read from the agent.
type BGPPeer struct {
	Address     string
	Established bool
}

// BGP is the routing-session snapshot with its freshness.
type BGP struct {
	Peers   []BGPPeer
	ReadAt  time.Time
	Reached bool
}

// Observer is told, after a write commits, that the write changed a
// member's verdict or the active tier. The store calls it outside its
// lock, on the writer's goroutine, so an observer must hand the event
// off quickly rather than doing the delivery work inline.
type Observer interface {
	// HealthTransition reports one committed verdict change.
	HealthTransition(member string, from Health, to Health)
	// TierChange reports that a routing pass installed a different
	// active tier than the previous pass.
	TierChange(from uint8, to uint8)
}

// Store is the concurrent snapshot store. The zero value is unusable;
// construct with New.
type Store struct {
	mu              sync.RWMutex
	health          map[string]MemberHealth
	routing         map[string]MemberRouting
	translation     map[string]MemberTranslation
	activeTier      uint8
	tierValid       bool
	bgp             BGP
	intendedRuleset string
	observer        Observer
}

// New returns an empty store.
func New() *Store {
	return &Store{
		mu:              sync.RWMutex{},
		health:          map[string]MemberHealth{},
		routing:         map[string]MemberRouting{},
		translation:     map[string]MemberTranslation{},
		activeTier:      0,
		tierValid:       false,
		bgp:             BGP{Peers: nil, ReadAt: time.Time{}, Reached: false},
		intendedRuleset: "",
		observer:        nil,
	}
}

// Observe registers the store's one observer. Set it before the modules
// start writing; events from earlier writes are not replayed.
func (s *Store) Observe(observer Observer) {
	s.mu.Lock()
	s.observer = observer
	s.mu.Unlock()
}

// NotifyHealthTransition forwards a committed verdict change to the
// observer. The health module calls it at its real transition point,
// after hysteresis, because the store cannot infer transitions from
// snapshot writes alone: the first write after a restart restores
// persisted verdicts without any transition having happened.
func (s *Store) NotifyHealthTransition(member string, from Health, to Health) {
	s.mu.RLock()
	observer := s.observer
	s.mu.RUnlock()
	if observer != nil {
		observer.HealthTransition(member, from, to)
	}
}

// SetHealth replaces the health snapshot for every member in one write.
func (s *Store) SetHealth(members map[string]MemberHealth) {
	copied := make(map[string]MemberHealth, len(members))
	maps.Copy(copied, members)
	s.mu.Lock()
	s.health = copied
	s.mu.Unlock()
}

// SetRouting replaces the routing decision snapshot, the active tier and
// each member's carrying flag, in one write. A write whose tier differs
// from the previous valid write is a tier change and reaches the
// observer; the first valid write only establishes the baseline.
func (s *Store) SetRouting(activeTier uint8, members map[string]MemberRouting) {
	copied := make(map[string]MemberRouting, len(members))
	maps.Copy(copied, members)
	s.mu.Lock()
	previousTier := s.activeTier
	previousValid := s.tierValid
	s.activeTier = activeTier
	s.tierValid = true
	s.routing = copied
	observer := s.observer
	s.mu.Unlock()
	if observer != nil && previousValid && previousTier != activeTier {
		observer.TierChange(previousTier, activeTier)
	}
}

// SetTranslation replaces the translation snapshot in one write.
func (s *Store) SetTranslation(members map[string]MemberTranslation) {
	copied := make(map[string]MemberTranslation, len(members))
	maps.Copy(copied, members)
	s.mu.Lock()
	s.translation = copied
	s.mu.Unlock()
}

// SetIntendedRuleset replaces the rendered text of the firewall rules the
// daemon intends, written by the translation module each reconcile.
func (s *Store) SetIntendedRuleset(text string) {
	s.mu.Lock()
	s.intendedRuleset = text
	s.mu.Unlock()
}

// SetBGP replaces the routing-session snapshot.
func (s *Store) SetBGP(bgp BGP) {
	peers := make([]BGPPeer, len(bgp.Peers))
	copy(peers, bgp.Peers)
	bgp.Peers = peers
	s.mu.Lock()
	s.bgp = bgp
	s.mu.Unlock()
}

// Snapshot is a complete, consistent copy of the store for one read.
type Snapshot struct {
	Health          map[string]MemberHealth
	Routing         map[string]MemberRouting
	Translation     map[string]MemberTranslation
	ActiveTier      uint8
	TierValid       bool
	BGP             BGP
	IntendedRuleset string
}

// Snapshot returns a copy the caller may read without further locking.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := Snapshot{
		Health:          make(map[string]MemberHealth, len(s.health)),
		Routing:         make(map[string]MemberRouting, len(s.routing)),
		Translation:     make(map[string]MemberTranslation, len(s.translation)),
		ActiveTier:      s.activeTier,
		TierValid:       s.tierValid,
		BGP:             s.bgp,
		IntendedRuleset: s.intendedRuleset,
	}
	maps.Copy(snap.Health, s.health)
	maps.Copy(snap.Routing, s.routing)
	maps.Copy(snap.Translation, s.translation)
	peers := make([]BGPPeer, len(s.bgp.Peers))
	copy(peers, s.bgp.Peers)
	snap.BGP.Peers = peers
	return snap
}
