package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	// notifHealthPath and notifTierPath name the two notifications the
	// steering module defines.
	notifHealthPath = "/" + steeringPrefix + ":health-transition"
	notifTierPath   = "/" + steeringPrefix + ":tier-change"

	// notifyQueueDepth bounds the queue between a reconcile writer and
	// the sender goroutine. Transitions are rare, so the queue only fills
	// when the datastore has stalled, and then dropping is the right
	// behavior: the tree still carries the current state.
	notifyQueueDepth = 16

	// notifySendTimeout bounds one send so a stalled datastore costs the
	// sender goroutine one bounded call per event, never the writers.
	notifySendTimeout = 5 * time.Second
)

// surfaceEvent is one queued notification.
type surfaceEvent struct {
	path  string
	items []yangpub.Item
}

// surfaceNotifier turns store transitions into datastore notifications.
// Writers enqueue on their own goroutine; a single sender goroutine
// delivers. The queue is bounded and a full queue drops the event with a
// log line, so a stalled datastore can lose notifications but never slow
// a reconcile pass.
type surfaceNotifier struct {
	log *slog.Logger
	// ifaces maps a member's name onto its interface name, which is how
	// the tree and its notifications identify the member.
	ifaces map[string]string
	queue  chan surfaceEvent
}

// newSurfaceNotifier builds the notifier for the published gateway.
func newSurfaceNotifier(log *slog.Logger, gateway wanconfig.Gateway) *surfaceNotifier {
	ifaces := make(map[string]string, len(gateway.Members))
	for _, member := range gateway.Members {
		ifaces[member.Name] = member.Iface
	}
	return &surfaceNotifier{
		log:    log,
		ifaces: ifaces,
		queue:  make(chan surfaceEvent, notifyQueueDepth),
	}
}

// HealthTransition implements wanstate.Observer.
func (n *surfaceNotifier) HealthTransition(member string, from wanstate.Health, to wanstate.Health) {
	iface, known := n.ifaces[member]
	if !known {
		// A member outside the published gateway has no interface entry in
		// the tree, so there is nothing coherent to notify about.
		return
	}
	n.enqueue(surfaceEvent{path: notifHealthPath, items: []yangpub.Item{
		{Path: "interface", Value: iface},
		{Path: "health", Value: string(to)},
		{Path: "previous-health", Value: string(from)},
	}})
}

// TierChange implements wanstate.Observer.
func (n *surfaceNotifier) TierChange(from uint8, to uint8) {
	n.enqueue(surfaceEvent{path: notifTierPath, items: []yangpub.Item{
		{Path: "active-tier", Value: strconv.FormatUint(uint64(to), 10)},
		{Path: "previous-tier", Value: strconv.FormatUint(uint64(from), 10)},
	}})
}

// enqueue hands one event to the sender without ever blocking the
// writer.
func (n *surfaceNotifier) enqueue(event surfaceEvent) {
	select {
	case n.queue <- event:
	default:
		n.log.Warn("wanconfig: notification queue full; event dropped", "path", event.path)
	}
}

// startNotifierSender launches the sender goroutine and returns a
// channel that closes when the goroutine has fully returned. The caller
// must wait on it before releasing the datastore connection: a send can
// sit inside the binding for sysrepo's internal subscriber timeout, and
// freeing the connection under it crashes the process.
func startNotifierSender(
	ctx context.Context,
	log *slog.Logger,
	notifier *surfaceNotifier,
	pub yangpub.Notifier,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx,
					"wanconfig: notifier panicked; transitions stop streaming",
					"err", fmt.Sprint(recovered))
			}
		}()
		notifier.run(ctx, pub)
	}()
	return done
}

// run delivers queued events one at a time until ctx ends. Each send is
// bounded and a failure loses only that event; the tree itself still
// carries the current state for any reader.
func (n *surfaceNotifier) run(ctx context.Context, pub yangpub.Notifier) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-n.queue:
			sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
			if err := pub.SendNotification(sendCtx, event.path, event.items); err != nil {
				n.log.ErrorContext(sendCtx, "wanconfig: notification send failed",
					"path", event.path, "err", err)
			}
			cancel()
		}
	}
}
