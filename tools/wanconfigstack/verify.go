package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// verifyRoot is where the bundle under verification is unpacked.
const verifyRoot = "/verify"

// verifyBinaries are the programs a gateway runs from the stack, with the
// argument that makes each print and exit. Each must start on a stock
// trixie system that installed only the bundle.
var verifyBinaries = []struct {
	program program
	arg     string
}{
	{program: programSysrepoctl, arg: "--version"},
	{program: programRousette, arg: "--help"},
}

var errBundleMember = errors.New("bundle member is neither the manifest nor a .deb")

// verify installs a bundle into a stock trixie container the way the
// gateway will, then proves the stack runs there: every package installs
// through apt, every shared library each stack binary needs resolves, and
// the datastore tool and the RESTCONF server both start.
func (b *builder) verify(ctx context.Context, bundlePath string) error {
	if err := b.unpackBundle(ctx, bundlePath, verifyRoot); err != nil {
		return err
	}
	debs, err := filepath.Glob(filepath.Join(verifyRoot, "debs", "*.deb"))
	if err != nil {
		return b.fail(ctx, "list bundle packages", err)
	}
	if len(debs) == 0 {
		return b.fail(ctx, "list bundle packages", errNoPackages, slog.String("bundle", bundlePath))
	}
	if err := b.run(ctx, cmd(programAptGet, "update").with(aptEnv...)); err != nil {
		return err
	}
	if err := b.aptInstall(ctx, debs...); err != nil {
		return err
	}
	owners, err := b.fileOwners(ctx)
	if err != nil {
		return err
	}
	if _, err := b.shlibDepends(ctx, stackPrefix, owners); err != nil {
		return err
	}
	for _, binary := range verifyBinaries {
		if err := b.run(ctx, cmd(binary.program, binary.arg)); err != nil {
			return err
		}
	}
	b.log.InfoContext(ctx, "wanconfigstack: bundle verified", "bundle", bundlePath, "packages", len(debs))
	return nil
}

// unpackBundle extracts the bundle's packages under root/debs by their base
// name, so a member path can never land anywhere else. The manifest is read
// for its listing only.
func (b *builder) unpackBundle(ctx context.Context, bundlePath string, root string) error {
	file, err := os.Open(bundlePath)
	if err != nil {
		return b.fail(ctx, "open bundle", err, slog.String("path", bundlePath))
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return b.fail(ctx, "read bundle", err, slog.String("path", bundlePath))
	}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return b.fail(ctx, "read bundle member", err, slog.String("path", bundlePath))
		}
		if header.Name == manifestName {
			continue
		}
		if !strings.HasSuffix(header.Name, ".deb") || strings.Contains(header.Name, "..") {
			return b.fail(ctx, "unpack bundle", errBundleMember, slog.String("member", header.Name))
		}
		target := filepath.Join(root, "debs", filepath.Base(header.Name))
		if err := b.writeUnpacked(ctx, target, reader); err != nil {
			return err
		}
	}
	b.log.InfoContext(ctx, "wanconfigstack: bundle unpacked", "path", bundlePath, "dir", root)
	return nil
}

func (b *builder) writeUnpacked(ctx context.Context, target string, content io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return b.fail(ctx, "create unpack dir", err, slog.String("path", target))
	}
	out, err := os.Create(target)
	if err != nil {
		return b.fail(ctx, "create unpacked file", err, slog.String("path", target))
	}
	if _, err := io.Copy(out, content); err != nil {
		_ = out.Close()
		return b.fail(ctx, "write unpacked file", err, slog.String("path", target))
	}
	if err := out.Close(); err != nil {
		return b.fail(ctx, "close unpacked file", err, slog.String("path", target))
	}
	b.log.DebugContext(ctx, "wanconfigstack: unpacked", "path", target)
	return nil
}
