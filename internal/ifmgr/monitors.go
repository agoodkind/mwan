package ifmgr

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/netif"
)

// MonitorEventHandler handles one kernel event drained from a per-iface
// monitor. It runs in the monitor's goroutine, so it must return promptly and
// must not block indefinitely.
type MonitorEventHandler func(ctx context.Context, log *slog.Logger, ev netif.Event)

// StartIfaceMonitors starts one netif.Monitor per interface in ifaces. Each
// monitor is drained in its own panic-guarded goroutine that invokes handle for
// every event it receives. The goroutines exit when ctx is cancelled or the
// monitor's event channel closes. moduleName tags the log messages so multiple
// modules using this helper stay distinguishable in the journal.
func StartIfaceMonitors(
	ctx context.Context,
	log *slog.Logger,
	moduleName string,
	ifaces []string,
	handle MonitorEventHandler,
) {
	for _, iface := range ifaces {
		monitor := netif.NewMonitor(ctx, log, netif.MonitorConfig{Iface: iface})
		go func(monitoredIface string, monitored *netif.Monitor) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				log.ErrorContext(ctx, moduleName+": monitor goroutine panicked",
					"iface", monitoredIface, "err", fmt.Sprint(recovered))
			}()
			drainIfaceMonitor(ctx, log, moduleName, monitoredIface, monitored, handle)
		}(iface, monitor)
	}
}

func drainIfaceMonitor(
	ctx context.Context,
	log *slog.Logger,
	moduleName string,
	iface string,
	monitor *netif.Monitor,
	handle MonitorEventHandler,
) {
	log = log.With("monitor_iface", iface)
	for {
		select {
		case <-ctx.Done():
			log.DebugContext(ctx, moduleName+": monitor drain exiting")
			return
		case event, ok := <-monitor.Events:
			if !ok {
				log.WarnContext(ctx, moduleName+": monitor event channel closed")
				return
			}
			handle(ctx, log, event)
		}
	}
}
