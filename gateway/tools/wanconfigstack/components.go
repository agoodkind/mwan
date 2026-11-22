package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

var (
	errNoPackages       = errors.New("no .deb files built")
	errNoProjectVersion = errors.New("no project(... VERSION ...) line")
)

func (b *builder) aptInstall(ctx context.Context, packages ...string) error {
	args := append([]string{"install", "-y", "--no-install-recommends"}, packages...)
	return b.run(ctx, cmd(programAptGet, args...).with(aptEnv...))
}

// Each Docker stage starts a new process but inherits installed packages.
// Read their exact versions before computing subsequent package dependencies.
func (b *builder) restoreVersions(ctx context.Context) error {
	paths, err := filepath.Glob(filepath.Join(buildRoot, "pkgs", "*", "*.deb"))
	if err != nil {
		return b.fail(ctx, "list previous packages", err)
	}

	return b.recordVersions(ctx, paths)
}

func (b *builder) recordVersions(ctx context.Context, paths []string) error {
	for _, path := range paths {
		name, version, err := b.debIdentity(ctx, path)
		if err != nil {
			return err
		}

		b.versions[name] = version
	}

	return nil
}

// apkgBuild builds a component through its own distro/pkg/deb template. The
// template's Build-Depends install first. apkg then downloads the release
// archive for the exact version. `apkg build --archive` builds that archive
// and renders the template from the clone, which is checked out at the
// pinned commit of the same tag. The clone's install files are ported to the
// multiarch glob before the build. The templates run the upstream test
// suites as part of the package build, so a package only exists when its
// tests passed in the container that built it.
func (b *builder) apkgBuild(ctx context.Context, c component) error {
	src := b.srcDir(c)
	templateDir := filepath.Join(src, "distro", "pkg", "deb")
	if err := b.portInstallFiles(ctx, templateDir); err != nil {
		return err
	}

	depends, err := b.buildDepends(ctx, filepath.Join(templateDir, "control"))
	if err != nil {
		return err
	}

	if err := b.aptInstall(ctx, depends...); err != nil {
		return err
	}

	archive, err := b.upstreamArchive(ctx, c)
	if err != nil {
		return err
	}

	build := cmd(
		programApkg,
		"build",
		"--archive",
		archive,
		"--no-cache",
		"--result-dir",
		b.pkgDir(c),
	).
		in(
			src,
		).with(
		aptEnv...,
	)
	if err := b.run(ctx, build); err != nil {
		return err
	}

	return b.installBuilt(ctx, b.pkgDir(c))
}

// nfpmBuild stages a cmake install under the stack prefix and wraps it as a
// package named mwan-wanconfig-NAME. CMAKE_PREFIX_PATH points cmake's
// pkg-config lookup at the /opt tree so each component finds the packaged
// ones before it.
func (b *builder) nfpmBuild(ctx context.Context, c component) error {
	src := b.srcDir(c)
	buildDir := filepath.Join(src, "build")
	stage := b.stageDir(c)
	b.log.InfoContext(ctx, "wanconfigstack: cmake stage", "component", c.name, "stage", stage)
	if err := os.RemoveAll(stage); err != nil {
		return b.fail(ctx, "clear stage", err, slog.String("stage", stage))
	}

	configure := append(
		[]string{
			"-S",
			src,
			"-B",
			buildDir,
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=" + stackPrefix,
			"-DCMAKE_PREFIX_PATH=" + stackPrefix,
			"-DBUILD_TESTING=OFF",
		},
		c.cmakeFlags...,
	)
	if err := b.runAll(
		ctx,
		cmd(programCmake, configure...),
		cmd(programCmake, "--build", buildDir, "--parallel", strconv.Itoa(b.jobs)),
		cmd(programCmake, "--install", buildDir).with("DESTDIR="+stage),
	); err != nil {
		return err
	}

	if c.loaderConf {
		if err := b.writeLoaderConf(ctx, stage); err != nil {
			return err
		}
	}

	version, err := b.packageVersion(ctx, c)
	if err != nil {
		return err
	}

	if err := b.nfpmPackage(ctx, c, version); err != nil {
		return err
	}

	return b.installBuilt(ctx, b.pkgDir(c))
}

// installBuilt installs every package in dir with apt, so Depends resolve
// from the archive the way the gateway resolves them, and records each
// package's version for the Depends of the packages that follow.
func (b *builder) installBuilt(ctx context.Context, dir string) error {
	debs, err := filepath.Glob(filepath.Join(dir, "*.deb"))
	if err != nil {
		return b.fail(ctx, "list packages", err, slog.String("dir", dir))
	}

	if len(debs) == 0 {
		return b.fail(ctx, "list packages", errNoPackages, slog.String("dir", dir))
	}

	if err := b.aptInstall(ctx, debs...); err != nil {
		return err
	}

	if err := b.recordVersions(ctx, debs); err != nil {
		return err
	}

	b.log.InfoContext(ctx, "wanconfigstack: packages installed", "dir", dir, "count", len(debs))

	return nil
}

// packageVersion is the pin without its v, or for a commit pin the cmake
// project version with the commit appended.
func (b *builder) packageVersion(ctx context.Context, c component) (string, error) {
	if !c.versionFromCMake {
		return b.pinVersion(c), nil
	}

	cmakeLists := filepath.Join(b.srcDir(c), "CMakeLists.txt")
	content, err := os.ReadFile(cmakeLists)
	if err != nil {
		return "", b.fail(ctx, "read cmake project", err, slog.String("path", cmakeLists))
	}

	cmakeVersion, ok := projectVersion(string(content))
	if !ok {
		return "", b.fail(
			ctx,
			"read cmake project version",
			errNoProjectVersion,
			slog.String("path", cmakeLists),
		)
	}

	return cmakeVersion + "+git" + b.pin(c), nil
}

// projectVersion reads the VERSION of a cmake project() line.
func projectVersion(cmakeLists string) (string, bool) {
	for line := range strings.SplitSeq(cmakeLists, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "project(") {
			continue
		}

		_, after, ok := strings.Cut(trimmed, "VERSION ")
		if !ok {
			continue
		}

		return strings.TrimRight(strings.Fields(after)[0], ")"), true
	}

	return "", false
}
