package health

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/netip"
	"sync"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/statuspush"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	moduleName            = "health"
	alertKindWANUnhealthy = "wan-health"
)

type pingFunc func(
	context.Context,
	string,
	netip.Addr,
	time.Duration,
) (time.Duration, error)

type httpFunc func(context.Context, string, string, time.Duration) (int, error)

// State stays wire-compatible with the shell state file while preventing
// arbitrary strings from entering the module's hysteresis state machine.
type State string

const (
	// StateUnknown preserves the shell warmup state until a threshold is met.
	StateUnknown State = "unknown"
	// StateHealthy records that consecutive cycles met recovery.
	StateHealthy State = "healthy"
	// StateUnhealthy records that consecutive cycles met failure.
	StateUnhealthy State = "unhealthy"
)

// WAN embeds the shared identity and carries optional health-policy overrides
// plus the provider's tier. The tier is here because the module computes the
// active tier for the status it pushes, and it already holds the verdict half
// of that computation.
type WAN struct {
	ifmgr.WANRef
	Tier              uint8
	TargetsV4         []netip.Addr
	TargetsV6         []netip.Addr
	HTTPURLs          []string
	PingCount         int
	SuccessThreshold  int
	FailureThreshold  int
	RecoveryThreshold int
	CheckInterval     time.Duration
}

// targetsV4 and targetsV6 read nil and empty differently, unlike every other
// resolver here. A nil list is a family the provider named no targets for,
// which inherits the module-wide list; an empty list is a family the provider
// declared it has none of, which stays empty so the module probes nothing in
// it. Without that distinction an IPv4-only provider would inherit the public
// resolvers applyDefaults installs and ping them out a link with no IPv6.
//
// These two lists are the only statement of whether a provider's link carries
// an address family. Three facts about a link are independent and must not be
// read off one another: whether the link has IPv6 at all, which these lists
// declare; whether the ISP delegates a prefix, which npt-prefix carries; and
// whether the link has a static IPv4 address, which v4-source carries. An ISP
// can advertise working IPv6 on the link and delegate no prefix, and such a
// provider is probed over IPv6 and routed over IPv6 while getting no
// translation and no IPv6 source rule.
func (wan WAN) targetsV4(cfg Config) []netip.Addr {
	if wan.TargetsV4 != nil {
		return wan.TargetsV4
	}
	return cfg.TargetsV4
}

func (wan WAN) targetsV6(cfg Config) []netip.Addr {
	if wan.TargetsV6 != nil {
		return wan.TargetsV6
	}
	return cfg.TargetsV6
}

func (wan WAN) httpURLs(cfg Config) []string {
	if len(wan.HTTPURLs) > 0 {
		return wan.HTTPURLs
	}
	return cfg.HTTPURLs
}

func (wan WAN) pingCount(cfg Config) int {
	if wan.PingCount != 0 {
		return wan.PingCount
	}
	return cfg.PingCount
}

func (wan WAN) successThreshold(cfg Config) int {
	if wan.SuccessThreshold != 0 {
		return wan.SuccessThreshold
	}
	return cfg.SuccessThreshold
}

func (wan WAN) failureThreshold(cfg Config) int {
	if wan.FailureThreshold != 0 {
		return wan.FailureThreshold
	}
	return cfg.FailureThreshold
}

func (wan WAN) recoveryThreshold(cfg Config) int {
	if wan.RecoveryThreshold != 0 {
		return wan.RecoveryThreshold
	}
	return cfg.RecoveryThreshold
}

// Config keeps module-wide probe policy as the fallback for per-WAN overrides.
type Config struct {
	StateFile         string
	PersistStateFile  string
	TargetsV4         []netip.Addr
	TargetsV6         []netip.Addr
	HTTPURLs          []string
	Timeout           time.Duration
	Interval          time.Duration
	PingCount         int
	SuccessThreshold  int
	FailureThreshold  int
	RecoveryThreshold int
	StatusPushCID     uint32
	StatusPushPort    uint32
	WANs              []WAN
}

// ModuleConfigName returns the registry key used by the WAN role.
func (Config) ModuleConfigName() string { return moduleName }

type wanStatus struct {
	State     State
	OKCount   int
	FailCount int
}

type probeResult struct {
	Passed bool
	// V6Probed and V4Probed record whether the provider carries that family at
	// all. A provider with no targets in a family is not probed in it, and its
	// zero counts below are the absence of a probe rather than a failed one.
	V6Probed       bool
	V4Probed       bool
	V6Successes    int
	V4Successes    int
	HTTP6Successes int
	HTTP4Successes int
}

