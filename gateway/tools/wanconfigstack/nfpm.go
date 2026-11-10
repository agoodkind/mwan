package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/goreleaser/nfpm/v2"
	"github.com/goreleaser/nfpm/v2/deb"
	"github.com/goreleaser/nfpm/v2/files"
)

// packageMaintainer is the Maintainer field of the nfpm packages.
const packageMaintainer = "Alexander Goodkind <alex@goodkind.io>"

// ldconfigHook is the maintainer script every /opt package runs after
// install and after removal, so the loader picks the tree up and drops it.
// The packager writes maintainer scripts executable whatever the source
// file's mode is.
const ldconfigHook = "#!/bin/sh\nldconfig\n"

// nfpmPackage wraps a component's stage as the Debian package
// mwan-wanconfig-NAME, with Depends read off the staged ELF files.
func (b *builder) nfpmPackage(ctx context.Context, c component, version string) error {
	stage := b.stageDir(c)
	owners, err := b.fileOwners(ctx)
	if err != nil {
		return err
	}
	depends, err := b.shlibDepends(ctx, stage, owners)
	if err != nil {
		return err
	}
	contents, err := b.stageContents(ctx, stage)
	if err != nil {
		return err
	}
	hook := filepath.Join(buildRoot, "ldconfig.sh")
	if err := os.WriteFile(hook, []byte(ldconfigHook), 0o600); err != nil {
		return b.fail(ctx, "write ldconfig hook", err, slog.String("path", hook))
	}
	info := nfpm.WithDefaults(&nfpm.Info{
		Name:            "mwan-wanconfig-" + c.name,
		Arch:            b.arch,
		Platform:        "linux",
		Version:         version,
		Section:         "libs",
		Priority:        "optional",
		Maintainer:      packageMaintainer,
		Description:     c.description,
		Homepage:        "https://github.com/agoodkind/configs",
		DisableGlobbing: true,
		Overridables: nfpm.Overridables{
			Depends:  depends,
			Contents: contents,
			Scripts:  nfpm.Scripts{PostInstall: hook, PostRemove: hook},
		},
	})
	if err := nfpm.PrepareForPackager(info, "deb"); err != nil {
		return b.fail(ctx, "prepare package", err, slog.String("name", info.Name))
	}
	pkgDir := b.pkgDir(c)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return b.fail(ctx, "create package dir", err, slog.String("path", pkgDir))
	}
	target := filepath.Join(pkgDir, deb.Default.ConventionalFileName(info))
	b.log.InfoContext(ctx, "wanconfigstack: package", "name", info.Name, "version", version, "depends", strings.Join(depends, ", "), "file", target)
	out, err := os.Create(target)
	if err != nil {
		return b.fail(ctx, "create package file", err, slog.String("path", target))
	}
	if err := deb.Default.Package(info, out); err != nil {
		_ = out.Close()
		return b.fail(ctx, "write package", err, slog.String("name", info.Name))
	}
	if err := out.Close(); err != nil {
		return b.fail(ctx, "close package file", err, slog.String("path", target))
	}
	return nil
}

// stageContents lists every file, symlink, and directory of a stage as
// package contents at the same absolute path, so the package reproduces the
// cmake install exactly. The loader conf is marked as configuration.
func (b *builder) stageContents(ctx context.Context, stage string) (files.Contents, error) {
	contents := files.Contents{}
	err := filepath.WalkDir(stage, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return b.fail(ctx, "walk stage", walkErr, slog.String("path", path))
		}
		destination := strings.TrimPrefix(path, stage)
		if destination == "" {
			return nil
		}
		content := files.Content{Source: path, Destination: destination, Type: files.TypeFile, Packager: "", FileInfo: nil, Expand: false}
		switch {
		case entry.IsDir():
			content.Source = ""
			content.Type = files.TypeDir
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return b.fail(ctx, "read symlink", err, slog.String("path", path))
			}
			content.Source = target
			content.Type = files.TypeSymlink
		case destination == loaderConfPath:
			content.Type = files.TypeConfig
		}
		contents = append(contents, &content)
		return nil
	})
	if err != nil {
		return nil, b.fail(ctx, "walk stage", err, slog.String("stage", stage))
	}
	return contents, nil
}
