package commandline

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
	notifHealthPath = "/" + steeringPrefix + ":health-transition"
	notifTierPath   = "/" + steeringPrefix + ":tier-change"

	// Drop notifications when the queue fills. Readers can query current datastore
	// state.
	notifyQueueDepth = 16

	notifySendTimeout = 5 * time.Second
)

type surfaceEvent struct {
	path  string
	items []yangpub.Item
}

// Send notifications in a goroutine to keep reconciliation responsive.
type surfaceNotifier struct {
	log *slog.Logger
	// Use interface names to identify notifications in the datastore.
	ifaces map[string]string
	queue  chan surfaceEvent
}

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

func (n *surfaceNotifier) HealthTransition(
	member string,
	from wanstate.Health,
	currentHealth wanstate.Health,
) {
	iface, known := n.ifaces[member]
	if !known {
		// Only members in the published tree can emit health notifications.
		return
	}

	n.enqueue(surfaceEvent{
		path: notifHealthPath,
		items: []yangpub.Item{
			{Path: "interface", Value: iface},
			{Path: "health", Value: string(currentHealth)},
			{Path: "previous-health", Value: string(from)},
		},
	})
}

func (n *surfaceNotifier) TierChange(from uint8, activeTier uint8) {
	n.enqueue(surfaceEvent{
		path: notifTierPath,
		items: []yangpub.Item{
			{Path: "active-tier", Value: strconv.FormatUint(uint64(activeTier), 10)},
			{Path: "previous-tier", Value: strconv.FormatUint(uint64(from), 10)},
		},
	})
}

func (n *surfaceNotifier) enqueue(event surfaceEvent) {
	select {
	case n.queue <- event:
	default:
		n.log.Warn("wanconfig: notification queue full; event dropped", "path", event.path)
	}
}

// Wait for the sender before closing the datastore connection.
// Sysrepo uses the connection until each send returns.
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
				log.ErrorContext(
					ctx,
					"wanconfig: notifier panicked; transitions stop streaming",
					"err",
					fmt.Sprint(recovered),
				)
			}
		}()
		notifier.run(ctx, pub)
	}()

	return done
}

func (n *surfaceNotifier) run(ctx context.Context, pub yangpub.Notifier) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-n.queue:
			sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
			if err := pub.SendNotification(sendCtx, event.path, event.items); err != nil {
				n.log.ErrorContext(
					sendCtx,
					"wanconfig: notification send failed",
					"path",
					event.path,
					"err",
					err,
				)
			}

			cancel()
		}
	}
}