type transition struct {
	WAN  WAN
	From State
	To   State
}

// statusSender is the push side of the status channel. The module depends on
// the interface so a test observes what it sends without opening a socket; the
// production implementation is statuspush.Sender.
type statusSender interface {
	Send(ctx context.Context, status statuspush.Status)
}

// Module serializes probe cycles so the interval loop and reconcile-triggered
// cycle cannot advance hysteresis from overlapping observations.
type Module struct {
	ifmgr.BaseModule

	cfg Config

	clock            internalclock.Clock
	cycleMu          sync.Mutex
	reconcileMu      sync.Mutex
	reconcilePending bool
	statuses         map[string]wanStatus
	// lastTransition records when each WAN's verdict last changed, for
	// the management surface. Guarded by cycleMu: only runCycle writes it.
	lastTransition map[string]time.Time

	probeV4    pingFunc
	probeV6    pingFunc
	probeHTTP6 httpFunc
	probeHTTP4 httpFunc

	// pusher delivers the cycle verdict to the hypervisor watchdog. Nil on a
	// host with no push address configured, which is every host but the two
	// gateways.
	pusher statusSender
}

// Init implements ifmgr.Module and binds the steady-state loop to daemon
// cancellation so no probe worker outlives the role instance.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", moduleName)
	log.InfoContext(
		ctx,
		"health: Init",
		"wan_count", len(m.cfg.WANs),
		"interval", m.cfg.Interval.String(),
	)
	if len(m.cfg.WANs) == 0 {
		log.WarnContext(ctx, "health: no WAN config; disabling module")
		return fmt.Errorf("%w: health: no [ifmgr.wan] WANs", ifmgr.ErrModuleDisabled)
	}
	if err := validateConfig(m.cfg); err != nil {
		log.WarnContext(ctx, "health: invalid config", "err", err)
		return fmt.Errorf("health: invalid config: %w", err)
	}
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	if m.probeV4 == nil {
		m.probeV4 = netif.Ping4
	}
	if m.probeV6 == nil {
		m.probeV6 = netif.Ping6
	}
	if m.probeHTTP6 == nil {
		m.probeHTTP6 = netif.HTTPCheck6
	}
	if m.probeHTTP4 == nil {
		m.probeHTTP4 = netif.HTTPCheck4
	}
	if err := m.loadStatuses(ctx, log); err != nil {
		return err
	}
	// Seed the state files. writeStateFiles tolerates a persist-mirror failure,
	// so any error here is a runtime-file write failure. The runtime file is the
	// required output, so fail Init rather than run blind on broken /var/run.
	if err := m.writeStateFiles(ctx, log, m.snapshotStatuses()); err != nil {
		log.WarnContext(ctx, "health: initialize runtime state failed", "err", err)
		return fmt.Errorf("health: initialize state files: %w", err)
	}
	// The per-cycle recover in runCycleGuarded keeps a panicking cycle from
	// killing the loop; this outer recover is a required last-resort backstop for
	// a panic in the loop's own control code (it should effectively never fire).
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(
					ctx,
					"health: probe loop panicked",
					"err", fmt.Sprint(recovered),
				)
			}
		}()
		m.probeLoop(ctx, log)
	}()
	return nil
}

// Reconcile runs an immediate cycle so later WAN-role modules read fresh state
// during the daemon's ordered startup reconcile.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.reconcileMu.Lock()
	if !m.reconcilePending {
		m.reconcileMu.Unlock()
		return nil
	}
	m.reconcilePending = false
	m.reconcileMu.Unlock()

	if err := m.runCycle(ctx, log); err != nil {
		m.reconcileMu.Lock()
		m.reconcilePending = true
		m.reconcileMu.Unlock()
		return err
	}
	return nil
}

// EvaluateAlerts implements ifmgr.Module; transitions emit synchronously from
// the probe cycle so the ten-second driver does not wait for daemon reconcile.
func (m *Module) EvaluateAlerts(_ context.Context, _ *slog.Logger, _ time.Time) {}

func (m *Module) probeLoop(ctx context.Context, log *slog.Logger) {
	// Use a timer reset after each cycle rather than a ticker so the full
	// interval always elapses between the end of one cycle and the start of the
	// next. A ticker queues a tick during a long cycle and fires it immediately,
	// collapsing the shell's post-cycle delay and probing back to back.
	timer := time.NewTimer(m.cfg.Interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.runCycleGuarded(ctx, log)
			timer.Reset(m.cfg.Interval)
		}
	}
}

