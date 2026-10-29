package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

// EventKind classifies a kernel netlink event.
type EventKind int

const (
	// EvUnknown is emitted when the netlink update did not match any known
	// pattern for the watched iface. Daemon ignores these.
	EvUnknown EventKind = iota
	// EvRouteAdded fires when a route via the watched iface is added.
	EvRouteAdded
	// EvRouteDeleted fires when a route via the watched iface is removed.
	EvRouteDeleted
	// EvAddrAdded fires when any address is added on the watched iface.
	// Used to detect SLAAC arrivals and renumber events.
	EvAddrAdded
	// EvAddrDeleted fires when an address is removed from the watched iface.
	EvAddrDeleted
	// EvLinkUp fires when the watched iface transitions to UP/LOWER_UP.
	// Used by bridge_probe to detect link state independently of NDP timing.
	EvLinkUp
	// EvLinkDown fires when the watched iface goes administratively or
	// operationally down.
	EvLinkDown
	// EvResync reports a complete kernel snapshot after observation loses continuity.
	EvResync
	// EvObservationStale reports incomplete state until a successful resynchronization.
	EvObservationStale
	// EvObservationFailed reports a subscription or snapshot failure.
	EvObservationFailed
)

// String returns the stable log-friendly name of the event kind.
func (k EventKind) String() string {
	switch k {
	case EvUnknown:
		return "unknown"
	case EvRouteAdded:
		return "route-added"
	case EvRouteDeleted:
		return "route-deleted"
	case EvAddrAdded:
		return "addr-added"
	case EvAddrDeleted:
		return "addr-deleted"
	case EvLinkUp:
		return "link-up"
	case EvLinkDown:
		return "link-down"
	case EvResync:
		return "resync"
	case EvObservationStale:
		return "observation-stale"
	case EvObservationFailed:
		return "observation-failed"
	}
	return "unknown"
}

// Event is one parsed netlink event for a watched interface.
type Event struct {
	Kind         EventKind
	Family       string // "inet" or "inet6" for address and route events
	Iface        string
	ConnectionID string
	ActualIface  string
	IfIndex      int
	ObservedAt   time.Time
	Reason       string
	// SnapshotReplay marks a state event reconstructed from a snapshot.
	SnapshotReplay bool
	// Route-specific fields (populated when Kind is EvRouteAdded/Deleted).
	Dest     string
	Via      string
	Dev      string
	TableID  int
	Protocol int
	Metric   int
	Scope    int
	Type     int
	NextHops []RouteNextHop
	// Addr-specific fields (populated when Kind is EvAddrAdded/Deleted).
	CIDR              string
	Flags             int
	PreferredLifetime int
	ValidLifetime     int
	Origin            string
	// Snapshot is populated only for EvResync.
	Snapshot *Snapshot
}

// Snapshot is the complete observed link, address, and route state after a gap.
type Snapshot struct {
	ConnectionID string
	Iface        string
	ActualIface  string
	IfIndex      int
	LinkUp       bool
	Addresses    []CurrentAddr
	Routes       []CurrentRoute
	ObservedAt   time.Time
}

// RuleEvent identifies one deleted routing policy rule.
type RuleEvent struct {
	Resync   bool
	Family   string
	TableID  int
	Priority int
	From     string
	Mark     uint32
	IifName  string
	UIDRange string
}

// MonitorConfig configures one Monitor instance. A typed connection matches
// its configured link identity. Bridges require the configured name.
// VLANs match the parent name, tag, and 802.1Q protocol.
// Legacy callers match the configured interface name.
type MonitorConfig struct {
	Iface string
	// ConnectionID associates legacy name matching with a stable configured identity.
	ConnectionID string
	// Connection enables stable configured identity matching. Nil retains legacy name matching.
	Connection *interfaceintent.Connection
}

