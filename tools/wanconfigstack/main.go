// Command wanconfigstack builds the wanconfig management stack as Debian
// packages (MWAN-431).
//
// It runs as root inside a stock Debian trixie container that the mwan
// Makefile starts from its wanconfig-stack-bundle target. Six components,
// pinned in that Makefile and passed in as flags, become Debian
// packages that install on a gateway with apt and nothing else. The two
// components with upstream Debian packaging (libyang, sysrepo) build through
// their apkg deb templates, untouched, into /usr under the upstream package
// names; the sysrepo template compiles the upstream access policy in (group
// sysrepo, umask 007). The four without (libyang-cpp, sysrepo-cpp,
// nghttp2-asio, rousette) are cmake-installed into a DESTDIR stage under the
// /opt/mwan-wanconfig prefix and packaged by the nfpm library, with a Depends
// line read off their ELF files.
//
// Every package is installed into the container as it is built, so each
// later component compiles against the packaged form of its predecessors,
// which is exactly what the gateway sees. The runtime packages land in one
// tar.gz bundle that the mwan release publishes next to the binaries.
// Package identity is the upstream version from the pins; the release tag is
// only delivery.
//
// External programs run only where no library exists: apt for the build
// dependencies, apkg and its dpkg-buildpackage for the upstream templates,
// and cmake. Git, ELF inspection, dpkg ownership, package assembly, and the
// bundle are libraries.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// packaging names how a component becomes a Debian package.
type packaging int

const (
	// packagingApkg builds the upstream distro/pkg/deb template.
	packagingApkg packaging = iota
	// packagingNfpm stages a cmake install under stackPrefix and wraps it.
	packagingNfpm
)

// component is one member of the stack. The table below is the whole
// recipe; the build functions only interpret it.
type component struct {
	name string
	url  string
	// pinVar is the flag name the pin arrives under.
	pinVar string
	// commitVar is the flag name of the pin's immutable commit hash. The
	// clone refuses a pin that resolves elsewhere.
	commitVar string
	how       packaging
	// cmakeFlags are extra configure flags for nfpm components.
	cmakeFlags []string
	// description is the package description for nfpm components.
	description string
	// loaderConf marks the one nfpm component that ships the ld.so.conf.d
	// entry for the /opt tree; the others reach it through Depends.
	loaderConf bool
	// versionFromCMake derives the package version from the project()
	// line instead of the pin, for a component pinned to a commit.
	versionFromCMake bool
}

var components = []component{
	{
		name: "libyang", url: "https://github.com/CESNET/libyang.git",
		pinVar: "wanconfig_libyang_version", commitVar: "wanconfig_libyang_commit", how: packagingApkg,
	},
	{
		name: "sysrepo", url: "https://github.com/sysrepo/sysrepo.git",
		pinVar: "wanconfig_sysrepo_version", commitVar: "wanconfig_sysrepo_commit", how: packagingApkg,
	},
	{
		name: "libyang-cpp", url: "https://github.com/CESNET/libyang-cpp.git",
		pinVar: "wanconfig_libyang_cpp_version", commitVar: "wanconfig_libyang_cpp_commit", how: packagingNfpm,
		cmakeFlags: []string{"-DWITH_DOCS=OFF"}, description: "C++ bindings for libyang (wanconfig management stack)", loaderConf: true,
	},
	{
		name: "sysrepo-cpp", url: "https://github.com/sysrepo/sysrepo-cpp.git",
		pinVar: "wanconfig_sysrepo_cpp_version", commitVar: "wanconfig_sysrepo_cpp_commit", how: packagingNfpm,
		cmakeFlags: []string{"-DWITH_DOCS=OFF", "-DWITH_EXAMPLES=OFF"}, description: "C++ bindings for sysrepo (wanconfig management stack)",
	},
	{
		name: "nghttp2-asio", url: "https://github.com/nghttp2/nghttp2-asio.git",
		pinVar: "wanconfig_nghttp2_asio_version", commitVar: "wanconfig_nghttp2_asio_commit", how: packagingNfpm,
		description: "HTTP/2 C++ library over Boost ASIO (wanconfig management stack)", versionFromCMake: true,
	},
	{
		name: "rousette", url: "https://github.com/CESNET/rousette.git",
		pinVar: "wanconfig_rousette_version", commitVar: "wanconfig_rousette_commit", how: packagingNfpm,
		cmakeFlags: []string{"-DWITH_DOCS=OFF"}, description: "RESTCONF server over sysrepo (wanconfig management stack)",
	},
}

// runtimePackages are the packages the gateway installs. Development and
// tool packages the gateway never runs stay out of the bundle.
var runtimePackages = []string{
	"libyang3", "libsysrepo7", "sysrepo-tools",
	"mwan-wanconfig-libyang-cpp", "mwan-wanconfig-sysrepo-cpp", "mwan-wanconfig-nghttp2-asio", "mwan-wanconfig-rousette",
}

// debianArchs maps the Go architecture the tool was built for to Debian's
// name for it. The tool runs natively in the container, so they agree.
var debianArchs = map[string]string{"amd64": "amd64", "arm64": "arm64"}

var errUnsupportedArch = errors.New("no Debian architecture mapping")