// runCycleGuarded runs one probe cycle and recovers from a panic so a single
// bad cycle (for example inside a netif primitive) logs and the interval loop
// keeps publishing state, instead of the goroutine dying for the rest of the
// process lifetime. health is the sole source of WAN state for later WAN-role
// modules, so a permanently dead loop is a quiet, serious degradation.
func (m *Module) runCycleGuarded(ctx context.Context, log *slog.Logger) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.ErrorContext(
				ctx,
				"health: probe cycle panicked",
				"err", fmt.Sprint(recovered),
			)
		}
	}()
	if err := m.runCycle(ctx, log); err != nil {
		log.WarnContext(ctx, "health: interval probe cycle failed", "err", err)
	}
}

func (m *Module) runCycle(ctx context.Context, log *slog.Logger) error {
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()

	nextStatuses := m.snapshotStatuses()
	transitions := make([]transition, 0, len(m.cfg.WANs))
	results := make(map[string]probeResult, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		result := m.probeWAN(ctx, wan, log)
		results[wan.Name] = result
		current := nextStatuses[wan.Name]
		next, changed := advanceHealth(
			current,
			result.Passed,
			wan.failureThreshold(m.cfg),
			wan.recoveryThreshold(m.cfg),
		)
		nextStatuses[wan.Name] = next
		if changed {
			transitions = append(transitions, transition{
				WAN: wan, From: current.State, To: next.State,
			})
			// The clock is set by the constructor; a test that builds the
			// struct bare records the transition without its timestamp.
			if m.clock != nil {
				if m.lastTransition == nil {
					m.lastTransition = map[string]time.Time{}
				}
				m.lastTransition[wan.Name] = m.clock.Now()
			}
		}
		log.DebugContext(
			ctx,
			"health: probe result",
			"wan", wan.Name,
			"iface", wan.Iface,
			"passed", result.Passed,
			"v6_successes", result.V6Successes,
			"v4_successes", result.V4Successes,
			"http6_successes", result.HTTP6Successes,
			"http4_successes", result.HTTP4Successes,
			"state", next.State,
			"ok_count", next.OKCount,
			"fail_count", next.FailCount,
		)
	}
	if err := m.writeStateFiles(ctx, log, nextStatuses); err != nil {
		log.WarnContext(ctx, "health: write state files failed", "err", err)
		return fmt.Errorf("health: write state files: %w", err)
	}
	m.Lock()
	m.statuses = nextStatuses
	m.Unlock()
	m.publishLiveState(nextStatuses, results)
	m.emitTransitions(ctx, log, transitions)
	// One push per cycle, after the transitions are committed, so a verdict
	// change reaches the watchdog in the cycle that made it and a transition
	// cycle never sends the same thing twice.
	m.pushStatus(ctx, nextStatuses)
	return nil
}

// publishLiveState writes the cycle's outcome to the management surface's
// snapshot store, when this host serves one. Each family's result is its
// verdict leg, the same rule probeWAN combines: the ping success threshold
// or at least one HTTP success in that family. A family the provider does not
// carry keeps the unprobed value, because nothing was asked of it and reporting
// it as failing would show an IPv4-only provider as half broken.
func (m *Module) publishLiveState(
	statuses map[string]wanStatus,
	results map[string]probeResult,
) {
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}
	members := make(map[string]wanstate.MemberHealth, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		status := statuses[wan.Name]
		member := wanstate.MemberHealth{
			Verdict:             verdictOf(status.State),
			ConsecutiveFailures: 0,
			LastTransition:      m.lastTransition[wan.Name],
			V4:                  wanstate.ProbeNone,
			V6:                  wanstate.ProbeNone,
		}
		if status.FailCount > 0 && status.FailCount <= math.MaxUint32 {
			member.ConsecutiveFailures = uint32(status.FailCount)
		}
		if result, ran := results[wan.Name]; ran {
			threshold := wan.successThreshold(m.cfg)
			if result.V6Probed {
				member.V6 = legResult(result.V6Successes, result.HTTP6Successes, threshold)
			}
			if result.V4Probed {
				member.V4 = legResult(result.V4Successes, result.HTTP4Successes, threshold)
			}
		}
		members[wan.Name] = member
	}
	m.Env.LiveState.SetHealth(members)
}

