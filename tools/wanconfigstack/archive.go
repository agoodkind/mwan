package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// The sysrepo v3.7.11 template lists its plugin directories under the amd64
// multiarch directory, and its library lines glob that directory. dh_install
// rejects a listed path the build did not produce. An arm64 build installs
// the plugin directories under the arm64 multiarch directory. The glob
// matches the amd64 directory on amd64 and the arm64 directory on arm64.
const (
	amd64MultiarchSegment = "/x86_64-linux-gnu/"
	anyMultiarchSegment   = "/*/"
)

// installFileSuffix marks a dh_install list in a Debian template.
const installFileSuffix = ".install"

var errArchiveCount = errors.New("apkg get-archive did not produce exactly one .tar.gz")

func (b *builder) archiveDir(c component) string {
	return filepath.Join(buildRoot, "archives", c.name)
}

// upstreamArchive downloads the component's release archive for the pinned
// version and returns its path.
func (b *builder) upstreamArchive(ctx context.Context, c component) (string, error) {
	dir := b.archiveDir(c)
	if err := os.RemoveAll(dir); err != nil {
		return "", b.fail(ctx, "clear archive dir", err, slog.String("dir", dir))
	}
	download := cmd(programApkg, "get-archive", "--version", b.pinVersion(c), "--no-cache", "--result-dir", dir).
		in(b.srcDir(c))
	if err := b.run(ctx, download); err != nil {
		return "", err
	}
	archives, err := filepath.Glob(filepath.Join(dir, "*.tar.gz"))
	if err != nil {
		return "", b.fail(ctx, "list archives", err, slog.String("dir", dir))
	}
	if len(archives) != 1 {
		return "", b.fail(ctx, "find archive", errArchiveCount, slog.String("dir", dir), slog.Int("count", len(archives)))
	}
	b.log.InfoContext(ctx, "wanconfigstack: upstream archive downloaded", "component", c.name, "archive", archives[0])
	return archives[0], nil
}

// portInstallFiles rewrites every amd64 multiarch path in the template's
// .install files to the multiarch glob. An [os.Root] opened on the template
// directory performs every read and write, and it rejects any path outside
// that directory.
func (b *builder) portInstallFiles(ctx context.Context, templateDir string) error {
	root, err := os.OpenRoot(templateDir)
	if err != nil {
		return b.fail(ctx, "open template dir", err, slog.String("dir", templateDir))
	}
	defer func() { _ = root.Close() }()
	installFiles, err := fs.Glob(root.FS(), "*"+installFileSuffix)
	if err != nil {
		return b.fail(ctx, "list install files", err, slog.String("dir", templateDir))
	}
	portedFiles := []string{}
	for _, name := range installFiles {
		content, err := root.ReadFile(name)
		if err != nil {
			return b.fail(ctx, "read install file", err, slog.String("dir", templateDir), slog.String("file", name))
		}
		original := string(content)
		ported := strings.ReplaceAll(original, amd64MultiarchSegment, anyMultiarchSegment)
		if ported == original {
			continue
		}
		info, err := root.Stat(name)
		if err != nil {
			return b.fail(ctx, "stat install file", err, slog.String("dir", templateDir), slog.String("file", name))
		}
		if err := root.WriteFile(name, []byte(ported), info.Mode().Perm()); err != nil {
			return b.fail(ctx, "write install file", err, slog.String("dir", templateDir), slog.String("file", name))
		}
		portedFiles = append(portedFiles, name)
	}
	b.log.InfoContext(ctx, "wanconfigstack: install files ported to the multiarch glob", "dir", templateDir, "ported", portedFiles)
	return nil
}
