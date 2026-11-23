package commandline

import (
	"context"
	"log/slog"
	"testing"

	"goodkind.io/mwan/internal/commandline/commandopts"
	"goodkind.io/mwan/internal/yangpub"
)

func withPrivateRepositoryForTest(
	t *testing.T,
	ctx context.Context,
	log *slog.Logger,
	flags commandopts.Selftest,
	run func(yangpub.Publisher),
) {
	t.Helper()
	if err := withPrivateRepository(ctx, log, flags, run); err != nil {
		t.Fatal(err)
	}
}
