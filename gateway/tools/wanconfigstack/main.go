// Command wanconfigstack builds pinned Debian packages and verifies their
// runtime bundle. Docker supplies the fixed build tools.
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

	"goodkind.io/mwan/gateway/stackspec"
)

type packaging int

const (
	packagingApkg packaging = iota
	packagingNfpm
)

type component struct {
	name        string
	url         string
	pinVar      string
	commitVar   string
	how         packaging
	cmakeFlags  []string
	description string
	loaderConf  bool
	// Commit pins need the upstream CMake version for Debian package identity.
	versionFromCMake bool
}

var components = []component{
	{
		name:      "libyang",
		url:       "https://github.com/CESNET/libyang.git",
		pinVar:    "wanconfig_libyang_version",
		commitVar: "wanconfig_libyang_commit",
		how:       packagingApkg,
	},
	{
		name:      "sysrepo",
		url:       "https://github.com/sysrepo/sysrepo.git",
		pinVar:    "wanconfig_sysrepo_version",
		commitVar: "wanconfig_sysrepo_commit",
		how:       packagingApkg,
	},
	{
		name:        "libyang-cpp",
		url:         "https://github.com/CESNET/libyang-cpp.git",
		pinVar:      "wanconfig_libyang_cpp_version",
		commitVar:   "wanconfig_libyang_cpp_commit",
		how:         packagingNfpm,
		cmakeFlags:  []string{"-DWITH_DOCS=OFF"},
		description: "C++ bindings for libyang (wanconfig management stack)",
		loaderConf:  true,
	},
	{
		name:        "sysrepo-cpp",
		url:         "https://github.com/sysrepo/sysrepo-cpp.git",
		pinVar:      "wanconfig_sysrepo_cpp_version",
		commitVar:   "wanconfig_sysrepo_cpp_commit",
		how:         packagingNfpm,
		cmakeFlags:  []string{"-DWITH_DOCS=OFF", "-DWITH_EXAMPLES=OFF"},
		description: "C++ bindings for sysrepo (wanconfig management stack)",
	},
	{
		name:             "nghttp2-asio",
		url:              "https://github.com/nghttp2/nghttp2-asio.git",
		pinVar:           "wanconfig_nghttp2_asio_version",
		commitVar:        "wanconfig_nghttp2_asio_commit",
		how:              packagingNfpm,
		description:      "HTTP/2 C++ library over Boost ASIO (wanconfig management stack)",
		versionFromCMake: true,
	},
	{
		name:        "rousette",
		url:         "https://github.com/CESNET/rousette.git",
		pinVar:      "wanconfig_rousette_version",
		commitVar:   "wanconfig_rousette_commit",
		how:         packagingNfpm,
		cmakeFlags:  []string{"-DWITH_DOCS=OFF"},
		description: "RESTCONF server over sysrepo (wanconfig management stack)",
	},
}

var (
	runtimePackages = stackspec.Names()
	debianArchs     = map[string]string{"amd64": "amd64", "arm64": "arm64"}
)

var (
	errUnsupportedArch  = errors.New("no Debian architecture mapping")
	errExclusiveModes   = errors.New("-verify, -component, and -bundle-only are mutually exclusive")
	errOutputRequired   = errors.New("-out is required when assembling a bundle")
	errUnknownComponent = errors.New("unknown component")
	errRequiredFlag     = errors.New("required flag is missing")
	errCommitFormat     = errors.New("commit must be a full lowercase 40-hex hash")
)

// Debian reserves /usr/local for administrators. The private prefix avoids
// conflicts with distribution libraries under /usr.
const (
	stackPrefix    = stackspec.Prefix
	loaderConfPath = "/etc/ld.so.conf.d/mwan-wanconfig.conf"
	buildRoot      = "/build"
)

type builder struct {
	log    *slog.Logger
	pins   map[string]string
	outDir string
	arch   string
	jobs   int
	// Later packages require exact versions of their packaged predecessors.
	versions map[string]string
}

func (b *builder) srcDir(c component) string { return filepath.Join(buildRoot, "src", c.name) }

func (b *builder) stageDir(
	c component,
) string {
	return filepath.Join(buildRoot, "stage", c.name)
}
func (b *builder) pkgDir(c component) string     { return filepath.Join(buildRoot, "pkgs", c.name) }
func (b *builder) pin(c component) string        { return b.pins[c.pinVar] }
func (b *builder) commit(c component) string     { return b.pins[c.commitVar] }
func (b *builder) pinVersion(c component) string { return strings.TrimPrefix(b.pin(c), "v") }

type options struct {
	pins         map[string]string
	outDir       string
	verifyBundle string
	component    string
	bundleOnly   bool
}