// pushStatus tells the hypervisor watchdog what this cycle decided: every
// provider's verdict and the tier now carrying traffic. The watchdog holds no
// provider list of its own, so this message is the whole of what it knows.
func (m *Module) pushStatus(ctx context.Context, statuses map[string]wanStatus) {
	if m.pusher == nil {
		return
	}
	providers := make(map[string]string, len(m.cfg.WANs))
	states := make(netif.HealthStates, len(m.cfg.WANs))
	members := make([]netif.TierMember, 0, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		state := StateUnknown
		if status, ok := statuses[wan.Name]; ok && status.State.Valid() {
			state = status.State
		}
		providers[wan.Name] = string(state)
		states[wan.Name] = string(state)
		members = append(members, netif.TierMember{Name: wan.Name, Tier: wan.Tier})
	}
	// ActiveTier reports false when nothing is healthy. The tier then carries
	// no meaning and the provider map is what says so, with every entry
	// unhealthy, so the message shape stays fixed and the reader checks the map.
	activeTier, _ := netif.ActiveTier(members, states)
	m.pusher.Send(ctx, statuspush.Status{
		SentAt:     m.now(),
		ActiveTier: activeTier,
		Providers:  providers,
	})
}

// verdictOf maps the module's hysteresis state onto the management
// surface's verdict values.
func verdictOf(state State) wanstate.Health {
	switch state {
	case StateHealthy:
		return wanstate.HealthHealthy
	case StateUnhealthy:
		return wanstate.HealthUnhealthy
	case StateUnknown:
		return wanstate.HealthUnknown
	}
	return wanstate.HealthUnknown
}

// legResult reduces one family's probe counts to its served outcome.
func legResult(pingSuccesses int, httpSuccesses int, threshold int) wanstate.ProbeResult {
	if pingSuccesses >= threshold || httpSuccesses >= 1 {
		return wanstate.ProbePass
	}
	return wanstate.ProbeFail
}

func (m *Module) probeWAN(ctx context.Context, wan WAN, log *slog.Logger) probeResult {
	// A family the provider lists no ping targets for is a family it does not
	// carry, so neither leg of it runs: no echo request and no HTTP request
	// forced onto it. A provider on an IPv4-only link would otherwise spend
	// every cycle failing IPv6 probes it can never answer. The target lists are
	// the only input to this decision. Whether the ISP delegates a prefix is a
	// separate fact and never decides whether a family is probed: a provider
	// given IPv6 by router advertisement with no DHCPv6-PD delegation carries
	// IPv6 targets and is probed over IPv6 like any other.
	v6Targets := wan.targetsV6(m.cfg)
	v4Targets := wan.targetsV4(m.cfg)
	v6Probed := len(v6Targets) > 0
	v4Probed := len(v4Targets) > 0
	result := probeResult{
		Passed:         false,
		V6Probed:       v6Probed,
		V4Probed:       v4Probed,
		V6Successes:    0,
		V4Successes:    0,
		HTTP6Successes: 0,
		HTTP4Successes: 0,
	}
	if v6Probed {
		result.V6Successes = m.probeTargets(ctx, wan, v6Targets, m.probeV6)
		result.HTTP6Successes = m.probeHTTPURLs(ctx, wan, "inet6", m.probeHTTP6, log)
	}
	if v4Probed {
		result.V4Successes = m.probeTargets(ctx, wan, v4Targets, m.probeV4)
		result.HTTP4Successes = m.probeHTTPURLs(ctx, wan, "inet", m.probeHTTP4, log)
	}
	// Verdict matches health-check.sh check_wan_health: each address family the
	// provider carries forms its own leg (ping threshold OR at least one HTTP
	// success in that family), and the WAN is healthy when the IPv6 leg
	// (preferred, the P0 signal) or the IPv4 leg (fallback) passes. Every family
	// the provider carries is probed every cycle regardless of outcome (v6
	// primary, v4 always), so a v4 flap never fails a healthy-v6 WAN and a v6
	// outage can still fall back to v4. An IPv4-only HTTP success never vouches
	// for IPv6 and the reverse, and a family the provider does not carry votes
	// neither way.
	successThreshold := wan.successThreshold(m.cfg)
	v6Passed := v6Probed &&
		(result.V6Successes >= successThreshold || result.HTTP6Successes >= 1)
	v4Passed := v4Probed &&
		(result.V4Successes >= successThreshold || result.HTTP4Successes >= 1)
	result.Passed = v6Passed || v4Passed
	return result
}

