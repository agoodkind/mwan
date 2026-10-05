// Package stackspec lists the runtime packages of the wanconfig stack bundle
// and the member each package has inside wanconfig-stack_linux_<arch>.tar.gz.
// The bundle tool and the OpenTofu provider read this one list, and the tool
// fails the bundle when a built package differs from it.
package stackspec

import (
	"errors"
	"fmt"
	"log/slog"
)

// MemberDir is the directory of the package members inside the bundle.
const MemberDir = "debs"

// Package is one runtime package of the stack and its exact version.
type Package struct {
	// Name is the Debian package name.
	Name string
	// Version is the Debian version of the built package.
	Version string
}

// Packages lists the packages the gateway installs. Development and tool
// packages the gateway never runs stay out of the bundle. A pin bump in the
// Makefile changes a version here in the same commit.
func Packages() []Package {
	return []Package{
		{Name: "libyang3", Version: "3.13.6-1"},
		{Name: "libsysrepo7", Version: "3.7.11-1"},
		{Name: "sysrepo-tools", Version: "3.7.11-1"},
		{Name: "mwan-wanconfig-libyang-cpp", Version: "4.0.0"},
		{Name: "mwan-wanconfig-sysrepo-cpp", Version: "6.0.0"},
		{Name: "mwan-wanconfig-nghttp2-asio", Version: "0.0.90+gite877868abe"},
		{Name: "mwan-wanconfig-rousette", Version: "2.0.0"},
	}
}

// Names lists the package names in the order of Packages.
func Names() []string {
	packages := Packages()
	names := make([]string, 0, len(packages))
	for _, pkg := range packages {
		names = append(names, pkg.Name)
	}
	return names
}

// FileName is the conventional Debian file name of the package for one
// architecture.
func (p Package) FileName(arch string) string {
	return p.Name + "_" + p.Version + "_" + arch + ".deb"
}

// ErrMismatch means a built package differs from the list.
var ErrMismatch = errors.New("a built package differs from the stackspec list")

// CheckBuilt returns ErrMismatch when a built runtime package has a version or
// file name other than the one listed for the architecture. A package that is
// not listed passes.
func CheckBuilt(name string, version string, fileName string, arch string) error {
	for _, pkg := range Packages() {
		if pkg.Name != name {
			continue
		}
		if pkg.Version != version || pkg.FileName(arch) != fileName {
			mismatch := fmt.Errorf("%w: built %s version %s, want %s", ErrMismatch, fileName, version, pkg.FileName(arch))
			slog.Error("stackspec: built package differs from the list", "package", name, "err", mismatch)
			return mismatch
		}
	}
	return nil
}

// Member is the path of the package inside the bundle for one architecture.
func (p Package) Member(arch string) string {
	return MemberDir + "/" + p.FileName(arch)
}
