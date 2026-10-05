package steering

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/nftables"
)

// nftMonitorRetryDelay is the backoff before re-establishing the nftables
// monitor after it errors or its socket closes. Short, so a transient netlink
// error does not leave the module blind to a ruleset flush for long.
const nftMonitorRetryDelay = 2 * time.Second

// watchNFTChanges runs for the module's lifetime and asks the daemon to
// reconcile whenever this module's table or its chain is deleted. An `nft -f`
// or an nftables restart reloads the static ruleset, which begins by flushing
// the whole ruleset and so removes this table. Without an event the chain would
// come back only on the next periodic tick, and until then no new connection
// would be assigned a provider.
func (m *Module) watchNFTChanges(ctx context.Context, log *slog.Logger) {
	log = log.With("goroutine", "nft-watch")
	log.DebugContext(ctx, "steering: nft-watch starting")
	for {
		if ctx.Err() != nil {
			log.DebugContext(ctx, "steering: nft-watch exiting (ctx done)")
			return
		}
		err := m.runNFTMonitor(ctx, log)
		if ctx.Err() != nil {
			log.DebugContext(ctx, "steering: nft-watch exiting (ctx done)")
			return
		}
		log.WarnContext(ctx, "steering: nft monitor ended; retrying", "err", err,
			"retry_in", nftMonitorRetryDelay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(nftMonitorRetryDelay):
		}
	}
}

// runNFTMonitor opens one nftables ruleset monitor and forwards wipes of this
// module's table to the daemon as reconcile requests until the socket closes or
// ctx is done. A dedicated netlink connection keeps the monitor's long-lived
// socket independent of the applier's programming connection.
func (m *Module) runNFTMonitor(ctx context.Context, log *slog.Logger) error {
	conn, err := nftables.New()
	if err != nil {
		log.WarnContext(ctx, "steering: nft monitor open conn failed", "err", err)
		return fmt.Errorf("nftables.New: %w", err)
	}
	monitor := nftables.NewMonitor(
		nftables.WithMonitorAction(nftables.MonitorActionAny),
		nftables.WithMonitorObject(nftables.MonitorObjectRuleset),
	)
	events, err := conn.AddMonitor(monitor)
	if err != nil {
		// Do not close the monitor here. AddMonitor cleans up its own failure
		// paths, and when the netlink dial fails it returns before setting the
		// monitor's closer, so Close would call a nil closer and panic.
		log.WarnContext(ctx, "steering: nft AddMonitor failed", "err", err)
		return fmt.Errorf("nftables AddMonitor: %w", err)
	}
	log.DebugContext(ctx, "steering: nft monitor established")

	for {
		select {
		case <-ctx.Done():
			// Close the monitor, then drain events until the channel closes.
			// google/nftables runs a forwarding goroutine that blocks sending on
			// the unbuffered events channel; returning without draining can
			// strand it mid-send.
			monitor.Close()
			for discarded := range events {
				_ = discarded
			}
			return fmt.Errorf("steering: nft monitor stopping: %w", ctx.Err())
		case event, ok := <-events:
			if !ok {
				monitor.Close()
				return errors.New("nft monitor channel closed")
			}
			m.handleNFTEvent(ctx, log, event)
		}
	}
}

// handleNFTEvent requests a reconcile when event removed this module's table or
// chain. Kept out of the monitor loop body so its info log is a per-wipe state
// change rather than a per-iteration emission.
func (m *Module) handleNFTEvent(
	ctx context.Context, log *slog.Logger, event *nftables.MonitorEvent,
) {
	if event == nil {
		log.WarnContext(ctx, "steering: nft monitor delivered a nil event")
		return
	}
	if event.Error != nil {
		log.WarnContext(ctx, "steering: nft monitor event error", "err", event.Error)
		return
	}
	if !nftEventWipesSteering(event) {
		return
	}
	log.InfoContext(ctx, "steering: table change detected; requesting reconcile",
		"event_type", int(event.Type))
	if m.Env != nil && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("steering: table change")
	}
}

// nftEventWipesSteering reports whether a monitor event removed this module's
// table or its chain, the structural wipes the next reconcile must repair. It
// matches a delete of the table or of a chain inside it, and deliberately does
// not match a rule delete.
//
// Rule deletes are excluded to avoid a feedback loop: the module's own applier
// empties the chain on every reconcile, which emits rule deletes. Matching them
// would make each pass request another and spin. Emptying a chain removes rules
// but never the chain or the table, so a chain or table delete never comes from
// this module's own work.
//
// Creates are excluded for the same reason from the other direction: the
// applier creates the table and the chain on every pass, so their creation
// events are this module's own doing.
func nftEventWipesSteering(event *nftables.MonitorEvent) bool {
	if event == nil {
		return false
	}
	switch data := event.Data.(type) {
	case *nftables.Table:
		return event.Type == nftables.MonitorEventTypeDelTable && isSteerTable(data)
	case *nftables.Chain:
		return event.Type == nftables.MonitorEventTypeDelChain && isSteerTable(data.Table)
	default:
		return false
	}
}

// isSteerTable reports whether table is the inet table this module owns.
func isSteerTable(table *nftables.Table) bool {
	return table != nil &&
		table.Family == nftables.TableFamilyINet &&
		table.Name == steerTableName
}