// probeHTTPURLs runs every configured HTTP URL through one family-forced
// probe and counts successes for that family's verdict leg.
func (m *Module) probeHTTPURLs(
	ctx context.Context,
	wan WAN,
	family string,
	probe httpFunc,
	log *slog.Logger,
) int {
	successes := 0
	for _, url := range wan.httpURLs(m.cfg) {
		statusCode, err := probe(ctx, wan.Iface, url, m.cfg.Timeout)
		reachable := err == nil
		if reachable {
			successes++
		}
		log.DebugContext(
			ctx,
			"health: HTTP probe result",
			"wan", wan.Name,
			"iface", wan.Iface,
			"family", family,
			"url", url,
			"status_code", statusCode,
			"reachable", reachable,
			"err", err,
		)
	}
	return successes
}

func (m *Module) probeTargets(
	ctx context.Context,
	wan WAN,
	targets []netip.Addr,
	probe pingFunc,
) int {
	successes := 0
	for _, target := range targets {
		targetReached := false
		for range wan.pingCount(m.cfg) {
			if _, err := probe(ctx, wan.Iface, target, m.cfg.Timeout); err == nil {
				targetReached = true
			}
		}
		if targetReached {
			successes++
		}
	}
	return successes
}

func (m *Module) snapshotStatuses() map[string]wanStatus {
	m.Lock()
	defer m.Unlock()

	statuses := make(map[string]wanStatus, len(m.statuses))
	maps.Copy(statuses, m.statuses)
	return statuses
}

// now reads the injected clock, falling back to the wall clock for a test that
// builds the struct bare. Init installs the real clock on the daemon path.
func (m *Module) now() time.Time {
	if m.clock == nil {
		return internalclock.Real{}.Now()
	}
	return m.clock.Now()
}

func advanceHealth(
	current wanStatus,
	cyclePassed bool,
	failureThreshold int,
	recoveryThreshold int,
) (wanStatus, bool) {
	next := current
	if cyclePassed {
		next.OKCount++
		next.FailCount = 0
		if next.OKCount >= recoveryThreshold {
			next.State = StateHealthy
		}
	} else {
		next.FailCount++
		next.OKCount = 0
		if next.FailCount >= failureThreshold {
			next.State = StateUnhealthy
		}
	}
	return next, next.State != current.State
}

func (m *Module) emitTransitions(
	ctx context.Context,
	log *slog.Logger,
	transitions []transition,
) {
	for _, event := range transitions {
		m.emitTransition(ctx, log, event)
	}
}

func (m *Module) emitTransition(
	ctx context.Context,
	log *slog.Logger,
	event transition,
) {
	log.InfoContext(
		ctx,
		"health: WAN state transition",
		"wan", event.WAN.Name,
		"iface", event.WAN.Iface,
		"from", event.From,
		"to", event.To,
	)
	// The management surface streams the committed transition, so a
	// subscriber learns of it at the moment it happens rather than on the
	// next poll. Hysteresis already ran; this is never a raw probe result.
	// It enqueues before the reconcile request so the tier change the
	// reconcile may cause cannot reach the stream ahead of the health
	// transition that caused it.
	if m.Env != nil && m.Env.LiveState != nil {
		m.Env.LiveState.NotifyHealthTransition(
			event.WAN.Name, verdictOf(event.From), verdictOf(event.To),
		)
	}
	// A health transition changes routing eligibility, so ask the daemon to
	// reconcile now rather than wait for the periodic tick; this makes
	// wan.routes failover event-driven.
	if m.Env != nil && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile(
			"health " + event.WAN.Name + " " + string(event.From) + "->" + string(event.To),
		)
	}
	if m.Env == nil || m.Env.Alerts == nil {
		return
	}
	now := m.clock.Now()
	if event.To == StateUnhealthy {
		// Alert even from the unknown warmup state: a WAN that is broken from
		// startup must raise its first unhealthy alert, not stay silent until it
		// has been healthy once.
		m.Env.Alerts.NotifyContext(
			ctx,
			now,
			slog.LevelWarn,
			alertKindWANUnhealthy,
			event.WAN.Name,
			"health: WAN became unhealthy",
			slog.String("iface", event.WAN.Iface),
		)
		return
	}
	if event.To == StateHealthy {
		// An unknown-to-healthy transition has no prior alert to resolve, so skip
		// the no-op resolve rather than emitting a spurious recovery.
		if event.From == StateUnknown {
			return
		}
		m.Env.Alerts.ResolveContext(
			ctx,
			now,
			alertKindWANUnhealthy,
			event.WAN.Name,
			"health: WAN recovered",
			slog.String("iface", event.WAN.Iface),
		)
	}
}
