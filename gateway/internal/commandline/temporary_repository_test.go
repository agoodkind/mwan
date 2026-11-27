package commandline

import (
	"context"
	"log/slog"
	"testing"

	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/yangpub"
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

func selftestModelsDir(t *testing.T) string {
	t.Helper()
	modelsDir := t.TempDir()
	if _, err := yangpub.WriteSchema(modelsDir); err != nil {
		t.Fatalf("write the embedded schema: %v", err)
	}

	return modelsDir
}
