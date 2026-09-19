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
)

// EventKind classifies a kernel netlink event.
type EventKind int

const (
	// EvUnknown is emitted when the netlink update did not match any known
	// pattern for the watched iface. Daemon ignores these.
	EvUnknown EventKind = iota
	// EvRouteAdded fires when a `default` route via the watched iface is added.
	// Includes RA-learned routes (proto ra) and DHCP-installed routes.
	EvRouteAdded
	// EvRouteDeleted fires when such a default route is removed.
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
	}
	return "unknown"
}

// Event is one parsed netlink event the daemon should react to. Mirrors
// what the previous shellout-based monitor produced; new EvLink{Up,Down}
// kinds are added with the netlink switch since LinkSubscribe gives us
// link-level events for free.
type Event struct {
	Kind   EventKind
	Family string // "inet" or "inet6" for addr/route events; "" for link events
	Iface  string
	// Route-specific fields (populated when Kind is EvRouteAdded/Deleted).
	Dest string
	Via  string
	// Addr-specific fields (populated when Kind is EvAddrAdded/Deleted).
	CIDR string
}

// MonitorConfig configures one Monitor instance. The watched iface name is
// resolved to a kernel link index at construction; if the iface is renamed
// or removed, the Monitor logs a WARN and the subscription naturally stops
// emitting events for the old index. Operator action required.
type MonitorConfig struct {
	Iface string
}

// Monitor is a long-lived consumer of netlink address, route, and link
// subscriptions for one interface. It emits parsed Events on Events.
// Callers must drain Events to avoid blocking the dispatch goroutines.
//
// The implementation owns three netlink subscriptions, one each for
// AddrSubscribe, RouteSubscribe, and LinkSubscribe. Each runs in its own
// goroutine that translates the netlink update into our Event type and
// forwards to Events. On cancellation the done channel closes; netlink
// goroutines see the close and exit cleanly.
type Monitor struct {
	cfg     MonitorConfig
	log     *slog.Logger
	Events  chan Event
	done    chan struct{}
	doneMu  sync.Mutex
	closed  bool
	ifIndex int
}

// NewMonitor returns a started Monitor. Cancel ctx to stop it cleanly.
// If the watched iface does not exist at construction, NewMonitor still
// succeeds with ifIndex=0; callers will get no events until the iface
// appears (the daemon's reconcile loop is what handles that case).
func NewMonitor(
	ctx context.Context, log *slog.Logger, cfg MonitorConfig,
) *Monitor {
	mlog := log.With("component", "monitor", "iface", cfg.Iface)
	mlog.DebugContext(ctx, "monitor: NewMonitor entry")

	link, err := netlink.LinkByName(cfg.Iface)
	idx := 0
	if err != nil {
		mlog.WarnContext(ctx,
			"monitor: iface not found at startup; events filtered to nothing until reconcile",
			"err", err)
	} else {
		idx = link.Attrs().Index
		mlog.DebugContext(ctx, "monitor: resolved iface index", "index", idx)
	}

	m := &Monitor{
		cfg:     cfg,
		log:     mlog,
		Events:  make(chan Event, 64),
		done:    make(chan struct{}),
		doneMu:  sync.Mutex{},
		closed:  false,
		ifIndex: idx,
	}

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
	m.doneMu.Lock()
	defer m.doneMu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.done)
	m.log.DebugContext(ctx, "monitor: ctx cancelled, done closed; subscribe goroutines will exit")
}

// subscribeAddr runs the netlink address subscription, translating each
// AddrUpdate into an Event and forwarding to Events.
func (m *Monitor) subscribeAddr(ctx context.Context) {
	log := m.log.With("goroutine", "subscribe-addr")
	log.DebugContext(ctx, "monitor: subscribe-addr starting")

	ch := make(chan netlink.AddrUpdate, 64)
	if err := netlink.AddrSubscribeWithOptions(ch, m.done, netlink.AddrSubscribeOptions{
		ErrorCallback: func(err error) {
			log.WarnContext(ctx, "monitor: AddrSubscribe error", "err", err)
		},
		// ListExisting=true: emit synthetic events for existing addresses
		// at subscribe time. Modules track first-observed timestamps via
		// OnKernelEvent, so they need to see what was already on the iface
		// when the daemon started, not just deltas going forward.
		ListExisting: true,
	}); err != nil {
		log.ErrorContext(ctx,
			"monitor: AddrSubscribeWithOptions failed; goroutine exiting",
			"err", err)
		return
	}

	for {
		select {
		case <-m.done:
			log.DebugContext(ctx, "monitor: subscribe-addr exiting (done)")
			return
		case upd, ok := <-ch:
			if !ok {
				log.DebugContext(ctx, "monitor: subscribe-addr channel closed")
				return
			}
			ev := m.addrUpdateToEvent(upd)
			if ev.Kind == EvUnknown {
				continue
			}
			log.DebugContext(ctx, "monitor: addr event",
				"kind", ev.Kind.String(), "cidr", ev.CIDR, "family", ev.Family)
			m.emit(ctx, ev)
		}
	}
}

