package main

import (
	"context"
	"log/slog"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

func startOwnershipObservers(ctx context.Context, log *slog.Logger, store *wanstate.Store, connections []interfaceintent.Connection) {
	store.SetConnections(connections)
	for i := range connections {
		connection := connections[i]
		config := netif.MonitorConfig{Iface: connection.Name, ConnectionID: connection.ID.String(), Connection: nil}
		if connection.Link != nil {
			config.Connection = &connection
		}
		monitor := netif.NewMonitor(ctx, log, config)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.ErrorContext(ctx, "ifmgr: ownership observer panicked", "connection", connection.ID.String(), "err", recovered)
				}
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-monitor.Events:
					store.RecordObservation(event)
				}
			}
		}()
	}
}