// stackPrefix is the tree the nfpm packages install into. Debian policy
// reserves /usr/local for the local administrator, and /usr would collide
// with any distribution libyang package, so the stack takes its own prefix.
const stackPrefix = "/opt/mwan-wanconfig"

// loaderConfPath makes the /opt tree's shared libraries resolvable.
const loaderConfPath = "/etc/ld.so.conf.d/mwan-wanconfig.conf"

// buildRoot is the scratch tree inside the container.
const buildRoot = "/build"

// builder carries the inputs and the resolved layout through one build.
type builder struct {
	log *slog.Logger
	// pins maps pinVar to the pinned ref.
	pins map[string]string
	// outDir receives the bundle.
	outDir string
	// arch is the Debian architecture of the container (amd64, arm64).
	arch string
	jobs int
	// versions records the version of every package this build produced,
	// by package name, so later packages pin their Depends on it exactly.
	versions map[string]string
}

func (b *builder) srcDir(c component) string   { return filepath.Join(buildRoot, "src", c.name) }
func (b *builder) stageDir(c component) string { return filepath.Join(buildRoot, "stage", c.name) }
func (b *builder) pkgDir(c component) string   { return filepath.Join(buildRoot, "pkgs", c.name) }
func (b *builder) pin(c component) string      { return b.pins[c.pinVar] }
func (b *builder) commit(c component) string   { return b.pins[c.commitVar] }

// pinVersion is the pin as a Debian version: the tag without its v.
func (b *builder) pinVersion(c component) string { return strings.TrimPrefix(b.pin(c), "v") }

// options are the tool's inputs. A verify path selects the verify mode,
// which needs no pins; otherwise every pin and the output directory are
// required.
type options struct {
	pins         map[string]string
	outDir       string
	verifyBundle string
}

func parseFlags(log *slog.Logger, args []string) (options, error) {
	flags := flag.NewFlagSet("wanconfigstack", flag.ContinueOnError)
	pins := map[string]*string{}
	for _, c := range components {
		pins[c.pinVar] = flags.String(c.pinVar, "", c.name+" pin")
		pins[c.commitVar] = flags.String(c.commitVar, "", c.name+" pinned commit hash")
	}
	outDir := flags.String("out", "", "directory that receives the bundle")
	verifyBundle := flags.String("verify", "", "bundle to install and prove on this system instead of building")
	if err := flags.Parse(args); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			log.Error("wanconfigstack: bad arguments", "err", err)
		}
		return options{}, fmt.Errorf("parse flags: %w", err)
	}
	opts := options{pins: map[string]string{}, outDir: *outDir, verifyBundle: *verifyBundle}
	if opts.verifyBundle != "" {
		return opts, nil
	}
	missing := []string{}
	for name, value := range pins {
		if *value == "" {
			missing = append(missing, name)
		}
		opts.pins[name] = *value
	}
	if opts.outDir == "" {
		missing = append(missing, "out")
	}
	if len(missing) > 0 {
		err := fmt.Errorf("every pin and -out is required; missing %v", missing)
		log.Error("wanconfigstack: bad arguments", "err", err)
		return options{}, err
	}
	for _, c := range components {
		if !commitPattern.MatchString(opts.pins[c.commitVar]) {
			err := fmt.Errorf("%s must be a full lowercase 40-hex commit hash; got %q", c.commitVar, opts.pins[c.commitVar])
			log.Error("wanconfigstack: bad arguments", "err", err)
			return options{}, err
		}
	}
	return opts, nil
}

// commitPattern is the only form a pinned commit may take: a full hash, so
// the checkout comparison in the clone can never pass on a prefix.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (b *builder) build(ctx context.Context) error {
	if err := b.installBuildTools(ctx); err != nil {
		return err
	}
	for _, c := range components {
		if err := b.buildComponent(ctx, c); err != nil {
			return err
		}
	}
	return b.bundle(ctx)
}

// buildComponent takes one component from clone to installed package.
func (b *builder) buildComponent(ctx context.Context, c component) error {
	b.log.InfoContext(ctx, "wanconfigstack: component", "name", c.name, "pin", b.pin(c))
	if err := b.clonePinned(ctx, c); err != nil {
		return err
	}
	switch c.how {
	case packagingApkg:
		return b.apkgBuild(ctx, c)
	case packagingNfpm:
		return b.nfpmBuild(ctx, c)
	}
	return b.fail(ctx, "unknown packaging", fmt.Errorf("packaging %d", c.how), slog.String("component", c.name))
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	opts, err := parseFlags(log, os.Args[1:])
	if err != nil {
		os.Exit(2)
	}
	arch, ok := debianArchs[runtime.GOARCH]
	if !ok {
		log.Error("wanconfigstack: unsupported architecture", "goarch", runtime.GOARCH, "err", errUnsupportedArch)
		os.Exit(2)
	}
	b := &builder{log: log, pins: opts.pins, outDir: opts.outDir, arch: arch, jobs: runtime.NumCPU(), versions: map[string]string{}}
	ctx := context.Background()
	if opts.verifyBundle != "" {
		err = b.verify(ctx, opts.verifyBundle)
	} else {
		err = b.build(ctx)
	}
	if err != nil {
		os.Exit(1)
	}
}
