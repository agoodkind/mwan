package main

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

func startOwnershipObservers(ctx context.Context, log *slog.Logger, store *wanstate.Store, connections []interfaceintent.Connection, cancel context.CancelCauseFunc) {
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
					err := fmt.Errorf("ownership observer %s panicked: %v", connection.ID, recovered)
					log.ErrorContext(ctx, "ifmgr: ownership observer panicked", "connection", connection.ID.String(), "err", err)
					cancel(err)
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