// Monitor is a long-lived consumer of netlink address, route, and link
// subscriptions for one interface. It emits parsed Events on Events.
// Callers must drain Events to avoid blocking the dispatch goroutines.
//
// Address, route, and link subscriptions retry after errors. A gap emits a
// complete snapshot before deltas resume. Cancellation closes subscriptions.
type Monitor struct {
	cancelled    <-chan struct{}
	cfg          MonitorConfig
	log          *slog.Logger
	Events       chan Event
	done         chan struct{}
	doneMu       sync.Mutex
	closed       bool
	dispatchMu   sync.Mutex
	mu           sync.RWMutex
	ifIndex      int
	actualIface  string
	bindingEpoch uint64
	stateMu      sync.Mutex
	stopped      bool
	stale        bool
	failed       bool
	dirty        bool
	ready        [3]bool
	resync       chan struct{}
}

// NewMonitor returns a started Monitor. Cancel ctx to stop it cleanly.
// A missing link starts with index zero and an empty snapshot. Link creation
// binds the monitor when the configured identity matches.
func NewMonitor(
	ctx context.Context, log *slog.Logger, cfg MonitorConfig,
) *Monitor {
	mlog := log.With("component", "monitor", "iface", cfg.Iface)
	mlog.DebugContext(ctx, "monitor: NewMonitor entry")

	var link netlink.Link
	var err error
	if cfg.Connection != nil {
		link, err = resolveConnectionLink(mlog, *cfg.Connection)
	} else {
		link, err = netlink.LinkByName(cfg.Iface)
	}
	idx := 0
	actualIface := ""
	if err != nil || link == nil {
		mlog.WarnContext(ctx,
			"monitor: iface not matched at startup; events filtered until a matching link appears",
			"err", err)
	} else {
		idx = link.Attrs().Index
		actualIface = link.Attrs().Name
		mlog.DebugContext(ctx, "monitor: resolved iface index", "index", idx)
	}

	m := new(Monitor)
	m.cancelled = ctx.Done()
	m.cfg = cfg
	m.log = mlog
	m.Events = make(chan Event, 64)
	m.done = make(chan struct{})
	m.ifIndex = idx
	m.actualIface = actualIface
	m.stale = true
	m.resync = make(chan struct{}, 1)

	m.startWorker(ctx, "resync", m.resyncLoop)
	m.startWorker(ctx, "subscribe-addr", m.subscribeAddr)
	m.startWorker(ctx, "subscribe-route", m.subscribeRoute)
	m.startWorker(ctx, "subscribe-link", m.subscribeLink)
	m.startWorker(ctx, "shutdown-on-ctx", m.shutdownOnCtx)

	mlog.DebugContext(ctx, "monitor: subscriptions started")
	return m
}

func (m *Monitor) startWorker(
	ctx context.Context,
	name string,
	run func(context.Context),
) {
	go func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			panicMessage := fmt.Sprint(recovered)
			m.log.ErrorContext(ctx, "monitor: worker panicked",
				"worker", name, "err", panicMessage, "panic", panicMessage)
		}()
		run(ctx)
	}()
}

// shutdownOnCtx closes the done channel when ctx is cancelled. Centralised
// so the three subscription goroutines all see one signal.
func (m *Monitor) shutdownOnCtx(ctx context.Context) {
	<-ctx.Done()
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	m.doneMu.Lock()
	defer m.doneMu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.stateMu.Lock()
	m.stopped = true
	for draining := true; draining; {
		select {
		case <-m.Events:
		default:
			draining = false
		}
	}
	m.stateMu.Unlock()
	close(m.done)
	m.log.DebugContext(ctx, "monitor: ctx cancelled, done closed; subscribe goroutines will exit")
}

