package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goreleaser/nfpm/v2"
	"github.com/goreleaser/nfpm/v2/deb"
	"github.com/goreleaser/nfpm/v2/files"
)

func testBuilder(t *testing.T) *builder {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &builder{log: log, pins: map[string]string{}, outDir: t.TempDir(), arch: "amd64", jobs: 1, versions: map[string]string{}}
}

// TestBuildDependsReadsTheUpstreamSysrepoTemplate pins that the upstream
// sysrepo v3.7.11 control file yields every Build-Depends package name,
// without version constraints, in template order.
func TestBuildDependsReadsTheUpstreamSysrepoTemplate(t *testing.T) {
	t.Parallel()
	names, err := testBuilder(t).buildDepends(context.Background(), filepath.Join("testdata", "sysrepo-control"))
	if err != nil {
		t.Fatalf("buildDepends: %v", err)
	}
	want := []string{"cmake", "debhelper", "libyang-dev", "libsystemd-dev", "pkg-config", "libcmocka-dev", "valgrind"}
	if !slices.Equal(names, want) {
		t.Fatalf("buildDepends = %v, want %v", names, want)
	}
}

// TestProjectVersionReadsNghttp2Asio pins the version derivation for the one
// component pinned to a commit.
func TestProjectVersionReadsNghttp2Asio(t *testing.T) {
	t.Parallel()
	version, ok := projectVersion("cmake_minimum_required(VERSION 3.0)\nproject(nghttp2-asio VERSION 0.0.90)\n")
	if !ok || version != "0.0.90" {
		t.Fatalf("projectVersion = %q, %v; want 0.0.90, true", version, ok)
	}
	if _, ok := projectVersion("project(rousette LANGUAGES CXX)\n"); ok {
		t.Fatal("projectVersion accepted a project() line without VERSION")
	}
}

// TestOwnerOfFollowsMergedUsrAliases pins that a library found under /lib
// resolves to the package that registered it under /usr/lib, which is how
// the dpkg database records files on a merged-/usr system.
func TestOwnerOfFollowsMergedUsrAliases(t *testing.T) {
	t.Parallel()
	owners := map[string]string{"/usr/lib/x86_64-linux-gnu/libyang.so.3.9.14": "libyang3"}
	pkg, ok := ownerOf("/lib/x86_64-linux-gnu/libyang.so.3.9.14", owners)
	if !ok || pkg != "libyang3" {
		t.Fatalf("ownerOf = %q, %v; want libyang3, true", pkg, ok)
	}
	if _, ok := ownerOf("/lib/x86_64-linux-gnu/libnone.so.1", owners); ok {
		t.Fatal("ownerOf accepted a path no package owns")
	}
}

// TestStageContentsKeepsSymlinksAndMarksLoaderConf pins that a staged cmake
// install becomes package contents with directories, symlinks preserved as
// symlinks, and the loader conf typed as configuration and naming the
// staged library directory.
func TestStageContentsKeepsSymlinksAndMarksLoaderConf(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	libDir := filepath.Join(stage, stackPrefix, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libyang-cpp.so.4"), []byte("elf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libyang-cpp.so.4", filepath.Join(libDir, "libyang-cpp.so")); err != nil {
		t.Fatal(err)
	}
	if err := testBuilder(t).writeLoaderConf(context.Background(), stage); err != nil {
		t.Fatalf("writeLoaderConf: %v", err)
	}
	contents, err := testBuilder(t).stageContents(context.Background(), stage)
	if err != nil {
		t.Fatalf("stageContents: %v", err)
	}
	byDestination := map[string]*files.Content{}
	for _, content := range contents {
		byDestination[content.Destination] = content
	}
	link := byDestination[stackPrefix+"/lib/libyang-cpp.so"]
	if link == nil || link.Type != files.TypeSymlink || link.Source != "libyang-cpp.so.4" {
		t.Fatalf("symlink content = %+v", link)
	}
	if dir := byDestination[stackPrefix+"/lib"]; dir == nil || dir.Type != files.TypeDir {
		t.Fatalf("dir content = %+v", dir)
	}
	conf := byDestination[loaderConfPath]
	if conf == nil || conf.Type != files.TypeConfig {
		t.Fatalf("loader conf content = %+v", conf)
	}
	text, err := os.ReadFile(conf.Source)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(text)) != stackPrefix+"/lib" {
		t.Fatalf("loader conf = %q, want %s/lib", text, stackPrefix)
	}
}

