package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/mwan/internal/stackspec"
)

var errDownloadedFile = errors.New("apt-get download did not leave exactly one .deb")

func (b *builder) downloadDistribution(ctx context.Context) error {
	count := 0
	for _, pkg := range stackspec.Packages() {
		if !pkg.Distribution {
			continue
		}
		dir := filepath.Join(buildRoot, "pkgs", pkg.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return b.fail(ctx, "create package dir", err, slog.String("path", dir))
		}
		download := cmd(programAptGet, "download", pkg.Name+"="+pkg.Version).in(dir).with(aptEnv...)
		if err := b.run(ctx, download); err != nil {
			return err
		}
		downloaded, err := filepath.Glob(filepath.Join(dir, "*.deb"))
		if err != nil {
			return b.fail(ctx, "list downloaded package", err, slog.String("dir", dir))
		}
		if len(downloaded) != 1 {
			return b.fail(ctx, "list downloaded package", errDownloadedFile, slog.String("dir", dir))
		}
		target := filepath.Join(dir, pkg.FileName(b.arch))
		if err := os.Rename(downloaded[0], target); err != nil {
			return b.fail(ctx, "rename downloaded package", err, slog.String("path", downloaded[0]))
		}
		count++
	}
	b.log.InfoContext(ctx, "wanconfigstack: distribution packages downloaded", "count", count)
	return nil
}
