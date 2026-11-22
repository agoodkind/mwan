package netif

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vishvananda/netlink"
)

const (
	addressReadinessIndex = 0
	routeReadinessIndex   = 1
	linkReadinessIndex    = 2
)

func (m *Monitor) subscribeAddr(ctx context.Context) {
	m.runSubscription(ctx, "address", addressReadinessIndex, m.subscribeAddressUpdates)
}

func (m *Monitor) subscribeRoute(ctx context.Context) {
	m.runSubscription(ctx, "route", routeReadinessIndex, m.subscribeRouteUpdates)
}

func (m *Monitor) subscribeLink(ctx context.Context) {
	m.runSubscription(ctx, "link", linkReadinessIndex, m.subscribeLinkUpdates)
}

func (m *Monitor) subscribeAddressUpdates(
	ctx context.Context,
	logger *slog.Logger,
	subscriptionDone chan struct{},
	failed chan error,
) (func() (Event, bool, error), error) {
	updates := make(chan netlink.AddrUpdate, monitorEventBufferSize)
	err := netlink.AddrSubscribeWithOptions(
		updates,
		subscriptionDone,
		netlink.AddrSubscribeOptions{
			ErrorCallback: subscriptionErrorCallback(m, addressReadinessIndex, "address", failed),
		},
	)
	if err != nil {
		logger.WarnContext(ctx, "monitor: address subscription failed", "err", err)

		return nil, fmt.Errorf("subscribe addresses: %w", err)
	}

	return func() (Event, bool, error) {
		return receiveAddressUpdate(m, updates, failed)
	}, nil
}

func (m *Monitor) subscribeRouteUpdates(
	ctx context.Context,
	logger *slog.Logger,
	subscriptionDone chan struct{},
	failed chan error,
) (func() (Event, bool, error), error) {
	updates := make(chan netlink.RouteUpdate, monitorEventBufferSize)
	err := netlink.RouteSubscribeWithOptions(
		updates,
		subscriptionDone,
		netlink.RouteSubscribeOptions{
			ErrorCallback: subscriptionErrorCallback(m, routeReadinessIndex, "route", failed),
		},
	)
	if err != nil {
		logger.WarnContext(ctx, "monitor: route subscription failed", "err", err)

		return nil, fmt.Errorf("subscribe routes: %w", err)
	}

	return func() (Event, bool, error) {
		return receiveRouteUpdate(m, updates, failed)
	}, nil
}

func (m *Monitor) subscribeLinkUpdates(
	ctx context.Context,
	logger *slog.Logger,
	subscriptionDone chan struct{},
	failed chan error,
) (func() (Event, bool, error), error) {
	updates := make(chan netlink.LinkUpdate, monitorEventBufferSize)
	err := netlink.LinkSubscribeWithOptions(
		updates,
		subscriptionDone,
		netlink.LinkSubscribeOptions{
			ErrorCallback: subscriptionErrorCallback(m, linkReadinessIndex, "link", failed),
		},
	)
	if err != nil {
		logger.WarnContext(ctx, "monitor: link subscription failed", "err", err)

		return nil, fmt.Errorf("subscribe links: %w", err)
	}

	return func() (Event, bool, error) {
		return receiveLinkUpdate(m, updates, failed)
	}, nil
}

func subscriptionErrorCallback(
	monitor *Monitor,
	readinessIndex int,
	name string,
	failed chan<- error,
) func(error) {
	return func(err error) {
		monitor.setSubscriptionReady(readinessIndex, false, name+" subscription lost: "+err.Error())
		select {
		case failed <- err:
		default:
		}
	}
}

func receiveAddressUpdate(
	monitor *Monitor,
	updates <-chan netlink.AddrUpdate,
	failed <-chan error,
) (Event, bool, error) {
	select {
	case <-monitor.done:
		return unknownEvent(), false, nil
	case err := <-failed:
		return unknownEvent(), false, err
	case update, ok := <-updates:
		if !ok {
			return unknownEvent(), false, nil
		}

		return monitor.addrUpdateToEvent(update), true, nil
	}
}

func receiveRouteUpdate(
	monitor *Monitor,
	updates <-chan netlink.RouteUpdate,
	failed <-chan error,
) (Event, bool, error) {
	select {
	case <-monitor.done:
		return unknownEvent(), false, nil
	case err := <-failed:
		return unknownEvent(), false, err
	case update, ok := <-updates:
		if !ok {
			return unknownEvent(), false, nil
		}

		return monitor.routeUpdateToEvent(update), true, nil
	}
}

func receiveLinkUpdate(
	monitor *Monitor,
	updates <-chan netlink.LinkUpdate,
	failed <-chan error,
) (Event, bool, error) {
	select {
	case <-monitor.done:
		return unknownEvent(), false, nil
	case err := <-failed:
		return unknownEvent(), false, err
	case update, ok := <-updates:
		if !ok {
			return unknownEvent(), false, nil
		}

		return monitor.linkUpdateToEvent(update), true, nil
	}
}

func (m *Monitor) runSubscription(
	ctx context.Context,
	subscriptionName string,
	readinessIndex int,
	subscribe func(
		context.Context,
		*slog.Logger,
		chan struct{},
		chan error,
	) (func() (Event, bool, error), error),
) {
	logger := m.log.With("goroutine", "subscribe-"+subscriptionName)
	logger.DebugContext(ctx, "monitor: subscription starting")
	backoff := monitorRetryInitial

	for ctx.Err() == nil {
		failed := make(chan error, 1)
		subscriptionDone := make(chan struct{})
		receiveUpdate, err := subscribe(ctx, logger, subscriptionDone, failed)
		if err != nil {
			close(subscriptionDone)
			m.setSubscriptionReady(
				readinessIndex,
				false,
				subscriptionName+" subscription failed: "+err.Error(),
			)
		} else {
			backoff = monitorRetryInitial
			m.setSubscriptionReady(readinessIndex, true, "")

			for active := true; active; {
				event, received, receiveErr := receiveUpdate()
				if receiveErr != nil {
					logger.WarnContext(ctx, "monitor: subscription lost", "err", receiveErr)
					active = false

					continue
				}

				if !received {
					active = false

					continue
				}

				if event.Kind != EvUnknown {
					m.dispatchMu.Lock()
					m.emit(ctx, event)
					m.dispatchMu.Unlock()
				}
			}

			close(subscriptionDone)
			m.setSubscriptionReady(readinessIndex, false, subscriptionName+" subscription closed")
			select {
			case <-m.done:
				return
			default:
			}
		}

		if !sleepMonitorRetry(ctx, backoff) {
			return
		}

		backoff = min(backoff*monitorRetryMultiplier, monitorRetryMaximum)
	}
}
