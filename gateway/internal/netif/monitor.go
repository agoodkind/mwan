package netif

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

const (
	monitorEventBufferSize = 64
	monitorRetryInitial    = 100 * time.Millisecond
	monitorRetryMaximum    = 5 * time.Second
	monitorRetryMultiplier = 2
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
	// EvLinkUp fires when the watched interface transitions to UP or LOWER_UP.
	EvLinkUp
	// EvLinkDown fires when the watched iface goes administratively or
	// operationally down.
	EvLinkDown
	// EvResync reports a complete kernel snapshot after observation gaps.
	EvResync
	// EvObservationStale reports incomplete state before resynchronization.
	EvObservationStale
	// EvObservationFailed reports a subscription or snapshot failure.
	EvObservationFailed
	// EvUplinkReturned fires when a watched bridge port returns to operational
	// state after going down.
	EvUplinkReturned
)

// String returns the stable log name for an event kind.
//
// Example:
//
//	EventKind(EvLinkUp).String() // "link-up"
func (k EventKind) String() string {
	switch k {
	case EvUnknown:
		return HealthStateUnknown
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
	case EvUplinkReturned:
		return "uplink-returned"
	}

	return HealthStateUnknown
}

// Event contains one parsed netlink update for a watched interface.
// Its fields depend on Kind. Resync events include a complete Snapshot.
//
// Example:
//
//	Event{Kind: EvLinkUp, Iface: "wan0"}
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

// Snapshot is the observed link, address, and route state after a gap.
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
	// ConnectionID associates legacy name matching with configured identity.
	ConnectionID string
	// Connection enables stable identity matching. Nil uses interface names.
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
	uplinkDown   map[int]bool
}

// NewMonitor starts netlink subscriptions for cfg and returns the monitor.
// Cancel ctx to stop subscriptions. A missing link stays unbound until it
// appears, and callers must read Events to receive updates.
//
// Example:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	monitor := NewMonitor(ctx, log, MonitorConfig{Iface: "wan0"})
//	_ = monitor.Events
func NewMonitor(
	ctx context.Context,
	log *slog.Logger,
	cfg MonitorConfig,
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
		mlog.WarnContext(
			ctx,
			"monitor: iface not matched at startup; events filtered until a matching link appears",
			"err",
			err,
		)
	} else {
		idx = link.Attrs().Index
		actualIface = link.Attrs().Name
		mlog.DebugContext(ctx, "monitor: resolved iface index", "index", idx)
	}

	monitor := new(Monitor)
	monitor.cancelled = ctx.Done()
	monitor.cfg = cfg
	monitor.log = mlog
	monitor.Events = make(chan Event, monitorEventBufferSize)
	monitor.done = make(chan struct{})
	monitor.ifIndex = idx
	monitor.actualIface = actualIface
	monitor.stale = true
	monitor.resync = make(chan struct{}, 1)
	monitor.uplinkDown = make(map[int]bool)

	monitor.startWorker(ctx, "resync", monitor.resyncLoop)
	monitor.startWorker(ctx, "subscribe-addr", monitor.subscribeAddr)
	monitor.startWorker(ctx, "subscribe-route", monitor.subscribeRoute)
	monitor.startWorker(ctx, "subscribe-link", monitor.subscribeLink)
	monitor.startWorker(ctx, "shutdown-on-ctx", monitor.shutdownOnCtx)

	mlog.DebugContext(ctx, "monitor: subscriptions started")

	return monitor
}

// IfIndex reports the watched interface's kernel index or zero when absent.
func (m *Monitor) IfIndex() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.ifIndex
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
			m.log.ErrorContext(
				ctx,
				"monitor: worker panicked",
				"worker",
				name,
				"err",
				panicMessage,
				"panic",
				panicMessage,
			)
		}()
		run(ctx)
	}()
}

// The monitor drains queued events before it closes its done channel.
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