// subscribeRoute runs the netlink route subscription.
func (m *Monitor) subscribeRoute(ctx context.Context) {
	log := m.log.With("goroutine", "subscribe-route")
	log.DebugContext(ctx, "monitor: subscribe-route starting")

	ch := make(chan netlink.RouteUpdate, 64)
	if err := netlink.RouteSubscribeWithOptions(ch, m.done, netlink.RouteSubscribeOptions{
		ErrorCallback: func(err error) {
			log.WarnContext(ctx, "monitor: RouteSubscribe error", "err", err)
		},
		// ListExisting=true: emit synthetic events for existing routes so
		// modules see RA-installed defaults that arrived before the daemon
		// started. Otherwise lastRA stays zero and bridge_probe never fires.
		ListExisting: true,
	}); err != nil {
		log.ErrorContext(ctx,
			"monitor: RouteSubscribeWithOptions failed; goroutine exiting",
			"err", err)
		return
	}

	for {
		select {
		case <-m.done:
			log.DebugContext(ctx, "monitor: subscribe-route exiting (done)")
			return
		case upd, ok := <-ch:
			if !ok {
				log.DebugContext(ctx, "monitor: subscribe-route channel closed")
				return
			}
			ev := m.routeUpdateToEvent(upd)
			if ev.Kind == EvUnknown {
				continue
			}
			log.DebugContext(ctx, "monitor: route event",
				"kind", ev.Kind.String(), "dest", ev.Dest, "via", ev.Via,
				"family", ev.Family)
			m.emit(ctx, ev)
		}
	}
}

// subscribeLink runs the netlink link subscription. Translates LinkUpdate
// into EvLinkUp / EvLinkDown when the OPERSTATE crosses the up/down
// boundary on the watched iface.
func (m *Monitor) subscribeLink(ctx context.Context) {
	log := m.log.With("goroutine", "subscribe-link")
	log.DebugContext(ctx, "monitor: subscribe-link starting")

	ch := make(chan netlink.LinkUpdate, 64)
	if err := netlink.LinkSubscribeWithOptions(ch, m.done, netlink.LinkSubscribeOptions{
		ErrorCallback: func(err error) {
			log.WarnContext(ctx, "monitor: LinkSubscribe error", "err", err)
		},
		// ListExisting=true: emit a synthetic LinkUpdate for the watched
		// iface so EvLinkUp fires once at startup if the iface is already
		// up. bridge_probe sets lastLinkUp from this event.
		ListExisting: true,
	}); err != nil {
		log.ErrorContext(ctx,
			"monitor: LinkSubscribeWithOptions failed; goroutine exiting",
			"err", err)
		return
	}

	for {
		select {
		case <-m.done:
			log.DebugContext(ctx, "monitor: subscribe-link exiting (done)")
			return
		case upd, ok := <-ch:
			if !ok {
				log.DebugContext(ctx, "monitor: subscribe-link channel closed")
				return
			}
			ev := m.linkUpdateToEvent(upd)
			if ev.Kind == EvUnknown {
				continue
			}
			log.DebugContext(ctx, "monitor: link event",
				"kind", ev.Kind.String(), "iface", ev.Iface)
			m.emit(ctx, ev)
		}
	}
}

// emit forwards an event to Events with non-blocking semantics. If the
// receiver is slow we drop with a WARN rather than block kernel processing.
func (m *Monitor) emit(ctx context.Context, ev Event) {
	select {
	case m.Events <- ev:
	case <-ctx.Done():
	default:
		m.log.WarnContext(ctx, "monitor: Events channel full; dropping event",
			"kind", ev.Kind.String(), "iface", ev.Iface)
	}
}

// addrUpdateToEvent translates one AddrUpdate into our Event type. Returns
// EvUnknown for events on other interfaces or with empty CIDR.
func (m *Monitor) addrUpdateToEvent(u netlink.AddrUpdate) Event {
	if m.ifIndex != 0 && u.LinkIndex != m.ifIndex {
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
	return Event{
		Kind:   kind,
		Family: fam,
		Iface:  m.cfg.Iface,
		Dest:   "",
		Via:    "",
		CIDR:   cidr,
	}
}

// routeUpdateToEvent translates one RouteUpdate into our Event type. Filters
// to default routes via the watched iface.
func (m *Monitor) routeUpdateToEvent(u netlink.RouteUpdate) Event {
	r := u.Route
	if m.ifIndex != 0 && r.LinkIndex != m.ifIndex {
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
	if !isDefaultRoute(r, famConst) {
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
	return Event{
		Kind:   kind,
		Family: fam,
		Iface:  m.cfg.Iface,
		Dest:   "default",
		Via:    via,
		CIDR:   "",
	}
}

// linkUpdateToEvent translates one LinkUpdate into our Event type, emitting
// only when the watched iface transitions across up/down. The kernel sends
// many LinkUpdates for unrelated state bits; we suppress noise.
func (m *Monitor) linkUpdateToEvent(u netlink.LinkUpdate) Event {
	if m.ifIndex != 0 && int(u.Index) != m.ifIndex {
		return unknownEvent()
	}
	switch u.Header.Type {
	case unix.RTM_NEWLINK:
		// IFF_UP set means administratively up. We use OperState for "really up".
		if u.Attrs() == nil {
			return unknownEvent()
		}
		switch u.Attrs().OperState {
		case netlink.OperUp, netlink.OperUnknown:
			return Event{Kind: EvLinkUp, Family: "", Iface: m.cfg.Iface, Dest: "", Via: "", CIDR: ""}
		case netlink.OperDown, netlink.OperLowerLayerDown, netlink.OperNotPresent:
			return Event{Kind: EvLinkDown, Family: "", Iface: m.cfg.Iface, Dest: "", Via: "", CIDR: ""}
		}
	case unix.RTM_DELLINK:
		return Event{Kind: EvLinkDown, Family: "", Iface: m.cfg.Iface, Dest: "", Via: "", CIDR: ""}
	}
	return unknownEvent()
}

// IfIndex returns the netlink index of the watched interface (0 if unknown).
// Exposed for diagnostic logging by callers.
func (m *Monitor) IfIndex() int { return m.ifIndex }

func unknownEvent() Event {
	return Event{
		Kind:   EvUnknown,
		Family: "",
		Iface:  "",
		Dest:   "",
		Via:    "",
		CIDR:   "",
	}
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
