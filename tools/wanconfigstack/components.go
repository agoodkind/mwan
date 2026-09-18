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

// apkgVersion pins the upstream packaging tool, which is not in Debian.
const apkgVersion = "1.0.0"

// buildPackages are the apt packages the container needs: the Debian
// packaging toolchain, cmake, and the -dev packages the nfpm components link
// against. The apkg templates add their own Build-Depends on top. systemd-dev
// carries the pkg-config file that names the unit directory on trixie; the
// sysrepo template's Build-Depends predate that split and its sysrepo-tools
// package expects the sysrepo-plugind unit that cmake only installs when the
// directory resolves. git is not for cloning (the tool clones through a
// library) but for the C++ components' cmake, which requires the git
// program to stamp their version.
var buildPackages = []string{
	"ca-certificates", "build-essential", "cmake", "pkg-config", "git",
	"debhelper", "dpkg-dev", "fakeroot", "python3", "python3-venv", "python3-pip", "systemd-dev",
	"libpcre2-dev", "libpam0g-dev", "libaudit-dev", "libhowardhinnant-date-dev",
	"libspdlog-dev", "libdocopt-dev", "libboost-system-dev", "libboost-thread-dev",
	"libssl-dev", "libnghttp2-dev", "libsystemd-dev",
}

var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

var (
	errNoPackages       = errors.New("no .deb files built")
	errNoProjectVersion = errors.New("no project(... VERSION ...) line")
)

func (b *builder) aptInstall(ctx context.Context, packages ...string) error {
	args := append([]string{"install", "-y", "--no-install-recommends"}, packages...)
	return b.run(ctx, cmd(programAptGet, args...).with(aptEnv...))
}

func (b *builder) installBuildTools(ctx context.Context) error {
	for _, dir := range []string{"src", "stage", "pkgs"} {
		if err := os.MkdirAll(filepath.Join(buildRoot, dir), 0o755); err != nil {
			return b.fail(ctx, "create build dir", err, slog.String("dir", dir))
		}
	}
	if err := b.run(ctx, cmd(programAptGet, "update").with(aptEnv...)); err != nil {
		return err
	}
	if err := b.aptInstall(ctx, buildPackages...); err != nil {
		return err
	}
	return b.runAll(ctx,
		cmd(programPython3, "-m", "venv", filepath.Dir(filepath.Dir(pipPath))),
		cmd(programPip, "install", "--quiet", "apkg=="+apkgVersion),
	)
}

// apkgBuild builds a component through its own distro/pkg/deb template. The
// template's Build-Depends install first; then apkg's upstream mode
// downloads the release archive for the exact version, so archive and
// template come from the same tag. The templates run the upstream test
// suites as part of the package build, so a package only exists when its
// tests passed in the container that built it.
func (b *builder) apkgBuild(ctx context.Context, c component) error {
	src := b.srcDir(c)
	depends, err := b.buildDepends(ctx, filepath.Join(src, "distro", "pkg", "deb", "control"))
	if err != nil {
		return err
	}
	if err := b.aptInstall(ctx, depends...); err != nil {
		return err
	}
	build := cmd(programApkg, "build", "--upstream", "--version", b.pinVersion(c), "--no-cache", "--result-dir", b.pkgDir(c)).
		in(src).with(aptEnv...)
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
	configure := append([]string{
		"-S", src, "-B", buildDir,
		"-DCMAKE_BUILD_TYPE=Release",
		"-DCMAKE_INSTALL_PREFIX=" + stackPrefix,
		"-DCMAKE_PREFIX_PATH=" + stackPrefix,
		"-DBUILD_TESTING=OFF",
	}, c.cmakeFlags...)
	if err := b.runAll(ctx,
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
	for _, debPath := range debs {
		name, version, err := b.debIdentity(ctx, debPath)
		if err != nil {
			return err
		}
		b.versions[name] = version
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
		return "", b.fail(ctx, "read cmake project version", errNoProjectVersion, slog.String("path", cmakeLists))
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