// emit sends deltas and invalidates the snapshot when the queue fills.
func (m *Monitor) emit(ctx context.Context, event Event) {
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
	case m.Events <- event:
	case <-ctx.Done():
	default:
		m.log.WarnContext(
			ctx,
			"monitor: Events channel full; requesting snapshot",
			"kind",
			event.Kind.String(),
			"iface",
			event.Iface,
		)
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

func (m *Monitor) resyncLoop(ctx context.Context) {
	backoff := monitorRetryInitial

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

			backoff = min(backoff*monitorRetryMultiplier, monitorRetryMaximum)
			m.stateMu.Lock()
			m.markStaleLocked("kernel snapshot retry")
			m.stateMu.Unlock()

			continue
		}

		backoff = monitorRetryInitial
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
		routes, err := listUsableRoutes(
			family,
			&netlink.Route{Table: unix.RT_TABLE_UNSPEC},
			netlink.RT_FILTER_TABLE,
		)
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

// addrUpdateToEvent translates one AddrUpdate into our Event type. Returns
// EvUnknown for events on other interfaces or with empty CIDR.
func (m *Monitor) addrUpdateToEvent(update netlink.AddrUpdate) Event {
	if !m.watchesIndex(update.LinkIndex) {
		return unknownEvent()
	}

	cidr := update.LinkAddress.String()
	if cidr == "<nil>" || cidr == "" {
		return unknownEvent()
	}

	fam := tunnelEndpointRouteFamily
	if update.LinkAddress.IP.To4() == nil {
		fam = string(httpFamilyV6)
	}

	kind := EvAddrAdded
	if !update.NewAddr {
		kind = EvAddrDeleted
	}

	var event Event
	event.Kind = kind
	event.Family = fam
	event.Iface = m.cfg.Iface
	event.CIDR = cidr
	event.Flags = update.Flags
	event.Scope = update.Scope
	event.PreferredLifetime = update.PreferedLft
	event.ValidLifetime = update.ValidLft
	event.Origin = HealthStateUnknown

	return m.decorate(event)
}

// routeUpdateToEvent translates one route update on the watched interface.
func (m *Monitor) routeUpdateToEvent(update netlink.RouteUpdate) Event {
	route := update.Route
	if !m.watchesRoute(route) {
		return unknownEvent()
	}

	famConst := route.Family
	if famConst == 0 {
		// Some netlink versions leave Family unset; infer from Dst/Gw.
		switch {
		case route.Gw != nil && route.Gw.To4() != nil:
			famConst = unix.AF_INET
		case route.Gw != nil:
			famConst = unix.AF_INET6
		case route.Dst != nil && route.Dst.IP.To4() != nil:
			famConst = unix.AF_INET
		case route.Dst != nil:
			famConst = unix.AF_INET6
		}
	}

	if famConst != unix.AF_INET && famConst != unix.AF_INET6 {
		return unknownEvent()
	}

	fam := string(httpFamilyV6)
	if famConst == unix.AF_INET {
		fam = tunnelEndpointRouteFamily
	}

	var kind EventKind
	switch update.Type {
	case unix.RTM_DELROUTE:
		kind = EvRouteDeleted
	case unix.RTM_NEWROUTE:
		kind = EvRouteAdded
	default:
		return unknownEvent()
	}

	via := ""
	if route.Gw != nil {
		via = route.Gw.String()
	}

	dest := "default"
	if !isDefaultRoute(route, famConst) {
		dest = route.Dst.String()
	}

	hops := observedRouteNextHops(route)
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
	event.TableID = route.Table
	event.Protocol = int(route.Protocol)
	event.Metric = route.Priority
	event.Scope = int(route.Scope)
	event.Type = route.Type
	event.NextHops = hops

	return m.decorate(event)
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
func (m *Monitor) linkUpdateToEvent(update netlink.LinkUpdate) Event {
	m.mu.Lock()
	previousIndex := m.ifIndex
	previousIface := m.actualIface
	if m.cfg.Connection != nil {
		m.rebindConfiguredLocked()
	} else {
		m.rebindLegacyLocked(0)
		if update.Header.Type == unix.RTM_NEWLINK && update.Attrs() != nil &&
			update.Attrs().Name == m.cfg.Iface {
			m.rebindLegacyLocked(int(update.Index))
		}
	}

	matched := int(update.Index) == m.ifIndex && m.ifIndex != 0
	if update.Header.Type == unix.RTM_DELLINK && int(update.Index) == previousIndex &&
		(m.cfg.Connection != nil || update.Attrs() != nil && update.Attrs().Name == m.cfg.Iface) {
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
		return m.uplinkReturnEvent(update, index, actualIface)
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
	switch update.Header.Type {
	case unix.RTM_NEWLINK:
		// IFF_UP set means administratively up. We use OperState for "really up".
		if update.Attrs() == nil {
			return unknownEvent()
		}

		switch update.Attrs().OperState {
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

// uplinkReturnEvent reports watched bridge ports after they return to
// operational state. It ignores veth and tuntap links and AF_BRIDGE copies.
func (m *Monitor) uplinkReturnEvent(
	update netlink.LinkUpdate,
	bridgeIndex int,
	actualIface string,
) Event {
	attrs := update.Attrs()
	invalidBridgeUpdate := bridgeIndex == 0 ||
		update.Header.Type != unix.RTM_NEWLINK ||
		update.Family == unix.AF_BRIDGE ||
		attrs == nil ||
		attrs.MasterIndex != bridgeIndex
	if invalidBridgeUpdate {
		return unknownEvent()
	}

	if linkType := update.Type(); linkType == "veth" || linkType == "tuntap" {
		return unknownEvent()
	}

	switch attrs.OperState {
	case netlink.OperDown, netlink.OperLowerLayerDown, netlink.OperNotPresent:
		m.uplinkDown[int(update.Index)] = true
	case netlink.OperUp, netlink.OperUnknown:
		if m.uplinkDown[int(update.Index)] {
			delete(m.uplinkDown, int(update.Index))
			event := m.observationEvent(EvUplinkReturned, attrs.Name)
			event.ActualIface = actualIface
			event.IfIndex = bridgeIndex

			return event
		}
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
