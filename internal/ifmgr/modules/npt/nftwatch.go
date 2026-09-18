package npt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/nftables"
)

// nftMonitorRetryDelay is the backoff before re-establishing the nftables
// monitor after it errors or its socket closes. Short so a transient netlink
// error does not leave npt blind to a flush for long.
const nftMonitorRetryDelay = 2 * time.Second

// watchNFTChanges runs for the module's lifetime and asks the daemon to
// reconcile whenever the ip6 nat table or its chains are deleted. This is the
// fix for the wipe problem: an `nft -f` or `systemctl restart nftables` reloads
// the static ruleset, which flushes and recreates the ip6 nat table and so
// removes npt's runtime rules. Without an event npt would re-add them only on
// the periodic tick, leaving an IPv6 outage of up to one interval. The nftables
// kernel subsystem emits netlink notifications on ruleset changes, so npt reacts
// to the table delete and re-adds its rules within a cycle. The classifier
// matches only table and chain deletes, never rule deletes, so npt's own
// FlushChain during a reconcile cannot retrigger the watch (see
// nftEventWipesNAT).
func (m *Module) watchNFTChanges(ctx context.Context, log *slog.Logger) {
	log = log.With("goroutine", "nft-watch")
	log.DebugContext(ctx, "npt: nft-watch starting")
	for {
		if ctx.Err() != nil {
			log.DebugContext(ctx, "npt: nft-watch exiting (ctx done)")
			return
		}
		err := m.runNFTMonitor(ctx, log)
		if ctx.Err() != nil {
			log.DebugContext(ctx, "npt: nft-watch exiting (ctx done)")
			return
		}
		log.WarnContext(ctx, "npt: nft monitor ended; retrying", "err", err,
			"retry_in", nftMonitorRetryDelay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(nftMonitorRetryDelay):
		}
	}
}

// runNFTMonitor opens one nftables ruleset monitor and forwards ip6 nat wipes
// to the daemon as reconcile requests until the socket closes or ctx is done.
// A dedicated netlink connection is used so the monitor's long-lived socket is
// independent of the applier's programming connection.
func (m *Module) runNFTMonitor(ctx context.Context, log *slog.Logger) error {
	conn, err := nftables.New()
	if err != nil {
		log.WarnContext(ctx, "npt: nft monitor open conn failed", "err", err)
		return fmt.Errorf("nftables.New: %w", err)
	}
	monitor := nftables.NewMonitor(
		nftables.WithMonitorAction(nftables.MonitorActionAny),
		nftables.WithMonitorObject(nftables.MonitorObjectRuleset),
	)
	events, err := conn.AddMonitor(monitor)
	if err != nil {
		// Do not call monitor.Close() here. AddMonitor cleans up its own
		// failure paths, and when the netlink dial fails it returns before
		// setting monitor's closer, so Close would call a nil closer and panic.
		log.WarnContext(ctx, "npt: nft AddMonitor failed", "err", err)
		return fmt.Errorf("nftables AddMonitor: %w", err)
	}
	log.DebugContext(ctx, "npt: nft monitor established")

	for {
		select {
		case <-ctx.Done():
			// Close the monitor, then drain events until the channel closes.
			// google/nftables runs a forwarding goroutine that blocks sending
			// on the unbuffered events channel. Returning without draining can
			// strand it mid-send; draining lets it finish the in-flight send,
			// observe the closed source channel after Close, and exit.
			monitor.Close()
			for discarded := range events {
				_ = discarded
			}
			return fmt.Errorf("npt: nft monitor stopping: %w", ctx.Err())
		case event, ok := <-events:
			if !ok {
				monitor.Close()
				return errors.New("nft monitor channel closed")
			}
			m.handleNFTEvent(ctx, log, event)
		}
	}
}

// handleNFTEvent requests a reconcile when event deleted part of the ip6 nat
// table. Kept out of the monitor loop body so its info log is a per-flush state
// change, not a per-iteration emission.
func (m *Module) handleNFTEvent(
	ctx context.Context, log *slog.Logger, event *nftables.MonitorEvent,
) {
	if event.Error != nil {
		log.WarnContext(ctx, "npt: nft monitor event error", "err", event.Error)
		return
	}
	if !nftEventWipesNAT(event) {
		return
	}
	log.InfoContext(ctx, "npt: ip6 nat table change detected; requesting reconcile",
		"event_type", int(event.Type))
	if m.Env != nil && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("npt: ip6 nat table change")
	}
}

// nftEventWipesNAT reports whether a monitor event removed the ip6 nat table or
// one of its chains, the structural wipes npt must repair. It matches a delete
// of the table or a chain in family IPv6 table "nat", and deliberately does NOT
// match a rule delete.
//
// Rule deletes are excluded to avoid a feedback loop: npt's own applier flushes
// both managed chains on every reconcile (applier.go Apply calls FlushChain),
// which emits DelRule events for the ip6 nat table. Matching DelRule would make
// each reconcile request another reconcile and spin. FlushChain removes rules
// but never the chain or table, so DelChain and DelTable never come from npt's
// own reconcile and are safe wipe signals.
//
// The MWAN-4 cases this repairs (nft -f, systemctl restart nftables) reload the
// static ruleset, which begins with `flush ruleset` and deletes the ip6 nat
// table, so a DelTable event fires and drives the re-add. A targeted rules-only
// flush (nft flush table ip6 nat) leaves the table and is recovered by the
// periodic reconcile tick instead, unchanged from today.
func nftEventWipesNAT(event *nftables.MonitorEvent) bool {
	if event == nil {
		return false
	}
	// Switch on the payload type rather than the event-type enum: only a delete
	// of the ip6 nat table or of a chain inside it is a structural wipe. A
	// create event carries the same payload types, so the event type is checked
	// too.
	switch data := event.Data.(type) {
	case *nftables.Table:
		return event.Type == nftables.MonitorEventTypeDelTable && isNATTable(data)
	case *nftables.Chain:
		return event.Type == nftables.MonitorEventTypeDelChain && isNATTable(data.Table)
	default:
		return false
	}
}

// isNATTable reports whether table is the IPv6 nat table npt owns.
func isNATTable(table *nftables.Table) bool {
	return table != nil &&
		table.Family == nftables.TableFamilyIPv6 &&
		table.Name == natTableName
}
