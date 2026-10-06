// Package installspec embeds the files used by the installer and OpenTofu provider.
// The package does not require cgo.
package installspec

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
)

// Add new install assets to the explicit embed list.
//
//go:embed mwan-agent.service mwan-ifmgr.service mwan-ifmgr@.service mwan-trace-boot.service mwan-ifmgr-failover.conf
//go:embed rousette.service nghttpx-wanconfig.service systemd-networkd-override.conf mwan-ifmgr-wan.conf
//go:embed 99-quiet-console.conf nacm-anonymous.xml
var unitFS embed.FS

// The anonymous RESTCONF policy permits reads and denies writes and execution.
const nacmPolicyName = "nacm-anonymous.xml"

const (
	// SystemdUnitDir takes precedence over package-provided unit directories.
	SystemdUnitDir = "/etc/systemd/system"
	// SysctlDir contains the settings read by systemd-sysctl at boot.
	SysctlDir = "/etc/sysctl.d"
	// NACMPolicyPath is the installer output path for the anonymous RESTCONF policy.
	NACMPolicyPath = "/etc/sysrepo-nacm-anonymous.xml"
	// FileMode permits every user to read installed files.
	FileMode fs.FileMode = 0o644
)

// Role selects an installation profile.
type Role string

const (
	// RoleWAN selects the gateway services and schema.
	RoleWAN Role = "wan"
	// RoleFailover selects agent and interface-manager settings for a failover container.
	RoleFailover Role = "failover"
	// RoleHost selects the hypervisor interface manager.
	RoleHost Role = "host"
)

// File maps an embedded asset to its absolute installation path.
// A systemd drop-in can use a different destination name.
type File struct {
	// Embedded identifies an asset in the binary.
	Embedded string
	// Dest is absolute; the installer prepends the selected root during a rooted install.
	Dest string
}

// Spec defines a role's files, services, and schema requirements.
type Spec struct {
	// Files preserves installation order.
	Files []File
	// Enable uses concrete service instances rather than template names.
	Enable []string
	// Schema enables YANG installation and NACM imports after unit installation.
	Schema bool
	// NotOwned includes system services that read installed files.
	// The installer does not enable these services; Units includes them for consumers.
	NotOwned []NotOwnedUnit
}

// NotOwnedUnit lists configuration files read by a system service.
type NotOwnedUnit struct {
	Name string
	// Files uses absolute installation paths.
	Files []string
}

func unit(name string) File {
	return File{Embedded: name, Dest: filepath.Join(SystemdUnitDir, name)}
}

func specs() map[Role]Spec {
	return map[Role]Spec{
		RoleWAN: {
			Files: []File{
				unit("mwan-agent.service"),
				unit("mwan-ifmgr@.service"),
				{
					Embedded: "mwan-ifmgr-wan.conf",
					Dest:     filepath.Join(SystemdUnitDir, "mwan-ifmgr@wan.service.d", "firewall.conf"),
				},
				unit("mwan-trace-boot.service"),
				unit("rousette.service"),
				unit("nghttpx-wanconfig.service"),
				{
					Embedded: "systemd-networkd-override.conf",
					Dest:     filepath.Join(SystemdUnitDir, "systemd-networkd.service.d", "override.conf"),
				},
				{
					Embedded: "99-quiet-console.conf",
					Dest:     filepath.Join(SysctlDir, "99-quiet-console.conf"),
				},
			},
			Enable: []string{
				"mwan-agent.service", "mwan-ifmgr@wan.service", "mwan-trace-boot.service",
				"rousette.service", "nghttpx-wanconfig.service",
			},
			Schema: true,
			NotOwned: []NotOwnedUnit{
				{
					Name:  "systemd-networkd.service",
					Files: []string{filepath.Join(SystemdUnitDir, "systemd-networkd.service.d", "override.conf")},
				},
				{
					Name:  "systemd-sysctl.service",
					Files: []string{filepath.Join(SysctlDir, "99-quiet-console.conf")},
				},
			},
		},
		RoleFailover: {
			Files: []File{
				unit("mwan-agent.service"),
				unit("mwan-ifmgr.service"),
				{
					Embedded: "mwan-ifmgr-failover.conf",
					Dest:     filepath.Join(SystemdUnitDir, "mwan-ifmgr.service.d", "lxc-failover.conf"),
				},
			},
			Enable:   []string{"mwan-agent.service", "mwan-ifmgr.service"},
			Schema:   false,
			NotOwned: nil,
		},
		RoleHost: {
			Files:    []File{unit("mwan-ifmgr.service")},
			Enable:   []string{"mwan-ifmgr.service"},
			Schema:   false,
			NotOwned: nil,
		},
	}
}

// For returns a false presence result for an unknown role.
func For(role Role) (Spec, bool) {
	spec, known := specs()[role]
	return spec, known
}

// Roles returns role names in sorted order.
func Roles() []Role {
	all := specs()
	roles := make([]Role, 0, len(all))
	for role := range all {
		roles = append(roles, role)
	}
	slices.Sort(roles)
	return roles
}

// Read returns an error when an embedded asset is absent.
func Read(embedded string) ([]byte, error) {
	content, err := unitFS.ReadFile(embedded)
	if err != nil {
		slog.Warn("installspec: read the embedded file failed", "name", embedded, "err", err)
		return nil, fmt.Errorf("read the embedded file %s: %w", embedded, err)
	}
	return content, nil
}

// NACMPolicy returns the embedded anonymous RESTCONF access policy.
func NACMPolicy() ([]byte, error) {
	return Read(nacmPolicyName)
}
