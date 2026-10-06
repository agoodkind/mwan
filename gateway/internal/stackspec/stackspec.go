// Package stackspec defines runtime package versions and archive member paths.
// The bundle builder and OpenTofu provider use these definitions.
package stackspec

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// MemberDir is the archive directory for Debian packages.
const MemberDir = "debs"

// Package identifies a runtime Debian package and its version.
type Package struct {
	// Name uses the Debian package identifier.
	Name string
	// Version uses Debian version syntax.
	Version string
	// Origin determines whether the stack tool builds or downloads the package.
	Origin Origin
}

// Origin distinguishes stack builds from distribution downloads.
type Origin string

const (
	// OriginBuilt identifies a package built by the stack tool.
	OriginBuilt Origin = "built"
	// OriginDistribution identifies a package downloaded from distribution repositories.
	OriginDistribution Origin = "distribution"
)

// Packages returns the runtime packages built by the stack tool.
// Update these versions with the corresponding build pins.
func Packages() []Package {
	return []Package{
		{Name: "libyang3", Version: "3.13.6-1", Origin: OriginBuilt},
		{Name: "libsysrepo7", Version: "3.7.11-1", Origin: OriginBuilt},
		{Name: "sysrepo-tools", Version: "3.7.11-1", Origin: OriginBuilt},
		{Name: "mwan-wanconfig-libyang-cpp", Version: "4.0.0", Origin: OriginBuilt},
		{Name: "mwan-wanconfig-sysrepo-cpp", Version: "6.0.0", Origin: OriginBuilt},
		{Name: "mwan-wanconfig-nghttp2-asio", Version: "0.0.90+gite877868abe", Origin: OriginBuilt},
		{Name: "mwan-wanconfig-rousette", Version: "2.0.0", Origin: OriginBuilt},
		{Name: "libdocopt0", Version: "0.6.3-5", Origin: OriginDistribution},
		{Name: "libfmt10", Version: "10.1.1+ds1-4", Origin: OriginDistribution},
		{Name: "libspdlog1.15", Version: "1:1.15.2+ds-2", Origin: OriginDistribution},
	}
}

// Names preserves the order returned by Packages.
func Names() []string {
	packages := Packages()
	names := make([]string, 0, len(packages))
	for _, pkg := range packages {
		names = append(names, pkg.Name)
	}
	return names
}

// FileName omits any Debian epoch and its separating colon from the version in
// <name>_<version>_<architecture>.deb.
func (p Package) FileName(arch string) string {
	_, withoutEpoch, hasEpoch := strings.Cut(p.Version, ":")
	if !hasEpoch {
		withoutEpoch = p.Version
	}
	return p.Name + "_" + withoutEpoch + "_" + arch + ".deb"
}

// ErrMismatch indicates a package version or filename mismatch.
var ErrMismatch = errors.New("a built package differs from the stackspec list")

// CheckBuilt checks listed packages against the expected version and filename.
// It does not validate packages absent from Packages.
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

// Member prefixes the package filename with the archive directory.
func (p Package) Member(arch string) string {
	return MemberDir + "/" + p.FileName(arch)
}
