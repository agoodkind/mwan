//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/mwan/gateway/internal/yangpub"
)

func WithPrivateRepository(
	t *testing.T,
	ctx context.Context,
	log *slog.Logger,
	directory string,
	run func(yangpub.Publisher),
) {
	t.Helper()
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("mwanintegration%d_", os.Getpid())
	t.Setenv("SYSREPO_REPOSITORY_PATH", directory)
	t.Setenv("SYSREPO_SHM_PREFIX", prefix)
	t.Setenv("SR_ENV_RUN_TESTS", "1")
	defer removeRepositoryMemory(t, prefix)

	modelsDir := t.TempDir()
	models, err := yangpub.WriteSchema(modelsDir)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := yangpub.New(log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := publisher.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := publisher.InstallModules(ctx, models, modelsDir); err != nil {
		t.Fatal(err)
	}
	run(publisher)
}

func removeRepositoryMemory(t *testing.T, prefix string) {
	t.Helper()
	segments, err := filepath.Glob(filepath.Join("/dev/shm", prefix+"*"))
	if err != nil {
		t.Error(err)
	}
	for _, segment := range segments {
		if err := os.Remove(segment); err != nil {
			t.Error(err)
		}
	}
}
