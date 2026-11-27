package firewall

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/nftables"
)

const nftMonitorRetryDelay = 2 * time.Second

func (m *Module) watchNFTChanges(ctx context.Context, log *slog.Logger) {
	for ctx.Err() == nil {
		err := m.runNFTMonitor(ctx, log)
		if ctx.Err() != nil {
			return
		}

		log.WarnContext(ctx, "firewall nft monitor stopped", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(nftMonitorRetryDelay):
		}
	}
}

func (m *Module) runNFTMonitor(ctx context.Context, log *slog.Logger) error {
	events, closeMonitor, err := subscribeNFTEvents(ctx, log)
	if err != nil {
		return err
	}
	defer closeMonitor()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return errors.New("nftables monitor channel closed")
			}

			m.handleNFTEvent(ctx, log, event)
		}
	}
}

func subscribeNFTEvents(
	ctx context.Context,
	log *slog.Logger,
) (<-chan *nftables.MonitorEvent, func(), error) {
	conn, err := nftables.New()
	if err != nil {
		log.WarnContext(ctx, "open nftables monitor connection failed", "err", err)
		return nil, nil, fmt.Errorf("open nftables connection: %w", err)
	}

	monitor := nftables.NewMonitor(
		nftables.WithMonitorAction(nftables.MonitorActionAny),
		nftables.WithMonitorObject(nftables.MonitorObjectRuleset),
	)
	events, err := conn.AddMonitor(monitor)
	if err != nil {
		log.WarnContext(ctx, "start nftables monitor failed", "err", err)
		return nil, nil, fmt.Errorf("start nftables monitor: %w", err)
	}

	return events, func() {
		if err := monitor.Close(); err != nil {
			log.WarnContext(ctx, "close nftables monitor failed", "err", err)
		}

		for discarded := range events {
			_ = discarded
		}
	}, nil
}

func (m *Module) handleNFTEvent(
	ctx context.Context,
	log *slog.Logger,
	event *nftables.MonitorEvent,
) {
	if event == nil {
		return
	}

	if event.Error != nil {
		log.WarnContext(ctx, "firewall nft monitor event failed", "err", event.Error)
		return
	}

	if !nftEventDeletesOwnedStructure(event) {
		return
	}

	log.InfoContext(ctx, "firewall table or chain deleted", "event_type", int(event.Type))
	if m.Env != nil && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("firewall table or chain deleted")
	}
}

func nftEventDeletesOwnedStructure(event *nftables.MonitorEvent) bool {
	if event == nil {
		return false
	}

	switch data := event.Data.(type) {
	case *nftables.Table:
		return event.Type == nftables.MonitorEventTypeDelTable && ownedTable(data)
	case *nftables.Chain:
		return event.Type == nftables.MonitorEventTypeDelChain && ownedTable(data.Table)
	default:
		return false
	}
}

func ownedTable(table *nftables.Table) bool {
	if table == nil {
		return false
	}

	switch table.Family {
	case nftables.TableFamilyINet:
		return table.Name == "filter" || table.Name == "mangle"
	case nftables.TableFamilyIPv4:
		return table.Name == "nat"
	case nftables.TableFamilyUnspecified, nftables.TableFamilyIPv6,
		nftables.TableFamilyARP, nftables.TableFamilyNetdev,
		nftables.TableFamilyBridge:
		return false
	default:
		return false
	}
}