// subscribeAddr runs the netlink address subscription, translating each
// AddrUpdate into an Event and forwarding to Events.
func (m *Monitor) subscribeAddr(ctx context.Context) {
	log := m.log.With("goroutine", "subscribe-addr")
	log.DebugContext(ctx, "monitor: subscribe-addr starting")
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		ch := make(chan netlink.AddrUpdate, 64)
		failed := make(chan error, 1)
		subDone := make(chan struct{})
		err := netlink.AddrSubscribeWithOptions(ch, subDone, netlink.AddrSubscribeOptions{
			ErrorCallback: func(err error) {
				m.setSubscriptionReady(0, false, "address subscription lost: "+err.Error())
				select {
				case failed <- err:
				default:
				}
			},
		})
		if err != nil {
			close(subDone)
			log.WarnContext(ctx, "monitor: address subscription failed", "err", err)
			m.setSubscriptionReady(0, false, "address subscription failed: "+err.Error())
		} else {
			backoff = 100 * time.Millisecond
			m.setSubscriptionReady(0, true, "")
			for active := true; active; {
				select {
				case <-m.done:
					close(subDone)
					return
				case err := <-failed:
					log.WarnContext(ctx, "monitor: address subscription lost", "err", err)
					active = false
				case upd, ok := <-ch:
					if !ok {
						active = false
						continue
					}
					m.dispatchMu.Lock()
					ev := m.addrUpdateToEvent(upd)
					if ev.Kind != EvUnknown {
						m.emit(ctx, ev)
					}
					m.dispatchMu.Unlock()
				}
			}
			close(subDone)
			m.setSubscriptionReady(0, false, "address subscription closed")
		}
		if !sleepMonitorRetry(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

// subscribeRoute runs the netlink route subscription.
func (m *Monitor) subscribeRoute(ctx context.Context) {
	log := m.log.With("goroutine", "subscribe-route")
	log.DebugContext(ctx, "monitor: subscribe-route starting")
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		ch := make(chan netlink.RouteUpdate, 64)
		failed := make(chan error, 1)
		subDone := make(chan struct{})
		err := netlink.RouteSubscribeWithOptions(ch, subDone, netlink.RouteSubscribeOptions{
			ErrorCallback: func(err error) {
				m.setSubscriptionReady(1, false, "route subscription lost: "+err.Error())
				select {
				case failed <- err:
				default:
				}
			},
		})
		if err != nil {
			close(subDone)
			log.WarnContext(ctx, "monitor: route subscription failed", "err", err)
			m.setSubscriptionReady(1, false, "route subscription failed: "+err.Error())
		} else {
			backoff = 100 * time.Millisecond
			m.setSubscriptionReady(1, true, "")
			for active := true; active; {
				select {
				case <-m.done:
					close(subDone)
					return
				case err := <-failed:
					log.WarnContext(ctx, "monitor: route subscription lost", "err", err)
					active = false
				case upd, ok := <-ch:
					if !ok {
						active = false
						continue
					}
					m.dispatchMu.Lock()
					ev := m.routeUpdateToEvent(upd)
					if ev.Kind != EvUnknown {
						m.emit(ctx, ev)
					}
					m.dispatchMu.Unlock()
				}
			}
			close(subDone)
			m.setSubscriptionReady(1, false, "route subscription closed")
		}
		if !sleepMonitorRetry(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

// subscribeLink runs the netlink link subscription. Translates LinkUpdate
// into EvLinkUp / EvLinkDown when the OPERSTATE crosses the up/down
// boundary on the watched iface.
func (m *Monitor) subscribeLink(ctx context.Context) {
	log := m.log.With("goroutine", "subscribe-link")
	log.DebugContext(ctx, "monitor: subscribe-link starting")
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		ch := make(chan netlink.LinkUpdate, 64)
		failed := make(chan error, 1)
		subDone := make(chan struct{})
		err := netlink.LinkSubscribeWithOptions(ch, subDone, netlink.LinkSubscribeOptions{
			ErrorCallback: func(err error) {
				m.setSubscriptionReady(2, false, "link subscription lost: "+err.Error())
				select {
				case failed <- err:
				default:
				}
			},
		})
		if err != nil {
			close(subDone)
			log.WarnContext(ctx, "monitor: link subscription failed", "err", err)
			m.setSubscriptionReady(2, false, "link subscription failed: "+err.Error())
		} else {
			backoff = 100 * time.Millisecond
			m.setSubscriptionReady(2, true, "")
			for active := true; active; {
				select {
				case <-m.done:
					close(subDone)
					return
				case err := <-failed:
					log.WarnContext(ctx, "monitor: link subscription lost", "err", err)
					active = false
				case upd, ok := <-ch:
					if !ok {
						active = false
						continue
					}
					m.dispatchMu.Lock()
					ev := m.linkUpdateToEvent(upd)
					if ev.Kind != EvUnknown {
						m.emit(ctx, ev)
					}
					m.dispatchMu.Unlock()
				}
			}
			close(subDone)
			m.setSubscriptionReady(2, false, "link subscription closed")
		}
		if !sleepMonitorRetry(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

// emit reports deltas while subscriptions are current. Overflow schedules
// a complete kernel snapshot before delta delivery resumes.
func (m *Monitor) emit(ctx context.Context, ev Event) {
	if ctx.Err() != nil {
		return
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if m.stale {
		m.dirty = true
		return
	}
	select {
	case m.Events <- ev:
	case <-ctx.Done():
	default:
		m.log.WarnContext(ctx, "monitor: Events channel full; requesting snapshot",
			"kind", ev.Kind.String(), "iface", ev.Iface)
		m.markStaleLocked("event queue overflow")
	}
}

func (m *Monitor) setSubscriptionReady(index int, ready bool, reason string) {
	if !ready {
		m.mu.Lock()
		m.ifIndex = 0
		m.actualIface = ""
		m.bindingEpoch++
		m.mu.Unlock()
		m.stateMu.Lock()
		m.ready[index] = false
		m.markStaleLocked(reason)
		m.markFailedLocked(reason)
		m.stateMu.Unlock()
		return
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.ready[index] = ready
	if m.ready[0] && m.ready[1] && m.ready[2] && m.stale {
		select {
		case m.resync <- struct{}{}:
		default:
		}
	}
}

func sleepMonitorRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *Monitor) resyncLoop(ctx context.Context) {
	backoff := 100 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.resync:
		}
		m.stateMu.Lock()
		ready := m.ready[0] && m.ready[1] && m.ready[2]
		m.stateMu.Unlock()
		if !ready {
			continue
		}
		snapshot, epoch, err := m.readSnapshot()
		if err != nil {
			m.log.WarnContext(ctx, "monitor: snapshot failed", "err", err)
			m.stateMu.Lock()
			m.markFailedLocked("kernel snapshot failed: " + err.Error())
			m.stateMu.Unlock()
			if !sleepMonitorRetry(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 5*time.Second)
			m.stateMu.Lock()
			m.markStaleLocked("kernel snapshot retry")
			m.stateMu.Unlock()
			continue
		}
		backoff = 100 * time.Millisecond
		var event Event
		event.Kind = EvResync
		event.Iface = m.cfg.Iface
		event.ConnectionID = snapshot.ConnectionID
		event.ActualIface = snapshot.ActualIface
		event.IfIndex = snapshot.IfIndex
		event.ObservedAt = snapshot.ObservedAt
		event.Snapshot = snapshot
		published := m.publishSnapshot(ctx, event, epoch)
		if !published && ctx.Err() != nil {
			return
		}
	}
}

func (m *Monitor) readSnapshot() (*Snapshot, uint64, error) {
	snapshot := new(Snapshot)
	snapshot.Iface = m.cfg.Iface
	snapshot.ConnectionID = m.cfg.ConnectionID
	snapshot.ObservedAt = realClock{}.Now()
	m.mu.RLock()
	epoch := m.bindingEpoch
	m.mu.RUnlock()
	var link netlink.Link
	var err error
	if m.cfg.Connection != nil {
		snapshot.ConnectionID = m.cfg.Connection.ID.String()
		link, err = resolveConnectionLink(m.log, *m.cfg.Connection)
	} else {
		link, err = netlink.LinkByName(m.cfg.Iface)
		if IsLinkNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		return nil, 0, err
	}
	if link == nil {
		return snapshot, epoch, nil
	}
	attrs := link.Attrs()
	snapshot.IfIndex = attrs.Index
	snapshot.ActualIface = attrs.Name
	snapshot.LinkUp = attrs.OperState == netlink.OperUp || attrs.OperState == netlink.OperUnknown
	snapshot.Addresses, err = listAddrsNetlink(m.log, link)
	if err != nil {
		return nil, 0, err
	}
	snapshot.Routes, err = m.snapshotRoutes(attrs.Index)
	if err != nil {
		return nil, 0, err
	}
	return snapshot, epoch, nil
}

func (m *Monitor) snapshotRoutes(index int) ([]CurrentRoute, error) {
	var observed []CurrentRoute
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		routes, err := listUsableRoutes(family,
			&netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
		if err != nil {
			m.log.Warn("monitor: route snapshot failed", "family", family, "err", err)
			return nil, fmt.Errorf("list %d routes: %w", family, err)
		}
		for _, route := range routes {
			if !routeUsesIndex(route, index) {
				continue
			}
			current, err := routeToCurrent(m.log, route)
			if err != nil {
				return nil, err
			}
			observed = append(observed, *current)
		}
	}
	return observed, nil
}

func routeUsesIndex(route netlink.Route, index int) bool {
	if route.LinkIndex == index {
		return true
	}
	for _, hop := range route.MultiPath {
		if hop != nil && hop.LinkIndex == index {
			return true
		}
	}
	return false
}

// addrUpdateToEvent translates one AddrUpdate into our Event type. Returns
// EvUnknown for events on other interfaces or with empty CIDR.
func (m *Monitor) addrUpdateToEvent(u netlink.AddrUpdate) Event {
	if !m.watchesIndex(u.LinkIndex) {
		return unknownEvent()
	}
	cidr := u.LinkAddress.String()
	if cidr == "<nil>" || cidr == "" {
		return unknownEvent()
	}
	fam := "inet"
	if u.LinkAddress.IP.To4() == nil {
		fam = "inet6"
	}
	kind := EvAddrAdded
	if !u.NewAddr {
		kind = EvAddrDeleted
	}
	var event Event
	event.Kind = kind
	event.Family = fam
	event.Iface = m.cfg.Iface
	event.CIDR = cidr
	event.Flags = u.Flags
	event.Scope = u.Scope
	event.PreferredLifetime = u.PreferedLft
	event.ValidLifetime = u.ValidLft
	event.Origin = "unknown"
	return m.decorate(event)
}

// routeUpdateToEvent translates one route update on the watched interface.
func (m *Monitor) routeUpdateToEvent(u netlink.RouteUpdate) Event {
	r := u.Route
	if !m.watchesRoute(r) {
		return unknownEvent()
	}
	famConst := r.Family
	if famConst == 0 {
		// Some netlink versions leave Family unset; infer from Dst/Gw.
		switch {
		case r.Gw != nil && r.Gw.To4() != nil:
			famConst = unix.AF_INET
		case r.Gw != nil:
			famConst = unix.AF_INET6
		case r.Dst != nil && r.Dst.IP.To4() != nil:
			famConst = unix.AF_INET
		case r.Dst != nil:
			famConst = unix.AF_INET6
		}
	}
	if famConst != unix.AF_INET && famConst != unix.AF_INET6 {
		return unknownEvent()
	}
	fam := "inet6"
	if famConst == unix.AF_INET {
		fam = "inet"
	}

	var kind EventKind
	switch u.Type {
	case unix.RTM_DELROUTE:
		kind = EvRouteDeleted
	case unix.RTM_NEWROUTE:
		kind = EvRouteAdded
	default:
		return unknownEvent()
	}

	via := ""
	if r.Gw != nil {
		via = r.Gw.String()
	}
	dest := "default"
	if !isDefaultRoute(r, famConst) {
		dest = r.Dst.String()
	}
	hops := observedRouteNextHops(r)
	dev := ""
	if len(hops) > 0 {
		dev = hops[0].Dev
		if via == "" {
			via = hops[0].Via
		}
	}
	var event Event
	event.Kind = kind
	event.Family = fam
	event.Iface = m.cfg.Iface
	event.Dest = dest
	event.Via = via
	event.Dev = dev
	event.TableID = r.Table
	event.Protocol = int(r.Protocol)
	event.Metric = r.Priority
	event.Scope = int(r.Scope)
	event.Type = r.Type
	event.NextHops = hops
	return m.decorate(event)
}

func observedRouteNextHops(route netlink.Route) []RouteNextHop {
	hops := make([]RouteNextHop, 0, len(route.MultiPath)+1)
	if route.LinkIndex != 0 {
		var hop RouteNextHop
		hop.LinkIndex = route.LinkIndex
		hop.Weight = 1
		if route.Gw != nil {
			hop.Via = route.Gw.String()
		}
		hops = append(hops, hop)
	}
	for _, next := range route.MultiPath {
		if next == nil {
			continue
		}
		var hop RouteNextHop
		hop.LinkIndex = next.LinkIndex
		hop.Weight = next.Hops + 1
		if next.Gw != nil {
			hop.Via = next.Gw.String()
		}
		hops = append(hops, hop)
	}
	for i := range hops {
		link, err := netlink.LinkByIndex(hops[i].LinkIndex)
		if err == nil && link.Attrs() != nil {
			hops[i].Dev = link.Attrs().Name
		}
	}
	return hops
}

func (m *Monitor) watchesRoute(route netlink.Route) bool {
	if m.watchesIndex(route.LinkIndex) {
		return true
	}
	for _, hop := range route.MultiPath {
		if hop != nil && m.watchesIndex(hop.LinkIndex) {
			return true
		}
	}
	return false
}

func (m *Monitor) decorate(event Event) Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	event.IfIndex = m.ifIndex
	event.ActualIface = m.actualIface
	event.ObservedAt = realClock{}.Now()
	event.ConnectionID = m.cfg.ConnectionID
	if m.cfg.Connection != nil {
		event.ConnectionID = m.cfg.Connection.ID.String()
	}
	return event
}

// linkUpdateToEvent translates one LinkUpdate into our Event type, emitting
// only when the watched iface transitions across up/down. The kernel sends
// many LinkUpdates for unrelated state bits; we suppress noise.
func (m *Monitor) linkUpdateToEvent(u netlink.LinkUpdate) Event {
	m.mu.Lock()
	previousIndex := m.ifIndex
	previousIface := m.actualIface
	if m.cfg.Connection != nil {
		m.rebindConfiguredLocked()
	} else {
		m.rebindLegacyLocked(0)
		if u.Header.Type == unix.RTM_NEWLINK && u.Attrs() != nil && u.Attrs().Name == m.cfg.Iface {
			m.rebindLegacyLocked(int(u.Index))
		}
	}
	matched := int(u.Index) == m.ifIndex && m.ifIndex != 0
	if u.Header.Type == unix.RTM_DELLINK && int(u.Index) == previousIndex &&
		(m.cfg.Connection != nil || u.Attrs() != nil && u.Attrs().Name == m.cfg.Iface) {
		matched = true
		m.ifIndex = 0
		m.actualIface = ""
	}
	actualIface := m.actualIface
	index := m.ifIndex
	if previousIndex != index || previousIface != actualIface {
		m.bindingEpoch++
	}
	m.mu.Unlock()
	if previousIndex != index || previousIface != actualIface {
		m.invalidateBinding()
	}
	if !matched {
		return unknownEvent()
	}
	makeEvent := func(kind EventKind) Event {
		var event Event
		event.Kind = kind
		event.Iface = m.cfg.Iface
		event.ActualIface = actualIface
		event.IfIndex = index
		event.ObservedAt = realClock{}.Now()
		event.ConnectionID = m.cfg.ConnectionID
		if m.cfg.Connection != nil {
			event.ConnectionID = m.cfg.Connection.ID.String()
		}
		return event
	}
	switch u.Header.Type {
	case unix.RTM_NEWLINK:
		// IFF_UP set means administratively up. We use OperState for "really up".
		if u.Attrs() == nil {
			return unknownEvent()
		}
		switch u.Attrs().OperState {
		case netlink.OperUp, netlink.OperUnknown:
			return makeEvent(EvLinkUp)
		case netlink.OperDown, netlink.OperLowerLayerDown, netlink.OperNotPresent:
			return makeEvent(EvLinkDown)
		}
	case unix.RTM_DELLINK:
		return makeEvent(EvLinkDown)
	}
	return unknownEvent()
}

func (m *Monitor) watchesIndex(index int) bool {
	if index == 0 {
		return false
	}
	m.mu.Lock()
	previousIndex := m.ifIndex
	previousIface := m.actualIface
	if m.cfg.Connection != nil {
		m.rebindConfiguredLocked()
		matched := index == m.ifIndex && m.ifIndex != 0
		changed := previousIndex != m.ifIndex || previousIface != m.actualIface
		if changed {
			m.bindingEpoch++
		}
		m.mu.Unlock()
		if changed {
			m.invalidateBinding()
		}
		return matched
	}
	m.rebindLegacyLocked(index)
	matched := index == m.ifIndex && m.ifIndex != 0
	changed := previousIndex != m.ifIndex || previousIface != m.actualIface
	if changed {
		m.bindingEpoch++
	}
	m.mu.Unlock()
	if changed {
		m.invalidateBinding()
	}
	return matched
}

func (m *Monitor) rebindLegacyLocked(index int) {
	if m.ifIndex != 0 {
		link, err := netlink.LinkByIndex(m.ifIndex)
		if err != nil || link.Attrs() == nil || link.Attrs().Name != m.cfg.Iface {
			m.ifIndex = 0
			m.actualIface = ""
		}
	}
	if m.ifIndex != 0 || index == 0 {
		return
	}
	link, err := netlink.LinkByIndex(index)
	if err != nil || link.Attrs() == nil || link.Attrs().Name != m.cfg.Iface {
		return
	}
	m.ifIndex = index
	m.actualIface = link.Attrs().Name
}

func (m *Monitor) invalidateBinding() {
	m.stateMu.Lock()
	m.markStaleLocked("link binding changed")
	m.stateMu.Unlock()
}

func (m *Monitor) rebindConfiguredLocked() {
	link, err := resolveConnectionLink(m.log, *m.cfg.Connection)
	if err != nil {
		m.log.Warn("monitor: configured link match failed", "err", err)
	}
	if err != nil || link == nil {
		m.ifIndex = 0
		m.actualIface = ""
		return
	}
	m.ifIndex = link.Attrs().Index
	m.actualIface = link.Attrs().Name
}

// IfIndex returns the netlink index of the watched interface (0 if unknown).
// Exposed for diagnostic logging by callers.
func (m *Monitor) IfIndex() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ifIndex
}

func unknownEvent() Event {
	var event Event
	event.Kind = EvUnknown
	return event
}

// sleepOrCancel sleeps for d or until ctx is cancelled, whichever first.
// Used by dhcp.go's backoff loop. Lives here (not in dhcp.go) so the
// helper survives any future per-file rewrite without breaking the
// async DHCP path's compile.
func sleepOrCancel(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Used to silence the unused-import linter on `errors` while we keep it
// imported for potential typed error handling additions.
var _ = errors.Is