// TestWriteBundleRoundTrips pins the bundle layout a consumer unpacks: the
// manifest first, then each package under debs/, with the manifest naming
// the package, version, architecture, sha256, and member path.
func TestWriteBundleRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	debPath := filepath.Join(dir, "libyang3_3.13.6-1_amd64.deb")
	if err := os.WriteFile(debPath, []byte("deb bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := testBuilder(t)
	sum, err := b.fileSHA256(context.Background(), debPath)
	if err != nil {
		t.Fatal(err)
	}
	members := []bundleMember{{name: "libyang3", version: "3.13.6-1", path: debPath, sha256: sum}}
	var archive bytes.Buffer
	if err := b.writeBundle(context.Background(), &archive, manifest("amd64", members), members); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
	gz, err := gzip.NewReader(&archive)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	got := map[string]string{}
	order := []string{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		got[header.Name] = string(content)
		order = append(order, header.Name)
	}
	if !slices.Equal(order, []string{manifestName, "debs/libyang3_3.13.6-1_amd64.deb"}) {
		t.Fatalf("members = %v", order)
	}
	if got["debs/libyang3_3.13.6-1_amd64.deb"] != "deb bytes" {
		t.Fatalf("package member = %q", got["debs/libyang3_3.13.6-1_amd64.deb"])
	}
	wantLine := "libyang3 3.13.6-1 amd64 " + sum + " debs/libyang3_3.13.6-1_amd64.deb"
	if !strings.Contains(got[manifestName], wantLine) {
		t.Fatalf("manifest = %q, want line %q", got[manifestName], wantLine)
	}
}

// writeTestDeb builds a minimal real Debian package with nfpm, the same
// packager the build uses, so debIdentity reads it the production way.
func writeTestDeb(t *testing.T, dir, name, version string) string {
	t.Helper()
	info := nfpm.WithDefaults(&nfpm.Info{
		Name:        name,
		Arch:        "amd64",
		Platform:    "linux",
		Version:     version,
		Maintainer:  "test <test@example.com>",
		Description: "test package",
	})
	if err := nfpm.PrepareForPackager(info, "deb"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"_"+version+"_amd64.deb")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := deb.Default.Package(info, out); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCollectRuntimeMembersRefusesDuplicatesAndNamesMissing pins the
// collection contract behind the bundle: a second package carrying an
// already-collected runtime name fails the build, and a missing runtime
// package is reported by name rather than by count.
func TestCollectRuntimeMembersRefusesDuplicatesAndNamesMissing(t *testing.T) {
	t.Parallel()
	b := testBuilder(t)
	first := writeTestDeb(t, t.TempDir(), "libyang3", "3.13.6")
	duplicate := writeTestDeb(t, t.TempDir(), "libyang3", "3.13.7")
	if _, err := b.collectRuntimeMembers(context.Background(), []string{first, duplicate}); !errors.Is(err, errDuplicatePackage) {
		t.Fatalf("duplicate error = %v, want %v", err, errDuplicatePackage)
	}
	if _, err := b.collectRuntimeMembers(context.Background(), []string{first}); !errors.Is(err, errMissingPackage) {
		t.Fatalf("missing error = %v, want %v", err, errMissingPackage)
	}
}

// TestElfNeededSkipsFilesTooShortForAHeader pins that a staged tree holding
// text and data files shorter than an ELF header, as a cmake install of
// headers and pkg-config files can, is walked without error.
func TestElfNeededSkipsFilesTooShortForAHeader(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "version"), []byte("3.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "libyang.pc"), []byte("Name: libyang\nVersion: 3.13.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	needed, err := testBuilder(t).elfNeeded(context.Background(), stage)
	if err != nil {
		t.Fatalf("elfNeeded: %v", err)
	}
	if len(needed) != 0 {
		t.Fatalf("elfNeeded = %v, want none", needed)
	}
}

// TestUnpackBundleRejectsForeignMembers pins the unpack contract the proof
// relies on: a member that is not a .deb, or whose name climbs out of the
// bundle, stops the unpack before anything is written.
func TestUnpackBundleRejectsForeignMembers(t *testing.T) {
	t.Parallel()
	for _, member := range []string{"../../etc/passwd.deb", "debs/../escape.deb", "notes.txt"} {
		t.Run(member, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bundlePath := filepath.Join(dir, "bundle.tar.gz")
			writeTestBundle(t, bundlePath, member)
			root := filepath.Join(dir, "unpack")
			err := testBuilder(t).unpackBundle(context.Background(), bundlePath, root)
			if !errors.Is(err, errBundleMember) {
				t.Fatalf("unpackBundle error = %v, want %v", err, errBundleMember)
			}
			if _, statErr := os.Stat(root); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("unpack root exists after a rejected member: stat = %v", statErr)
			}
		})
	}
}

// writeTestBundle writes a gzip tar holding the manifest and one member.
func writeTestBundle(t *testing.T, path string, member string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	b := testBuilder(t)
	if err := b.writeMember(context.Background(), tw, manifestName, []byte("# manifest\n")); err != nil {
		t.Fatal(err)
	}
	if err := b.writeMember(context.Background(), tw, member, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestParseFlagsRequiresEveryPin pins that a missing pin or output directory
// refuses to run rather than building with a blank version.
func TestParseFlagsRequiresEveryPin(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := parseFlags(log, []string{"-out", "/tmp/x"}); err == nil {
		t.Fatal("parseFlags accepted missing pins")
	}
	args := []string{"-out", "/tmp/x"}
	for _, c := range components {
		args = append(args, "-"+c.pinVar, "v1", "-"+c.commitVar, strings.Repeat("a", 40))
	}
	opts, err := parseFlags(log, args)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.outDir != "/tmp/x" || len(opts.pins) != 2*len(components) {
		t.Fatalf("parseFlags = %+v", opts)
	}
	short := []string{"-out", "/tmp/x"}
	for _, c := range components {
		short = append(short, "-"+c.pinVar, "v1", "-"+c.commitVar, "e877868abe")
	}
	if _, err := parseFlags(log, short); err == nil {
		t.Fatal("parseFlags accepted a truncated commit pin")
	}
	verify, err := parseFlags(log, []string{"-verify", "/out/wanconfig-stack_linux_amd64.tar.gz"})
	if err != nil {
		t.Fatalf("parseFlags verify: %v", err)
	}
	if verify.verifyBundle != "/out/wanconfig-stack_linux_amd64.tar.gz" {
		t.Fatalf("parseFlags verify = %+v", verify)
	}
}