func parseFlags(log *slog.Logger, args []string) (options, error) {
	flags := flag.NewFlagSet("wanconfigstack", flag.ContinueOnError)
	pins := map[string]*string{}

	for _, c := range components {
		pins[c.pinVar] = flags.String(c.pinVar, "", c.name+" pin")
		pins[c.commitVar] = flags.String(c.commitVar, "", c.name+" pinned commit hash")
	}

	outDir := flags.String(
		"out",
		"",
		"directory that receives the bundle after building or verifying",
	)
	verifyBundle := flags.String(
		"verify",
		"",
		"bundle to install and prove on this system instead of building",
	)
	selected := flags.String(
		"component",
		"",
		"build one component against previously installed packages",
	)
	bundleOnly := flags.Bool(
		"bundle-only",
		false,
		"assemble previously built packages without compiling",
	)
	if err := flags.Parse(args); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			log.Error("wanconfigstack: bad arguments", "err", err)
		}

		return options{}, fmt.Errorf("parse flags: %w", err)
	}

	opts := options{
		pins:         map[string]string{},
		outDir:       *outDir,
		verifyBundle: *verifyBundle,
		component:    *selected,
		bundleOnly:   *bundleOnly,
	}
	if (opts.verifyBundle != "" && (opts.component != "" || opts.bundleOnly)) ||
		(opts.component != "" && opts.bundleOnly) {
		return options{}, errExclusiveModes
	}

	if opts.verifyBundle != "" {
		return opts, nil
	}

	if opts.component == "" && opts.outDir == "" {
		return options{}, errOutputRequired
	}

	if opts.bundleOnly {
		return opts, nil
	}

	required := components
	if opts.component != "" {
		required = nil

		for _, c := range components {
			if c.name == opts.component {
				required = append(required, c)
			}
		}

		if len(required) == 0 {
			return options{}, fmt.Errorf("%w: %q", errUnknownComponent, opts.component)
		}
	}

	for _, entry := range required {
		for _, name := range []string{entry.pinVar, entry.commitVar} {
			value := *pins[name]
			if value == "" {
				return options{}, fmt.Errorf("%w: -%s", errRequiredFlag, name)
			}

			opts.pins[name] = value
		}

		if !commitPattern.MatchString(opts.pins[entry.commitVar]) {
			return options{}, fmt.Errorf(
				"%w: %s got %q",
				errCommitFormat,
				entry.commitVar,
				opts.pins[entry.commitVar],
			)
		}
	}

	return opts, nil
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (b *builder) build(ctx context.Context) error {
	for _, c := range components {
		if err := b.buildComponent(ctx, c); err != nil {
			return err
		}
	}

	return b.assemble(ctx)
}

func (b *builder) assemble(ctx context.Context) error {
	if err := os.MkdirAll(b.outDir, 0o755); err != nil {
		return b.fail(ctx, "create output directory", err)
	}

	if err := b.downloadDistribution(ctx); err != nil {
		return err
	}

	return b.bundle(ctx)
}

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

	return b.fail(
		ctx,
		"unknown packaging",
		fmt.Errorf("packaging %d", c.how),
		slog.String("component", c.name),
	)
}

func (b *builder) execute(ctx context.Context, opts options) error {
	if opts.verifyBundle != "" {
		if err := b.verify(ctx, opts.verifyBundle); err != nil {
			return err
		}

		if opts.outDir != "" {
			return b.exportVerified(ctx, opts.verifyBundle)
		}

		return nil
	}

	if opts.bundleOnly {
		return b.assemble(ctx)
	}

	if opts.component != "" {
		if err := b.restoreVersions(ctx); err != nil {
			return err
		}

		for _, c := range components {
			if c.name == opts.component {
				return b.buildComponent(ctx, c)
			}
		}

		return b.fail(
			ctx,
			"select component",
			errUnknownComponent,
			slog.String("component", opts.component),
		)
	}

	return b.build(ctx)
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	opts, err := parseFlags(log, os.Args[1:])
	if err != nil {
		log.Error("wanconfigstack: invalid arguments", "err", err)
		os.Exit(2)
	}

	arch, ok := debianArchs[runtime.GOARCH]
	if !ok {
		log.Error(
			"wanconfigstack: unsupported architecture",
			"goarch",
			runtime.GOARCH,
			"err",
			errUnsupportedArch,
		)
		os.Exit(2)
	}

	b := &builder{
		log:      log,
		pins:     opts.pins,
		outDir:   opts.outDir,
		arch:     arch,
		jobs:     runtime.NumCPU(),
		versions: map[string]string{},
	}
	if err := b.execute(context.Background(), opts); err != nil {
		os.Exit(1)
	}
}
