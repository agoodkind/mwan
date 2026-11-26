// Package schema provides the YANG modules for installation without linking to libyang.
package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
)

//go:embed *.yang
var files embed.FS

const (
	// InstallDir contains the model files used to validate gateway configuration.
	InstallDir = "/usr/local/share/wanconfig/yang"
	// FileMode permits sysrepo and rousette users to read the installed modules.
	FileMode fs.FileMode = 0o644
	// DirMode permits directory traversal by module readers.
	DirMode fs.FileMode = 0o755
	// SteeringFile omits the revision date because the module declares its
	// revision.
	SteeringFile = "goodkind-mwan-steering.yang"
)

// Module defines an embedded YANG file and its installation options.
type Module struct {
	File string
	// Features selects optional YANG features during installation.
	Features []string
	// Update permits replacement of a different installed revision.
	Update bool
}

// Modules orders extensions after the modules they augment.
// The NAT module requires enabled nat-type features for its enum values.
// Modules disables revision updates for the base type modules ietf-yang-types
// and ietf-inet-types and the IANA interface-type registry iana-if-type.
func Modules() []Module {
	return []Module{
		{File: "ietf-yang-types@2025-12-22.yang", Features: nil, Update: false},
		{File: "ietf-inet-types@2025-12-22.yang", Features: nil, Update: false},
		{File: "iana-if-type@2014-05-08.yang", Features: nil, Update: false},
		{File: "ietf-interfaces@2018-02-20.yang", Features: nil, Update: true},
		{File: "ietf-ip@2018-02-22.yang", Features: nil, Update: true},
		{File: "ietf-routing@2018-03-13.yang", Features: nil, Update: true},
		{
			File:     "ietf-nat@2019-01-10.yang",
			Features: []string{"basic-nat44", "napt44", "dst-nat", "nptv6"},
			Update:   true,
		},
		{File: SteeringFile, Features: nil, Update: true},
	}
}

// Read returns an error when an embedded module file is absent.
func Read(file string) ([]byte, error) {
	content, err := files.ReadFile(file)
	if err != nil {
		slog.Warn("schema: read the embedded module failed", "name", file, "err", err)
		return nil, fmt.Errorf("read the embedded module %s: %w", file, err)
	}

	return content, nil
}
