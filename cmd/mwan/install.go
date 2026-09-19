package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"

	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/yangpub"
)

// unitFS carries the files the install verb writes inside the binary, so the
// file a host runs comes from the release its pin names. Each file is named
// here rather than matched by a pattern, so a file added to this directory
// never reaches the binary until someone says it should.
//
//go:embed mwan-agent.service mwan-ifmgr.service mwan-ifmgr@.service mwan-trace-boot.service mwan-ifmgr-failover.conf
//go:embed rousette.service nghttpx-wanconfig.service nftables-override.conf systemd-networkd-override.conf
//go:embed 99-quiet-console.conf
var unitFS embed.FS

const (
	// systemdUnitDir is where the units go. This is the administrator's unit
	// directory, which outranks anything a package ships.
	systemdUnitDir = "/etc/systemd/system"
	// sysctlDir is where the kernel settings files go; systemd-sysctl reads
	// them at boot.
	sysctlDir = "/etc/sysctl.d"
	// systemdUnitMode matches what the playbooks write today, for the units
	// and for every other file the verb installs.
	systemdUnitMode fs.FileMode = 0o644
)

// installRole is the set of units one kind of host runs.
type installRole string

const (
	// roleWAN is the gateway VM: the agent, the instanced interface manager,
	// the boot trace oneshot, the wanconfig RESTCONF server and its front-end
	// proxy, the nftables and systemd-networkd drop-ins, and the quiet console
	// sysctl file.
	roleWAN installRole = "wan"
	// roleFailover is the failover container: the agent, plus the interface
	// manager with the sandbox relaxation slaac_health needs.
	roleFailover installRole = "failover"
	// roleHost is a Proxmox hypervisor, which runs the single-instance
	// interface manager in its out-of-band role.
	roleHost installRole = "host"
)

// installedFile is one embedded file and where it lands on the host. A
// drop-in names a path inside a unit's .d directory, so the destination
// cannot always be the embedded file's own name.
type installedFile struct {
	// embedded is the file's name inside unitFS.
	embedded string
	// dest is the absolute host path to write; a run under --root writes it
	// below the root.
	dest string
}

// unit names an embedded unit file that installs under its own name in the
// systemd unit directory.
func unit(name string) installedFile {
	return installedFile{embedded: name, dest: filepath.Join(systemdUnitDir, name)}
}

// roleUnits is one role's units: the files to write and the units to enable.
type roleUnits struct {
	// files are the embedded files this role installs, in write order.
	files []installedFile
	// enable are the unit names to enable, which for an instanced unit is a
	// concrete instance rather than the template.
	enable []string
}

// installRoles maps each role onto what it installs, matching what the
// playbooks write and enable today.
//
// One interface-manager unit body serves every host, and a deployment that
// needs its sandbox relaxed says so in a drop-in. mwan-ifmgr.service's own
// ProtectKernelTunables comment prescribes exactly that, naming this file's
// path, and the suburban hypervisor already relaxes the same setting the same
// way through a drop-in configs deploys. Two full unit bodies would be the
// novelty here.
var installRoles = map[installRole]roleUnits{
	roleWAN: {
		files: []installedFile{
			unit("mwan-agent.service"),
			unit("mwan-ifmgr@.service"),
			unit("mwan-trace-boot.service"),
			unit("rousette.service"),
			unit("nghttpx-wanconfig.service"),
			{
				embedded: "nftables-override.conf",
				dest:     filepath.Join(systemdUnitDir, "nftables.service.d", "override.conf"),
			},
			{
				embedded: "systemd-networkd-override.conf",
				dest:     filepath.Join(systemdUnitDir, "systemd-networkd.service.d", "override.conf"),
			},
			{
				embedded: "99-quiet-console.conf",
				dest:     filepath.Join(sysctlDir, "99-quiet-console.conf"),
			},
		},
		enable: []string{
			"mwan-agent.service", "mwan-ifmgr@wan.service", "mwan-trace-boot.service",
			"rousette.service", "nghttpx-wanconfig.service",
		},
	},
	roleFailover: {
		files: []installedFile{
			unit("mwan-agent.service"),
			unit("mwan-ifmgr.service"),
			{
				embedded: "mwan-ifmgr-failover.conf",
				dest:     filepath.Join(systemdUnitDir, "mwan-ifmgr.service.d", "lxc-failover.conf"),
			},
		},
		enable: []string{"mwan-agent.service", "mwan-ifmgr.service"},
	},
	roleHost: {
		files:  []installedFile{unit("mwan-ifmgr.service")},
		enable: []string{"mwan-ifmgr.service"},
	},
}

const (
	exitInstallOK     = 0
	exitInstallFailed = 1
	exitInstallUsage  = 2
)

// installFlags is one parsed invocation of the subcommand.
type installFlags struct {
	role        string
	apply       bool
	printSchema string
	root        string
}

