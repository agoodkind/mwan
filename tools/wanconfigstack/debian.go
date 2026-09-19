package main

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"pault.ag/go/debian/control"
	"pault.ag/go/debian/deb"
)

// dpkgInfoDir holds one .list file per installed package, naming every path
// the package owns. Reading them is what dpkg -S does; no library covers it.
const dpkgInfoDir = "/var/lib/dpkg/info"

var errNoBuildDepends = errors.New("control file names no Build-Depends")

// buildDepends returns the package names a debian/control file's
// Build-Depends names, without version constraints or build profiles. The
// container runs the Debian release the templates target, so the plain
// names resolve to versions the templates accept.
func (b *builder) buildDepends(ctx context.Context, controlPath string) ([]string, error) {
	file, err := os.Open(filepath.Clean(controlPath))
	if err != nil {
		return nil, b.fail(ctx, "open control file", err, slog.String("path", controlPath))
	}
	defer func() { _ = file.Close() }()
	parsed, err := control.ParseControl(bufio.NewReader(file), controlPath)
	if err != nil {
		return nil, b.fail(ctx, "parse control file", err, slog.String("path", controlPath))
	}
	names := []string{}
	for _, relation := range parsed.Source.BuildDepends.Relations {
		for _, possibility := range relation.Possibilities {
			names = append(names, possibility.Name)
		}
	}
	if len(names) == 0 {
		return nil, b.fail(ctx, "read build dependencies", errNoBuildDepends, slog.String("path", controlPath))
	}
	return names, nil
}

// debIdentity reads a built package's name and version from its control
// section.
func (b *builder) debIdentity(ctx context.Context, path string) (string, string, error) {
	pkg, closer, err := deb.LoadFile(path)
	if err != nil {
		return "", "", b.fail(ctx, "read package", err, slog.String("path", path))
	}
	defer func() { _ = closer() }()
	return pkg.Control.Package, pkg.Control.Version.String(), nil
}

// fileOwners maps every path an installed package owns to the package
// name, read from the dpkg database.
func (b *builder) fileOwners(ctx context.Context) (map[string]string, error) {
	lists, err := filepath.Glob(filepath.Join(dpkgInfoDir, "*.list"))
	if err != nil {
		return nil, b.fail(ctx, "list dpkg database", err)
	}
	owners := map[string]string{}
	for _, list := range lists {
		pkg, _, _ := strings.Cut(strings.TrimSuffix(filepath.Base(list), ".list"), ":")
		content, err := os.ReadFile(list)
		if err != nil {
			return nil, b.fail(ctx, "read dpkg file list", err, slog.String("path", list))
		}
		for line := range strings.SplitSeq(string(content), "\n") {
			if line != "" {
				owners[line] = pkg
			}
		}
	}
	b.log.InfoContext(ctx, "wanconfigstack: dpkg database read", "packages", len(lists), "paths", len(owners))
	return owners, nil
}
