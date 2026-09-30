package wanstate

import (
	"fmt"
	"log/slog"
	"slices"
	"time"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

// RecentTransitionLimit bounds operational history per connection.
const RecentTransitionLimit = 32

type addressFamily string

const (
	familyIPv4 addressFamily = "ipv4"
	familyIPv6 addressFamily = "ipv6"
)

// FamilyState keeps protocol decisions separate from observed kernel state.
type FamilyState struct {
	Acquisition            string
	AssignmentValid        string
	LeasePersistence       string
	leasePersistenceReason string
	Firewall               string
	Routing                string
	Readiness              string
	Assignments            []interfaceintent.Assignment
	LastApply              ApplyResult
	Addresses              []netif.CurrentAddr
	Routes                 []netif.CurrentRoute
}

// ApplyResult records the last operation reported by the component owner.
type ApplyResult struct {
	Operation  string
	Dependency string
	Result     string
	Reason     string
	At         time.Time
}

// PendingRemoval reports failed cleanup for a removed connection.
type PendingRemoval struct {
	ConnectionID string
	Name         string
	Family       string
	Apply        ApplyResult
}

// ReplacePendingRemovals publishes cleanup failures from one completed pass.
func (s *Store) ReplacePendingRemovals(pending []PendingRemoval) {
	next := make(map[string]PendingRemoval, len(pending))
	for _, value := range pending {
		if value.Apply.At.IsZero() {
			value.Apply.At = s.clock.Now()
		}
		next[value.ConnectionID+"/"+value.Family] = value
	}
	s.mu.Lock()
	s.pendingRemovals = next
	s.mu.Unlock()
}

// Transition identifies a state change within one daemon run.
type Transition struct {
	ID         string
	At         time.Time
	Family     string
	Previous   string
	Current    string
	Operation  string
	Dependency string
	Reason     string
}

// ConnectionState is the current ownership and observation for one stable ID.
type ConnectionState struct {
	ID                string
	ConfiguredName    string
	Owner             interfaceintent.Owner
	ActualName        string
	IfIndex           int
	LinkState         string
	Observation       string
	ObservationAt     time.Time
	ObservationReason string
	ObservedAt        time.Time
	LastApply         ApplyResult
	IPv4              FamilyState
	IPv6              FamilyState
	Recent            []Transition
}

func cloneAssignment(value interfaceintent.Assignment) interfaceintent.Assignment {
	cloneTime := func(source *time.Time) *time.Time {
		if source == nil {
			return nil
		}
		copied := *source
		return &copied
	}
	value.RenewAt = cloneTime(value.RenewAt)
	value.RebindAt = cloneTime(value.RebindAt)
	value.PreferredUntil = cloneTime(value.PreferredUntil)
	value.ValidUntil = cloneTime(value.ValidUntil)
	if value.IAID != nil {
		id := *value.IAID
		value.IAID = &id
	}
	if value.Route != nil {
		route := *value.Route
		value.Route = &route
	}
	return value
}

func cloneFamily(value FamilyState) FamilyState {
	value.Addresses = slices.Clone(value.Addresses)
	value.Routes = slices.Clone(value.Routes)
	for i := range value.Routes {
		value.Routes[i].NextHops = slices.Clone(value.Routes[i].NextHops)
	}
	value.Assignments = slices.Clone(value.Assignments)
	for i := range value.Assignments {
		value.Assignments[i] = cloneAssignment(value.Assignments[i])
	}
	return value
}

func cloneConnection(value ConnectionState) ConnectionState {
	value.IPv4 = cloneFamily(value.IPv4)
	value.IPv6 = cloneFamily(value.IPv6)
	value.Recent = slices.Clone(value.Recent)
	return value
}

func newFamilyState() FamilyState {
	return FamilyState{Acquisition: "unknown", AssignmentValid: "unknown", LeasePersistence: "unknown", leasePersistenceReason: "", Firewall: "unknown", Routing: "unknown", Readiness: "unknown", Assignments: nil, LastApply: ApplyResult{Operation: "", Dependency: "", Result: "", Reason: "", At: time.Time{}}, Addresses: nil, Routes: nil}
}

// SetLeasePersistence records lease storage separately from forwarding state.
func (s *Store) SetLeasePersistence(id, family, result, reason string) {
	s.mu.Lock()
	current, ok := s.connections[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	var state *FamilyState
	switch addressFamily(family) {
	case familyIPv4:
		state = &current.IPv4
	case familyIPv6:
		state = &current.IPv6
	default:
		s.mu.Unlock()
		return
	}
	previous := state.LeasePersistence
	state.LeasePersistence = result
	var transition *Transition
	if result != previous || reason != state.leasePersistenceReason {
		value := s.addTransitionLocked(&current, family, previous, result, "persist-lease", "storage", reason, s.clock.Now())
		transition = &value
	}
	state.leasePersistenceReason = reason
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logOwnershipTransition(logger, id, transition)
}

// SetFamilyApplyResult records a family's kernel operation separately from assignment validity.
func (s *Store) SetFamilyApplyResult(id, family string, result ApplyResult) {
	if result.At.IsZero() {
		result.At = s.clock.Now()
	}
	s.mu.Lock()
	current, ok := s.connections[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	var state *FamilyState
	switch addressFamily(family) {
	case familyIPv4:
		state = &current.IPv4
	case familyIPv6:
		state = &current.IPv6
	default:
		s.mu.Unlock()
		return
	}
	previous := state.LastApply.Result
	previousReason := state.LastApply.Reason
	if previous == "" {
		previous = "unknown"
	}
	state.LastApply = result
	state.Routing = result.Result
	var transition *Transition
	if result.Result != previous || result.Reason != previousReason {
		value := s.addTransitionLocked(&current, family, previous, result.Result, result.Operation, result.Dependency, result.Reason, result.At)
		transition = &value
	}
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logOwnershipTransition(logger, id, transition)
}

// SetConnections initializes configured identity without interpreting kernel state as a lease.
func (s *Store) SetConnections(connections []interfaceintent.Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, connection := range connections {
		id := connection.ID.String()
		if _, exists := s.connections[id]; exists {
			continue
		}
		s.connections[id] = ConnectionState{
			ID: id, ConfiguredName: connection.Name, Owner: connection.Owner,
			ActualName: "", IfIndex: 0, LinkState: "unknown", Observation: "unknown", ObservationAt: time.Time{}, ObservationReason: "", ObservedAt: time.Time{},
			LastApply: ApplyResult{Operation: "", Dependency: "", Result: "", Reason: "", At: time.Time{}}, Recent: nil,
			IPv4: newFamilyState(), IPv6: newFamilyState(),
		}
	}
}

// SetAssignment records protocol evidence separately from observed kernel lifetimes.
func (s *Store) SetAssignment(id, family, acquisition, validity string, assignments []interfaceintent.Assignment) {
	s.mu.Lock()
	current, ok := s.connections[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	var state *FamilyState
	switch addressFamily(family) {
	case familyIPv4:
		state = &current.IPv4
	case familyIPv6:
		state = &current.IPv6
	default:
		s.mu.Unlock()
		return
	}
	previous := state.Acquisition + "/" + state.AssignmentValid
	state.Acquisition = acquisition
	state.AssignmentValid = validity
	state.Assignments = cloneFamily(FamilyState{Acquisition: "", AssignmentValid: "", LeasePersistence: "", leasePersistenceReason: "", Firewall: "", Routing: "", Readiness: "", Assignments: assignments, LastApply: ApplyResult{Operation: "", Dependency: "", Result: "", Reason: "", At: time.Time{}}, Addresses: nil, Routes: nil}).Assignments
	var transition *Transition
	if next := acquisition + "/" + validity; next != previous {
		value := s.addTransitionLocked(&current, family, previous, next, "acquire", "protocol", "assignment state changed", s.clock.Now())
		transition = &value
	}
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logOwnershipTransition(logger, id, transition)
}

// SetReadiness records a forwarding result supplied by the family owner.
func (s *Store) SetReadiness(id, family, readiness string) {
	s.mu.Lock()
	current, ok := s.connections[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	var state *FamilyState
	switch addressFamily(family) {
	case familyIPv4:
		state = &current.IPv4
	case familyIPv6:
		state = &current.IPv6
	default:
		s.mu.Unlock()
		return
	}
	previous := state.Readiness
	state.Readiness = readiness
	var transition *Transition
	if previous != readiness {
		value := s.addTransitionLocked(&current, family, previous, readiness, "evaluate-forwarding", "routing", "forwarding readiness changed", s.clock.Now())
		transition = &value
	}
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logOwnershipTransition(logger, id, transition)
}

// SetApplyResult records the last owner operation without consulting the kernel.
func (s *Store) SetApplyResult(id string, result ApplyResult) {
	if result.At.IsZero() {
		result.At = s.clock.Now()
	}
	s.mu.Lock()
	current, ok := s.connections[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	previous := current.LastApply.Result
	if previous == "" {
		previous = "unknown"
	}
	current.LastApply = result
	var transition *Transition
	if result.Result != previous {
		value := s.addTransitionLocked(&current, "link", previous, result.Result, result.Operation, result.Dependency, result.Reason, result.At)
		transition = &value
	}
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logOwnershipTransition(logger, id, transition)
}

// SetFirewallProtection records the firewall module's family verdict.
func (s *Store) SetFirewallProtection(id, family, verdict string) {
	s.mu.Lock()
	current, ok := s.connections[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	var previous string
	switch addressFamily(family) {
	case familyIPv4:
		previous = current.IPv4.Firewall
		current.IPv4.Firewall = verdict
	case familyIPv6:
		previous = current.IPv6.Firewall
		current.IPv6.Firewall = verdict
	default:
		s.mu.Unlock()
		return
	}
	var transition *Transition
	if previous != verdict {
		value := s.addTransitionLocked(&current, family, previous, verdict, "evaluate-firewall", "firewall", "firewall protection changed", s.clock.Now())
		transition = &value
	}
	s.connections[id] = current
	logger := s.transitionLog
	s.mu.Unlock()
	logOwnershipTransition(logger, id, transition)
}

// RecordObservation applies the production monitor's complete snapshots and ordered deltas.
// The initial snapshot establishes a baseline and does not report a transition.
func (s *Store) RecordObservation(event netif.Event) {
	id := event.ConnectionID
	if id == "" {
		return
	}
	s.mu.Lock()
	current, ok := s.connections[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	if event.Kind == netif.EvObservationStale || event.Kind == netif.EvObservationFailed {
		previous := current.Observation
		current.Observation = "stale"
		if event.Kind == netif.EvObservationFailed {
			current.Observation = "failed"
		}
		current.ObservationAt = event.ObservedAt
		current.ObservationReason = event.Reason
		var transition *Transition
		if previous != current.Observation {
			value := s.addTransitionLocked(&current, "link", previous, current.Observation, "observe", "netlink", event.Reason, event.ObservedAt)
			transition = &value
		}
		s.connections[id] = current
		logger := s.transitionLog
		s.mu.Unlock()
		logOwnershipTransition(logger, id, transition)
		return
	}
	if current.Observation != "fresh" && event.Kind != netif.EvResync {
		s.mu.Unlock()
		return
	}
	previous := cloneConnection(current)
	if event.Kind == netif.EvResync && event.Snapshot != nil {
		applyObservedSnapshot(&current, event.Snapshot)
	} else {
		if event.SnapshotReplay {
			s.mu.Unlock()
			return
		}
		applyObservedDelta(&current, event)
	}
	var transitions []Transition
	if previous.Observation != "unknown" {
		if previous.LinkState != current.LinkState {
			transitions = append(transitions, s.addTransitionLocked(&current, "link", previous.LinkState, current.LinkState, "observe-link", "kernel", "kernel link state changed", current.ObservedAt))
		}
		s.appendFamilyObservationTransitionsLocked(&current, "ipv4", previous.IPv4, current.IPv4, &transitions)
		s.appendFamilyObservationTransitionsLocked(&current, "ipv6", previous.IPv6, current.IPv6, &transitions)
	}
	s.connections[id] = current
	log := s.transitionLog
	s.mu.Unlock()
	for i := range transitions {
		logOwnershipTransition(log, id, &transitions[i])
	}
}

func (s *Store) appendFamilyObservationTransitionsLocked(current *ConnectionState, family string, previous, next FamilyState, transitions *[]Transition) {
	for _, address := range previous.Addresses {
		if !slices.ContainsFunc(next.Addresses, func(candidate netif.CurrentAddr) bool { return candidate.CIDR == address.CIDR }) {
			*transitions = append(*transitions, s.addTransitionLocked(current, family, "present", "absent", "observe-address", "kernel", "kernel address "+address.CIDR+" removed", current.ObservedAt))
		}
	}
	for _, address := range next.Addresses {
		oldIndex := slices.IndexFunc(previous.Addresses, func(candidate netif.CurrentAddr) bool { return candidate.CIDR == address.CIDR })
		if oldIndex < 0 {
			*transitions = append(*transitions, s.addTransitionLocked(current, family, "absent", "present", "observe-address", "kernel", "kernel address "+address.CIDR+" added", current.ObservedAt))
		} else if before, after := addressObservationState(previous.Addresses[oldIndex]), addressObservationState(address); before != after {
			*transitions = append(*transitions, s.addTransitionLocked(current, family, before, after, "observe-address", "kernel", "kernel address "+address.CIDR+" flags or validity changed", current.ObservedAt))
		}
	}
	for _, route := range previous.Routes {
		if !slices.ContainsFunc(next.Routes, func(candidate netif.CurrentRoute) bool { return sameRoute(candidate, route) }) {
			*transitions = append(*transitions, s.addTransitionLocked(current, family, "present", "absent", "observe-route", "kernel", routeTransitionReason(route, "removed"), current.ObservedAt))
		}
	}
	for _, route := range next.Routes {
		if !slices.ContainsFunc(previous.Routes, func(candidate netif.CurrentRoute) bool { return sameRoute(candidate, route) }) {
			*transitions = append(*transitions, s.addTransitionLocked(current, family, "absent", "present", "observe-route", "kernel", routeTransitionReason(route, "added"), current.ObservedAt))
		}
	}
}

func addressObservationState(address netif.CurrentAddr) string {
	return fmt.Sprintf("flags=%d preferred=%t valid=%t", address.Flags, address.PreferredLifetime > 0, address.ValidLifetime > 0)
}

func routeTransitionReason(route netif.CurrentRoute, action string) string {
	return fmt.Sprintf("kernel route %s via %s on %s in table %d, protocol %d, metric %d %s", route.Dest, route.Via, route.Dev, route.TableID, route.Protocol, route.Metric, action)
}

func logOwnershipTransition(logger *slog.Logger, id string, transition *Transition) {
	if logger == nil || transition == nil {
		return
	}
	logger.Info("ifmgr: interface transition", "connection_id", id, "transition", *transition)
}

func applyObservedSnapshot(current *ConnectionState, snapshot *netif.Snapshot) {
	current.ActualName, current.IfIndex = snapshot.ActualIface, snapshot.IfIndex
	current.Observation, current.ObservedAt = "fresh", snapshot.ObservedAt
	current.ObservationAt, current.ObservationReason = snapshot.ObservedAt, ""
	current.LinkState = "absent"
	if snapshot.IfIndex > 0 {
		current.LinkState = "down"
		if snapshot.LinkUp {
			current.LinkState = "up"
		}
	}
	current.IPv4.Addresses, current.IPv6.Addresses = nil, nil
	current.IPv4.Routes, current.IPv6.Routes = nil, nil
	for _, address := range snapshot.Addresses {
		if address.Family == "inet6" {
			current.IPv6.Addresses = append(current.IPv6.Addresses, address)
		} else {
			current.IPv4.Addresses = append(current.IPv4.Addresses, address)
		}
	}
	for _, route := range snapshot.Routes {
		route.NextHops = slices.Clone(route.NextHops)
		if route.Family == "inet6" {
			current.IPv6.Routes = append(current.IPv6.Routes, route)
		} else {
			current.IPv4.Routes = append(current.IPv4.Routes, route)
		}
	}
}

func applyObservedDelta(current *ConnectionState, event netif.Event) {
	current.ObservedAt = event.ObservedAt
	current.ActualName, current.IfIndex = event.ActualIface, event.IfIndex
	state := &current.IPv4
	if event.Family == "inet6" {
		state = &current.IPv6
	}
	switch event.Kind {
	case netif.EvLinkUp:
		current.LinkState = "up"
	case netif.EvLinkDown:
		current.LinkState = "down"
	case netif.EvAddrAdded:
		state.Addresses = appendOrReplaceAddress(state.Addresses, netif.CurrentAddr{CIDR: event.CIDR, Family: event.Family, Flags: event.Flags, Scope: event.Scope, PreferredLifetime: event.PreferredLifetime, ValidLifetime: event.ValidLifetime, Origin: event.Origin, ObservedAt: event.ObservedAt})
	case netif.EvAddrDeleted:
		state.Addresses = removeAddress(state.Addresses, event.CIDR)
	case netif.EvRouteAdded:
		state.Routes = appendOrReplaceRoute(state.Routes, routeFromEvent(event))
	case netif.EvRouteDeleted:
		state.Routes = removeRoute(state.Routes, routeFromEvent(event))
	case netif.EvUnknown, netif.EvResync, netif.EvObservationStale, netif.EvObservationFailed:
	}
}

func (s *Store) addTransitionLocked(current *ConnectionState, family, previous, next, operation, dependency, reason string, at time.Time) Transition {
	s.transitionSequence++
	value := Transition{ID: fmt.Sprintf("%s:%d", s.runID, s.transitionSequence), At: at.UTC(), Family: family, Previous: previous, Current: next, Operation: operation, Dependency: dependency, Reason: reason}
	current.Recent = append(current.Recent, value)
	if len(current.Recent) > RecentTransitionLimit {
		current.Recent = slices.Clone(current.Recent[len(current.Recent)-RecentTransitionLimit:])
	}
	return value
}

// SetTransitionLogger sends complete transitions to the daemon's persistent JSON logger.
func (s *Store) SetTransitionLogger(runID string, logger *slog.Logger) {
	s.mu.Lock()
	s.runID, s.transitionLog = runID, logger
	s.mu.Unlock()
}

func appendOrReplaceAddress(addresses []netif.CurrentAddr, value netif.CurrentAddr) []netif.CurrentAddr {
	for i := range addresses {
		if addresses[i].CIDR == value.CIDR {
			addresses[i] = value
			return addresses
		}
	}
	return append(addresses, value)
}

func removeAddress(addresses []netif.CurrentAddr, cidr string) []netif.CurrentAddr {
	return slices.DeleteFunc(addresses, func(address netif.CurrentAddr) bool { return address.CIDR == cidr })
}

func routeFromEvent(event netif.Event) netif.CurrentRoute {
	return netif.CurrentRoute{Family: event.Family, Dest: event.Dest, Via: event.Via, Dev: event.Dev, TableID: event.TableID, Protocol: event.Protocol, Metric: event.Metric, Scope: event.Scope, Type: event.Type, NextHops: slices.Clone(event.NextHops)}
}

func sameRoute(left, right netif.CurrentRoute) bool {
	return left.Family == right.Family && left.Dest == right.Dest && left.TableID == right.TableID && left.Protocol == right.Protocol && left.Metric == right.Metric && left.Scope == right.Scope && left.Type == right.Type && left.Via == right.Via && left.Dev == right.Dev && slices.Equal(left.NextHops, right.NextHops)
}

func appendOrReplaceRoute(routes []netif.CurrentRoute, value netif.CurrentRoute) []netif.CurrentRoute {
	for i := range routes {
		if sameRoute(routes[i], value) {
			routes[i] = value
			return routes
		}
	}
	return append(routes, value)
}

func removeRoute(routes []netif.CurrentRoute, value netif.CurrentRoute) []netif.CurrentRoute {
	return slices.DeleteFunc(routes, func(route netif.CurrentRoute) bool { return sameRoute(route, value) })
}
