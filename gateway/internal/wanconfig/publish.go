package wanconfig

import (
	"context"
	"fmt"
	"log/slog"
)

// Publisher is the one capability this package needs from the management
// datastore: replace the owned subtrees with the given items in a single
// transaction. The linux daemon satisfies it with the sysrepo binding; tests
// satisfy it with a recorder. Keeping the dependency at this boundary keeps
// the whole package portable and testable on every platform.
type Publisher interface {
	ReplaceConfig(ctx context.Context, ownedPaths []string, items []Item) error
}

// Publish projects the gateway onto the model and writes it through pub,
// replacing the owned subtrees in one transaction. The caller decides what a
// failure means; the daemon logs it and keeps running, because describing
// the system is not a precondition for running it.
func Publish(ctx context.Context, log *slog.Logger, pub Publisher, gateway Gateway) error {
	items, err := ConfigItems(gateway)
	if err != nil {
		log.ErrorContext(ctx, "wanconfig: projection failed", "err", err)
		return fmt.Errorf("wanconfig: project configuration: %w", err)
	}
	if err := pub.ReplaceConfig(ctx, OwnedPaths, items); err != nil {
		log.ErrorContext(ctx, "wanconfig: publish failed", "err", err, "items", len(items))
		return fmt.Errorf("wanconfig: publish configuration: %w", err)
	}
	log.InfoContext(ctx, "wanconfig: configuration published",
		"members", len(gateway.Members), "items", len(items))
	return nil
}
