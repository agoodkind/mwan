package commandline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

var errOwnershipObserverPanicked = errors.New("ownership observer panicked")

func startOwnershipObservers(
	ctx context.Context,
	log *slog.Logger,
	store *wanstate.Store,
	connections []interfaceintent.Connection,
	cancel context.CancelCauseFunc,
) {
	store.SetConnections(connections)

	for i := range connections {
		connection := connections[i]
		config := netif.MonitorConfig{
			Iface:        connection.Name,
			ConnectionID: connection.ID.String(),
			Connection:   nil,
		}
		if connection.Link != nil {
			config.Connection = &connection
		}

		monitor := netif.NewMonitor(ctx, log, config)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					err := fmt.Errorf(
						"ownership observer %s panicked: %w",
						connection.ID,
						errOwnershipObserverPanicked,
					)
					log.ErrorContext(
						ctx,
						"ifmgr: ownership observer panicked",
						"connection",
						connection.ID.String(),
						"recovered",
						recovered,
						"err",
						err,
					)
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