// installOutcome is what one install run did, so the caller reports it and a
// test asserts on it rather than on printed text.
type installOutcome struct {
	// changed names every file whose content the run replaced, in the order
	// it wrote them.
	changed []string
	// enabled names every unit the run asked systemd to enable.
	enabled []string
}

// runInstall is the `mwan install` entry point.
func runInstall(args []string) int {
	flags, err := parseInstallFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan install: %v\n", err)
		return exitInstallUsage
	}
	if flags.printSchema != "" {
		if _, err := yangpub.WriteSchema(flags.printSchema); err != nil {
			fmt.Fprintf(os.Stderr, "mwan install: %v\n", err)
			return exitInstallFailed
		}
		fmt.Fprintf(os.Stdout, "schema written to %s\n", flags.printSchema)
		return exitInstallOK
	}
	if !flags.apply {
		printInstallUsage(os.Stdout)
		return exitInstallOK
	}
	role := installRole(flags.role)
	if _, known := installRoles[role]; !known {
		fmt.Fprintf(os.Stderr, "mwan install: unknown role %q; want one of %s\n",
			flags.role, strings.Join(knownInstallRoles(), ", "))
		return exitInstallUsage
	}
	rooted := flags.root != ""
	outcome, err := installUnits(context.Background(), role, flags.root, enablerFor(rooted))
	reportInstall(os.Stdout, outcome, rooted)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan install: %v\n", err)
		return exitInstallFailed
	}
	return exitInstallOK
}

// enablerFor picks the systemd side of the run. A run under --root writes
// somewhere other than the host's unit directory, so enabling units on this
// machine would act on files the run did not write. Such a run leaves systemd
// alone and reports what it would have enabled.
func enablerFor(rooted bool) unitEnabler {
	if !rooted {
		return realUnitEnabler
	}
	return func(_ context.Context, _ []string, _ bool) error { return nil }
}

// unitEnabler is the systemd side of an install: reload the manager when
// reload is set, so it reads what was just written, then re-enable the named
// units. It is a seam so the file writing can be exercised where no system bus
// exists.
type unitEnabler func(ctx context.Context, units []string, reload bool) error

// installUnits writes the role's files under root and re-enables its units.
// It returns what it did even when it fails, so the caller reports the
// files that were already written before the failure.
//
// The units are re-enabled on every run, because a current unit file can still
// sit behind a stale install symlink when something other than this verb wrote
// it. systemd is asked to reload only when a file changed, so a second run
// leaves the manager's loaded state alone.
func installUnits(
	ctx context.Context,
	role installRole,
	root string,
	enabler unitEnabler,
) (installOutcome, error) {
	outcome := installOutcome{changed: nil, enabled: nil}
	units := installRoles[role]
	for _, file := range units.files {
		content, err := unitFS.ReadFile(file.embedded)
		if err != nil {
			return outcome, installFailed("read the embedded file", file.embedded, err)
		}
		path := filepath.Join(root, file.dest)
		changed, err := installfile.Write(path, content, systemdUnitMode)
		if err != nil {
			return outcome, installFailed("install the file", file.dest, err)
		}
		if changed {
			outcome.changed = append(outcome.changed, path)
		}
	}
	if err := enabler(ctx, units.enable, len(outcome.changed) > 0); err != nil {
		return outcome, err
	}
	outcome.enabled = units.enable
	return outcome, nil
}

// realUnitEnabler reloads systemd when reload is set, then removes each unit's existing install
// symlinks and writes the ones its current [Install] section names. That pair
// is what `systemctl reenable` does, and enabling alone is not enough: enable
// only creates symlinks at the paths the current unit names, so a unit whose
// WantedBy moved keeps the symlink under its old target and ends up wanted by
// both. A gateway proved that on 2026-09-18, when mwan-ifmgr@.service moved
// from multi-user.target to sysinit.target and the deployed host still
// reported multi-user.target with a two month old symlink.
//
// Disabling changes no running state. It removes symlinks; it does not stop
// the daemon, so the unit keeps running across this call and the deploy keeps
// the restart decision.
func realUnitEnabler(ctx context.Context, units []string, reload bool) error {
	conn, err := systemddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return installFailed("connect to systemd for", strings.Join(units, " "), err)
	}
	defer conn.Close()
	return reenableUnits(ctx, conn, units, reload)
}

// unitInstaller is the part of the systemd manager the enable path drives. It
// is an interface so the call sequence can be exercised where no system bus
// exists; *systemddbus.Conn is the only implementation that ships.
type unitInstaller interface {
	ReloadContext(ctx context.Context) error
	DisableUnitFilesContext(
		ctx context.Context, files []string, runtime bool,
	) ([]systemddbus.DisableUnitFileChange, error)
	EnableUnitFilesContext(
		ctx context.Context, files []string, runtime bool, force bool,
	) (bool, []systemddbus.EnableUnitFileChange, error)
}

