// Package schema embeds the gateway's YANG modules and lists the order and
// features they install with. It imports no cgo package, which lets the
// OpenTofu provider read the same list the binary installs.
package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
)

// files embeds the gateway's data model. A gateway serves the model its own
// release was built from. The README in this directory records the source of
// each file.
//
//go:embed *.yang
var files embed.FS

const (
	// InstallDir is where the wanconfig stack deploy installs the model files.
	// The deploy validates the rendered network file against the same files
	// before it writes the file.
	InstallDir = "/usr/local/share/wanconfig/yang"
	// FileMode is the mode of each written module. The deploy installs these
	// world readable, because sysrepo and rousette read them as their own
	// users.
	FileMode fs.FileMode = 0o644
	// DirMode is the mode of the directory a write creates.
	DirMode fs.FileMode = 0o755
	// SteeringFile is the steering revision installed by this binary.
	SteeringFile = "goodkind-mwan-steering@2026-10-01.yang"
)

// Module is one module of the gateway's model, named by its file and carrying
// the features that must be enabled when it is installed.
type Module struct {
	// File is the module's file name inside the embedded schema directory,
	// including the revision date the YANG convention puts there.
	File string
	// Features are the feature names to enable at install time. A module
	// without feature-gated leaves has none.
	Features []string
	// Update lets an install replace this module when the repository has it
	// at another revision.
	Update bool
}

// Modules lists the modules to install, in the order they install.
// Imports resolve from the directory. The order only has to put a module
// after anything it augments.
//
// ietf-nat guards every enum value behind its nat-type features. A module
// installed with no features enabled leaves those leaves with no valid value
// and libyang rejects it. The four named here are the translation types the
// steering model uses.
//
// Update is set on the five modules the deploy installed and updated with
// sysrepoctl. The deploy never updated the two base type modules or the
// interface-type registry: libyang and sysrepo load their own revisions of
// the base types, and the deploy installed the registry from rousette's model
// directory without an update step.
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

// Read returns the bytes of one embedded module file.
func Read(file string) ([]byte, error) {
	content, err := files.ReadFile(file)
	if err != nil {
		slog.Warn("schema: read the embedded module failed", "name", file, "err", err)
		return nil, fmt.Errorf("read the embedded module %s: %w", file, err)
	}
	return content, nil
}
