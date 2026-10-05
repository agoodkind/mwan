package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// manifestName is the bundle member that lists every package it carries.
const manifestName = "manifest.txt"

var (
	errDuplicatePackage = errors.New("two built packages carry the same runtime package name")
	errMissingPackage   = errors.New("a runtime package has no built .deb")
)

// bundleMember is one package in the bundle.
type bundleMember struct {
	name    string
	version string
	path    string
	sha256  string
}

// bundle collects the runtime packages into wanconfig-stack_linux_ARCH.tar.gz
// under the output directory, with a manifest listing each package's name,
// version, architecture, and sha256.
func (b *builder) bundle(ctx context.Context) error {
	members, err := b.runtimeMembers(ctx)
	if err != nil {
		return err
	}
	target := filepath.Join(b.outDir, "wanconfig-stack_linux_"+b.arch+".tar.gz")
	text := manifest(b.arch, members)
	b.log.InfoContext(ctx, "wanconfigstack: bundle", "path", target, "manifest", text)
	file, err := os.Create(target)
	if err != nil {
		return b.fail(ctx, "create bundle", err, slog.String("path", target))
	}
	if err := b.writeBundle(ctx, file, text, members); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return b.fail(ctx, "close bundle", err, slog.String("path", target))
	}
	return nil
}

// runtimeMembers finds the built .deb of every runtime package.
func (b *builder) runtimeMembers(ctx context.Context) ([]bundleMember, error) {
	debs, err := filepath.Glob(filepath.Join(buildRoot, "pkgs", "*", "*.deb"))
	if err != nil {
		return nil, b.fail(ctx, "list built packages", err)
	}
	return b.collectRuntimeMembers(ctx, debs)
}

// collectRuntimeMembers reads each built package's identity and keeps the
// runtime ones, refusing a duplicate name and naming every missing package.
func (b *builder) collectRuntimeMembers(ctx context.Context, debs []string) ([]bundleMember, error) {
	members := []bundleMember{}
	found := map[string]string{}
	for _, debPath := range debs {
		name, version, err := b.debIdentity(ctx, debPath)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(runtimePackages, name) {
			continue
		}
		if previous, ok := found[name]; ok {
			return nil, b.fail(ctx, "collect runtime packages", errDuplicatePackage, slog.String("package", name), slog.String("first", previous), slog.String("second", debPath))
		}
		found[name] = debPath
		sum, err := b.fileSHA256(ctx, debPath)
		if err != nil {
			return nil, err
		}
		members = append(members, bundleMember{name: name, version: version, path: debPath, sha256: sum})
	}
	missing := []string{}
	for _, name := range runtimePackages {
		if _, ok := found[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, b.fail(ctx, "collect runtime packages", errMissingPackage, slog.String("missing", strings.Join(missing, " ")))
	}
	slices.SortFunc(members, func(x, y bundleMember) int { return strings.Compare(x.name, y.name) })
	return members, nil
}

// manifest renders the bundle manifest: one line per package.
func manifest(arch string, members []bundleMember) string {
	var out strings.Builder
	out.WriteString("# package version architecture sha256 file\n")
	for _, m := range members {
		fmt.Fprintf(&out, "%s %s %s %s debs/%s\n", m.name, m.version, arch, m.sha256, filepath.Base(m.path))
	}
	return out.String()
}

// writeBundle writes the manifest and the packages as a gzip tar with fixed
// ownership and timestamps, so the same packages give the same bytes.
func (b *builder) writeBundle(ctx context.Context, out io.Writer, manifestText string, members []bundleMember) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	if err := b.writeMember(ctx, tw, manifestName, []byte(manifestText)); err != nil {
		return err
	}
	for _, m := range members {
		content, err := os.ReadFile(m.path)
		if err != nil {
			return b.fail(ctx, "read package", err, slog.String("path", m.path))
		}
		if err := b.writeMember(ctx, tw, "debs/"+filepath.Base(m.path), content); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return b.fail(ctx, "close tar", err)
	}
	if err := gz.Close(); err != nil {
		return b.fail(ctx, "close gzip", err)
	}
	return nil
}

func (b *builder) writeMember(ctx context.Context, tw *tar.Writer, name string, content []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		return b.fail(ctx, "write member header", err, slog.String("member", name))
	}
	if _, err := tw.Write(content); err != nil {
		return b.fail(ctx, "write member", err, slog.String("member", name))
	}
	return nil
}

func (b *builder) fileSHA256(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", b.fail(ctx, "open file", err, slog.String("path", path))
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", b.fail(ctx, "hash file", err, slog.String("path", path))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
