// Package installspec is the one list of files and units that `mwan install`
// writes for each host role. It embeds the file bodies and imports no cgo
// package, which lets the OpenTofu provider read the list the binary installs.
package installspec

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
)

// unitFS embeds the files the install verb writes. Each file is listed by name
// instead of by pattern: a file added to this directory ships only after
// someone adds it to this list.
//
//go:embed mwan-agent.service mwan-ifmgr.service mwan-ifmgr@.service mwan-trace-boot.service mwan-ifmgr-failover.conf
//go:embed rousette.service nghttpx-wanconfig.service systemd-networkd-override.conf mwan-ifmgr-wan.conf
//go:embed 99-quiet-console.conf nacm-anonymous.xml
var unitFS embed.FS

// nacmPolicyName is the embedded name of the read-only RESTCONF access policy:
// NACM denies every write and grants the anonymous user read access, the
// contract rousette serves anonymous clients under.
const nacmPolicyName = "nacm-anonymous.xml"

const (
	// SystemdUnitDir is the administrator's unit directory, which outranks
	// anything a package ships.
	SystemdUnitDir = "/etc/systemd/system"
	// SysctlDir is the directory systemd-sysctl reads at boot.
	SysctlDir = "/etc/sysctl.d"
	// NACMPolicyPath is where the wan role writes the policy on the host, the
	// path the deploy has always written it to.
	NACMPolicyPath = "/etc/sysrepo-nacm-anonymous.xml"
	// FileMode matches what the playbooks write today, for the units and for
	// every other file the verb installs.
	FileMode fs.FileMode = 0o644
)

// Role is the set of units one kind of host runs.
type Role string

const (
	// RoleWAN is the gateway VM: the agent, the instanced interface manager,
	// the boot trace oneshot, the wanconfig RESTCONF server and its front-end
	// proxy, the systemd-networkd drop-in, and the quiet console
	// sysctl file.
	RoleWAN Role = "wan"
	// RoleFailover is the failover container: the agent, plus the interface
	// manager with the sandbox relaxation slaac_health needs.
	RoleFailover Role = "failover"
	// RoleHost is a Proxmox hypervisor, which runs the single-instance
	// interface manager in its out-of-band role.
	RoleHost Role = "host"
)

// File is one embedded file and its host destination. A drop-in's destination
// is a path inside a unit's .d directory, which differs from the embedded name.
type File struct {
	// Embedded is the file's name inside the embedded set.
	Embedded string
	// Dest is the absolute host path to write; a run under --root writes it
	// below the root.
	Dest string
}

// Spec is one role's install list: the files to write and the units to enable.
type Spec struct {
	// Files are the embedded files this role installs, in write order.
	Files []File
	// Enable are the unit names to enable, which for an instanced unit is a
	// concrete instance rather than the template.
	Enable []string
	// Schema is set for the role that runs the wanconfig datastore: after the
	// units, the run writes the embedded modules and the NACM policy and
	// installs the modules into sysrepo.
	Schema bool
	// NotOwned are units that the role does not enable and that read files the
	// role writes. `mwan install` ignores them; Units lists them.
	NotOwned []NotOwnedUnit
}

// NotOwnedUnit is a system unit that reads files a role writes.
type NotOwnedUnit struct {
	// Name is the unit name.
	Name string
	// Files are the host paths of the role's files that the unit reads.
	Files []string
}

// unit is an embedded unit file that installs under its own name in the
// systemd unit directory.
func unit(name string) File {
	return File{Embedded: name, Dest: filepath.Join(SystemdUnitDir, name)}
}

// specs maps each role onto what it installs, matching what the playbooks
// write and enable today.
//
// One interface-manager unit body serves every host, and a deployment that
// needs its sandbox relaxed says so in a drop-in. mwan-ifmgr.service's own
// ProtectKernelTunables comment prescribes exactly that, naming this file's
// path, and the suburban hypervisor already relaxes the same setting the same
// way through a drop-in configs deploys.
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

// For returns the install list for a role and whether the role exists.
func For(role Role) (Spec, bool) {
	spec, known := specs()[role]
	return spec, known
}

// Roles lists the roles in a stable order for help and errors.
func Roles() []Role {
	all := specs()
	roles := make([]Role, 0, len(all))
	for role := range all {
		roles = append(roles, role)
	}
	slices.Sort(roles)
	return roles
}

// Read returns the bytes of one embedded file.
func Read(embedded string) ([]byte, error) {
	content, err := unitFS.ReadFile(embedded)
	if err != nil {
		slog.Warn("installspec: read the embedded file failed", "name", embedded, "err", err)
		return nil, fmt.Errorf("read the embedded file %s: %w", embedded, err)
	}
	return content, nil
}

// NACMPolicy returns the bytes of the read-only RESTCONF access policy the wan
// role installs at NACMPolicyPath.
func NACMPolicy() ([]byte, error) {
	return Read(nacmPolicyName)
}
