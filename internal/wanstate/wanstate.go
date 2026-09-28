// Package wanstate holds the live steering state the management surface
// serves. Modules write a snapshot of what they just reconciled; the
// operational-datastore provider reads it at request time. The store is
// the seam between the two: writers never wait on a reader, readers
// never touch a reconcile lock, and a reader always sees a complete
// snapshot from some recent pass rather than a half-written one.
package wanstate

import (
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"goodkind.io/mwan/internal/clock"
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
	V4Ready  bool
	V6Ready  bool
	// OwnedAddresses are the mapped addresses the routing module holds on the
	// member's link as host addresses, in configuration order.
	OwnedAddresses []netip.Addr
}

// FamilyTranslation records the last realized translation result for one
// address family. Native mode is ready without a kernel translation rule.
type FamilyTranslation struct {
	Mode           string
	Ready          bool
	Reason         string
	InternalPrefix netip.Prefix
	ExternalPrefix netip.Prefix
}

// MemberTranslation records IPv4 and IPv6 results independently.
type MemberTranslation struct {
	V4 FamilyTranslation
	V6 FamilyTranslation
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

// OwnedRuleset is one writer's last intended rules and reconciliation error.
type OwnedRuleset struct {
	Rules string
	Error string
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
	mu                  sync.RWMutex
	clock               clock.Clock
	connections         map[string]ConnectionState
	providerConnections map[string]string
	runID               string
	transitionSequence  uint64
	transitionLog       *slog.Logger
	health              map[string]MemberHealth
	routing             map[string]MemberRouting
	translation         map[string]MemberTranslation
	routingGeneration   uint64
	activeTier          uint8
	tierValid           bool
	bgp                 BGP
	intendedRulesets    map[string]OwnedRuleset
	observer            Observer
	observerGeneration  uint64
}

// New returns an empty store.
func New() *Store {
	return NewWithClock(clock.Real{})
}

// NewWithClock constructs a store with an injected wall clock.
func NewWithClock(wallClock clock.Clock) *Store {
	return &Store{
		mu:                  sync.RWMutex{},
		clock:               wallClock,
		connections:         map[string]ConnectionState{},
		providerConnections: map[string]string{},
		runID:               "",
		transitionSequence:  0,
		transitionLog:       nil,
		health:              map[string]MemberHealth{},
		routing:             map[string]MemberRouting{},
		translation:         map[string]MemberTranslation{},
		routingGeneration:   0,
		activeTier:          0,
		tierValid:           false,
		bgp:                 BGP{Peers: nil, ReadAt: time.Time{}, Reached: false},
		intendedRulesets:    map[string]OwnedRuleset{},
		observer:            nil,
		observerGeneration:  0,
	}
}

// Observe registers the store's one observer. Set it before the modules
// start writing; events from earlier writes are not replayed. The returned
// function removes this registration without replacing a newer observer.
func (s *Store) Observe(observer Observer) func() {
	s.mu.Lock()
	s.observerGeneration++
	generation := s.observerGeneration
	s.observer = observer
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.observerGeneration == generation {
			s.observer = nil
		}
		s.mu.Unlock()
	}
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
	type routingTransition struct {
		id    string
		value Transition
	}
	var transitions []routingTransition
	copied := make(map[string]MemberRouting, len(members))
	for id, member := range members {
		member.OwnedAddresses = slices.Clone(member.OwnedAddresses)
		copied[id] = member
	}
	s.mu.Lock()
	previousTier := s.activeTier
	previousValid := s.tierValid
	s.activeTier = activeTier
	s.tierValid = true
	s.routing = copied
	for provider, member := range members {
		id, mapped := s.providerConnections[provider]
		if !mapped {
			continue
		}
		connection, present := s.connections[id]
		if !present {
			continue
		}
		oldV4, oldV6 := connection.IPv4.Routing, connection.IPv6.Routing
		connection.IPv4.Routing = "not-ready"
		connection.IPv6.Routing = "not-ready"
		if member.V4Ready {
			connection.IPv4.Routing = "ready"
		}
		if member.V6Ready {
			connection.IPv6.Routing = "ready"
		}
		for _, change := range []struct{ family, previous, current string }{
			{"ipv4", oldV4, connection.IPv4.Routing}, {"ipv6", oldV6, connection.IPv6.Routing},
		} {
			if change.previous != "unknown" && change.previous != change.current {
				value := s.addTransitionLocked(&connection, change.family, change.previous, change.current, "evaluate-routing", "wan-routes", "routing readiness changed", s.clock.Now())
				transitions = append(transitions, routingTransition{id: id, value: value})
			}
		}
		s.connections[id] = connection
	}
	s.routingGeneration++
	observer := s.observer
	transitionLog := s.transitionLog
	s.mu.Unlock()
	for _, change := range transitions {
		logOwnershipTransition(transitionLog, change.id, &change.value)
	}
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

// SetIntendedRuleset writes the NPT module's intended ruleset without an apply error.
func (s *Store) SetIntendedRuleset(text string) {
	s.SetOwnedIntendedRuleset("npt", text, nil)
}

// SetOwnedIntendedRuleset replaces one writer's intent and last error.
func (s *Store) SetOwnedIntendedRuleset(owner string, text string, reconcileError error) {
	state := OwnedRuleset{Rules: text, Error: ""}
	if reconcileError != nil {
		state.Error = reconcileError.Error()
	}
	s.mu.Lock()
	s.intendedRulesets[owner] = state
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
	Connections       map[string]ConnectionState
	Health            map[string]MemberHealth
	Routing           map[string]MemberRouting
	RoutingGeneration uint64
	Translation       map[string]MemberTranslation
	ActiveTier        uint8
	TierValid         bool
	BGP               BGP
	IntendedRuleset   string
	IntendedByOwner   map[string]OwnedRuleset
}

// Snapshot returns a copy the caller may read without further locking.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := Snapshot{
		Connections:       make(map[string]ConnectionState, len(s.connections)),
		Health:            make(map[string]MemberHealth, len(s.health)),
		Routing:           make(map[string]MemberRouting, len(s.routing)),
		RoutingGeneration: s.routingGeneration,
		Translation:       make(map[string]MemberTranslation, len(s.translation)),
		ActiveTier:        s.activeTier,
		TierValid:         s.tierValid,
		BGP:               s.bgp,
		IntendedRuleset:   renderIntendedRulesets(s.intendedRulesets),
		IntendedByOwner:   make(map[string]OwnedRuleset, len(s.intendedRulesets)),
	}
	maps.Copy(snap.Health, s.health)
	for id, connection := range s.connections {
		snap.Connections[id] = cloneConnection(connection)
	}
	maps.Copy(snap.Routing, s.routing)
	for id, member := range snap.Routing {
		member.OwnedAddresses = slices.Clone(member.OwnedAddresses)
		snap.Routing[id] = member
	}
	maps.Copy(snap.Translation, s.translation)
	maps.Copy(snap.IntendedByOwner, s.intendedRulesets)
	peers := make([]BGPPeer, len(s.bgp.Peers))
	copy(peers, s.bgp.Peers)
	snap.BGP.Peers = peers
	return snap
}

func renderIntendedRulesets(owners map[string]OwnedRuleset) string {
	if len(owners) == 0 {
		return ""
	}
	keys := make([]string, 0, len(owners))
	for owner := range owners {
		keys = append(keys, owner)
	}
	sort.Strings(keys)
	var output strings.Builder
	for _, owner := range keys {
		state := owners[owner]
		output.WriteString(owner + ":\n")
		output.WriteString(state.Rules)
		if !strings.HasSuffix(state.Rules, "\n") {
			output.WriteByte('\n')
		}
		if state.Error != "" {
			output.WriteString("error: " + state.Error + "\n")
		}
	}
	return output.String()
}