// reenableUnits reloads the manager when reload is set, clears each unit's
// install symlinks, and writes the ones the unit currently names. The symlinks
// come from the unit files on disk, so re-enabling without a reload still
// reads the current [Install] section.
func reenableUnits(
	ctx context.Context, manager unitInstaller, units []string, reload bool,
) error {
	named := strings.Join(units, " ")
	if reload {
		if err := manager.ReloadContext(ctx); err != nil {
			return installFailed("reload systemd before enabling", named, err)
		}
	}
	// A unit that is not enabled has no symlinks to remove, which systemd
	// reports as an empty change list rather than an error, so a first install
	// runs through here unremarkably.
	if _, err := manager.DisableUnitFilesContext(ctx, units, false); err != nil {
		return installFailed("clear the install symlinks of", named, err)
	}
	// runtimeOnly=false writes the symlinks under /etc so they survive a
	// reboot; force=true replaces one that points somewhere else.
	if _, _, err := manager.EnableUnitFilesContext(ctx, units, false, true); err != nil {
		return installFailed("enable", named, err)
	}
	return nil
}

// installFailed logs one failure where it happened and returns it wrapped
// under the same words, so the cause reads the same in the journal and in the
// message the command prints.
func installFailed(operation string, name string, err error) error {
	slog.Warn("install: "+operation+" failed", "name", name, "err", err)
	return fmt.Errorf("%s %s: %w", operation, name, err)
}

// reportInstall prints one line per changed file, then the units enabled. A
// run that changed no file says so, so an operator can tell "already correct"
// from "did nothing because it failed early". A rooted run says what it would
// have enabled, because it asked systemd for nothing.
func reportInstall(out io.Writer, outcome installOutcome, rooted bool) {
	if len(outcome.changed) == 0 {
		fmt.Fprintln(out, "no change")
	}
	for _, path := range outcome.changed {
		fmt.Fprintf(out, "wrote %s\n", path)
	}
	if len(outcome.enabled) == 0 {
		return
	}
	verb := "enabled"
	if rooted {
		verb = "would enable"
	}
	fmt.Fprintf(out, "%s %s\n", verb, strings.Join(outcome.enabled, " "))
}

// parseInstallFlags reads the subcommand's flags.
func parseInstallFlags(args []string) (installFlags, error) {
	flags := installFlags{role: "", apply: false, printSchema: "", root: ""}
	set := flag.NewFlagSet("install", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	set.StringVar(&flags.role, "role", "",
		"which host this is: "+strings.Join(knownInstallRoles(), ", "))
	set.BoolVar(&flags.apply, "apply", false,
		"write the files and enable the units; without it, print this help and exit")
	set.StringVar(&flags.printSchema, "print-schema", "",
		"write the embedded YANG modules to this directory and exit, touching nothing else")
	set.StringVar(&flags.root, "root", "",
		"write under this directory instead of /, and name the units rather than enabling them")
	if err := set.Parse(args); err != nil {
		return flags, installFailed("parse the flags of", "mwan install", err)
	}
	if flags.printSchema != "" && flags.apply {
		return flags, errors.New("--print-schema writes no host files, so it does not take --apply")
	}
	if flags.apply && flags.role == "" {
		return flags, errors.New("--apply needs --role")
	}
	return flags, nil
}

// knownInstallRoles lists the roles in a stable order for help and errors.
func knownInstallRoles() []string {
	roles := make([]string, 0, len(installRoles))
	for role := range installRoles {
		roles = append(roles, string(role))
	}
	sort.Strings(roles)
	return roles
}

// printInstallUsage explains what a run would do. This is what an operator
// sees when they leave --apply off, which is the default.
func printInstallUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: mwan install --role <"+strings.Join(knownInstallRoles(), "|")+"> --apply")
	fmt.Fprintln(out, "       mwan install --print-schema <dir>")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Writes the files this binary owns onto the host and enables its units.")
	fmt.Fprintln(out, "Without --apply nothing is written. A second run writes no file and")
	fmt.Fprintln(out, "re-enables the units without reloading systemd.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Files installed, by role:")
	for _, name := range knownInstallRoles() {
		units := installRoles[installRole(name)]
		written := make([]string, 0, len(units.files))
		for _, file := range units.files {
			written = append(written, file.dest)
		}
		fmt.Fprintf(out, "  %-9s writes %s\n", name, strings.Join(written, " "))
		fmt.Fprintf(out, "  %-9s enables %s\n", "", strings.Join(units.enable, " "))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The YANG modules are embedded too. --print-schema writes them to a")
	fmt.Fprintln(out, "directory for validation. Installing them into sysrepo is still the")
	fmt.Fprintln(out, "deploy's job; this verb does not touch the datastore.")
}
