package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

// quietLogger keeps the notifier's drop warnings out of test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// TestSurfaceNotifier_RendersTheNotifications pins what a subscriber
// receives for the two transitions: the member is named by its interface
// as everywhere else in the tree, and both directions of each change are
// carried.
func TestSurfaceNotifier_RendersTheNotifications(t *testing.T) {
	t.Parallel()
	notifier := newSurfaceNotifier(quietLogger(), liveTestGateway())

	notifier.HealthTransition("att", wanstate.HealthHealthy, wanstate.HealthUnhealthy)
	notifier.TierChange(0, 1)

	health := <-notifier.queue
	if health.path != "/goodkind-mwan-steering:health-transition" {
		t.Fatalf("health path = %s", health.path)
	}
	wantHealth := map[string]string{
		"interface": "enatt0", "health": "unhealthy", "previous-health": "healthy",
	}
	assertItems(t, health, wantHealth)

	tier := <-notifier.queue
	if tier.path != "/goodkind-mwan-steering:tier-change" {
		t.Fatalf("tier path = %s", tier.path)
	}
	assertItems(t, tier, map[string]string{"active-tier": "1", "previous-tier": "0"})
}

func TestSurfaceObserverRemovalPreservesNewRegistration(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	oldNotifier := newSurfaceNotifier(quietLogger(), liveTestGateway())
	currentNotifier := newSurfaceNotifier(quietLogger(), liveTestGateway())
	removeOld := store.Observe(oldNotifier)
	removeCurrent := store.Observe(currentNotifier)

	removeOld()
	store.NotifyHealthTransition("att", wanstate.HealthHealthy, wanstate.HealthUnhealthy)
	if len(oldNotifier.queue) != 0 || len(currentNotifier.queue) != 1 {
		t.Fatalf("observer events: old=%d current=%d", len(oldNotifier.queue), len(currentNotifier.queue))
	}

	removeCurrent()
	store.NotifyHealthTransition("att", wanstate.HealthUnhealthy, wanstate.HealthHealthy)
	if len(currentNotifier.queue) != 1 {
		t.Fatalf("removed observer received %d events", len(currentNotifier.queue))
	}
}

func assertItems(t *testing.T, event surfaceEvent, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, item := range event.items {
		got[item.Path] = item.Value
	}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for leaf, value := range want {
		if got[leaf] != value {
			t.Fatalf("leaf %s = %q, want %q", leaf, got[leaf], value)
		}
	}
}

// blockingNotifier stands in for the sysrepo binding at its interface
// boundary: SendNotification parks inside the call until released, the
// way a real send parks on a wedged subscriber, so the test can hold a
// send in flight deliberately.
type blockingNotifier struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingNotifier) SendNotification(_ context.Context, _ string, _ []yangpub.Item) error {
	close(b.entered)
	<-b.release
	return nil
}

func (b *blockingNotifier) SubscribeNotifications(_ context.Context, _ string, _ yangpub.NotificationFunc) error {
	return nil
}

// The management worker waits for in-flight sends before closing its datastore
// connection. Closing that connection during a native send crashes the process.
func TestStartNotifierSender_DoneWaitsForTheInFlightSend(t *testing.T) {
	t.Parallel()
	notifier := newSurfaceNotifier(quietLogger(), liveTestGateway())
	binding := &blockingNotifier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := startNotifierSender(ctx, quietLogger(), notifier, binding)

	notifier.TierChange(0, 1)
	<-binding.entered
	cancel()
	select {
	case <-done:
		t.Fatal("done closed while the send was still inside the binding")
	case <-time.After(50 * time.Millisecond):
	}

	close(binding.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("done never closed after the send returned")
	}
}

// TestSurfaceNotifier_UnknownMemberAndFullQueue pins the two guard
// behaviors: a member outside the published gateway produces nothing, and
// a full queue drops the event instead of blocking the writer.
func TestSurfaceNotifier_UnknownMemberAndFullQueue(t *testing.T) {
	t.Parallel()
	notifier := newSurfaceNotifier(quietLogger(), liveTestGateway())

	notifier.HealthTransition("stranger", wanstate.HealthHealthy, wanstate.HealthUnhealthy)
	if len(notifier.queue) != 0 {
		t.Fatalf("unknown member queued %d events", len(notifier.queue))
	}

	for range notifyQueueDepth + 3 {
		notifier.TierChange(0, 1)
	}
	if len(notifier.queue) != notifyQueueDepth {
		t.Fatalf("queue holds %d events, want the cap %d", len(notifier.queue), notifyQueueDepth)
	}
}
