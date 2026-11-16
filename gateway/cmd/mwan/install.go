package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/installspec"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	exitInstallOK     = 0
	exitInstallFailed = 1
	exitInstallUsage  = 2
)

type installFlags struct {
	role        string
	apply       bool
	printSchema string
	root        string
	guestType   string
}

type installOutcome struct {
	changed      []string
	removed      []string
	enabled      []string
	modules      []yangpub.ModuleChange
	nacmImported []yangpub.Datastore
}

func runInstallOptions(flags installFlags, output, diagnostics io.Writer) int {
	if flags.printSchema != "" {
		if _, err := yangpub.WriteSchema(flags.printSchema); err != nil {
			fmt.Fprintf(diagnostics, "mwan install: %v\n", err)
			return exitInstallFailed
		}

		fmt.Fprintf(output, "schema written to %s\n", flags.printSchema)

		return exitInstallOK
	}

	role := installspec.Role(flags.role)
	spec, known := installspec.For(role)
	if !known {
		fmt.Fprintf(
			diagnostics,
			"mwan install: unknown role %q; want one of %s\n",
			flags.role,
			strings.Join(knownInstallRoles(), ", "),
		)

		return exitInstallUsage
	}

	rooted := flags.root != ""
	ctx := context.Background()
	outcome, err := installUnits(
		ctx,
		role,
		flags.root,
		config.GuestType(flags.guestType),
		enablerFor(rooted),
	)
	if err == nil && spec.ValidationSchema {
		var changed []string
		_, changed, err = yangpub.WriteSchemaChanges(
			filepath.Join(flags.root, networkjson.DefaultSchemaDir),
		)
		outcome.changed = append(outcome.changed, changed...)
	}

	if err == nil && spec.Schema {
		var schema schemaOutcome
		schema, err = installSchema(ctx, slog.Default(), flags.root)
		outcome.changed = append(outcome.changed, schema.changed...)
		outcome.modules = schema.modules
		outcome.nacmImported = schema.nacmImported
	}

	reportInstall(output, outcome, rooted)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan install: %v\n", err)
		return exitInstallFailed
	}

	return exitInstallOK
}

// A rooted install must not enable units on the host.
func enablerFor(rooted bool) unitEnabler {
	if !rooted {
		return realUnitEnabler
	}

	return func(_ context.Context, _ []string, _ bool) error { return nil }
}

type unitEnabler func(ctx context.Context, units []string, reload bool) error

// Re-enable units even when files are unchanged to remove stale installation links.
// Reload the manager only when a file changes.
func installUnits(
	ctx context.Context,
	role installspec.Role,
	root string,
	guest config.GuestType,
	enabler unitEnabler,
) (installOutcome, error) {
	outcome := installOutcome{changed: nil, enabled: nil, modules: nil, nacmImported: nil}
	spec, known := installspec.For(role)
	if !known {
		return outcome, fmt.Errorf("unknown role %q", role)
	}

	for _, file := range spec.Files {
		content, install, err := file.Content(guest)
		if err != nil {
			return outcome, installFailed("read the embedded file", file.Embedded, err)
		}

		path := filepath.Join(root, file.Dest)
		if !install {
			removeErr := os.Remove(path)
			if removeErr == nil {
				outcome.removed = append(outcome.removed, path)
			} else if !errors.Is(removeErr, fs.ErrNotExist) {
				return outcome, installFailed("remove the file", file.Dest, removeErr)
			}

			continue
		}

		changed, err := installfile.Write(path, content, installspec.FileMode)
		if err != nil {
			return outcome, installFailed("install the file", file.Dest, err)
		}

		if changed {
			outcome.changed = append(outcome.changed, path)
		}
	}

	slog.InfoContext(
		ctx,
		"install: processed unit files",
		"written",
		outcome.changed,
		"removed",
		outcome.removed,
		"guest_type",
		guest,
	)
	if err := enabler(ctx, spec.Enable, len(outcome.changed)+len(outcome.removed) > 0); err != nil {
		return outcome, err
	}

	outcome.enabled = spec.Enable

	return outcome, nil
}

// Disabling and enabling unit files replaces old WantedBy links.
// These operations do not stop or restart running services.
func realUnitEnabler(ctx context.Context, units []string, reload bool) error {
	conn, err := systemddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return installFailed("connect to systemd for", strings.Join(units, " "), err)
	}
	defer conn.Close()

	return reenableUnits(ctx, conn, units, reload)
}

type unitInstaller interface {
	ReloadContext(ctx context.Context) error
	DisableUnitFilesContext(
		ctx context.Context,
		files []string,
		runtime bool,
	) ([]systemddbus.DisableUnitFileChange, error)
	EnableUnitFilesContext(
		ctx context.Context,
		files []string,
		runtime bool,
		force bool,
	) (bool, []systemddbus.EnableUnitFileChange, error)
}

// systemd reads the current [Install] section when creating unit links.
func reenableUnits(
	ctx context.Context,
	manager unitInstaller,
	units []string,
	reload bool,
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

func installFailed(operation string, name string, err error) error {
	slog.Warn("install: "+operation+" failed", "name", name, "err", err)
	return fmt.Errorf("%s %s: %w", operation, name, err)
}

func reportInstall(out io.Writer, outcome installOutcome, rooted bool) {
	if len(outcome.changed)+len(outcome.removed) == 0 {
		fmt.Fprintln(out, "no change")
	}

	for _, path := range outcome.changed {
		fmt.Fprintf(out, "wrote %s\n", path)
	}

	for _, path := range outcome.removed {
		fmt.Fprintf(out, "removed %s\n", path)
	}

	for _, module := range outcome.modules {
		if module.Action == yangpub.ModuleUpdated {
			fmt.Fprintf(
				out,
				"updated module %s from %s to %s\n",
				module.Module,
				module.PriorRevision,
				module.Revision,
			)
			continue
		}

		fmt.Fprintf(out, "installed module %s@%s\n", module.Module, module.Revision)
	}

	if len(outcome.nacmImported) > 0 {
		names := make([]string, 0, len(outcome.nacmImported))
		for _, ds := range outcome.nacmImported {
			names = append(names, string(ds))
		}

		fmt.Fprintf(
			out,
			"imported the %s policy into %s\n",
			installspec.NACMModule,
			strings.Join(names, " and "),
		)
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

// A rooted install must not use / or a symlink to /.
// It would write host files without enabling host services and would open the
// host's sysrepo repository with a separate shared-memory prefix.
// Clean root before resolving symlinks because [filepath.Join] cleans destination
// paths lexically without resolving symlinks.
func rootIsHost(root string) bool {
	cleaned := filepath.Clean(root)
	if cleaned == "/" {
		return true
	}

	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return false
	}

	return filepath.Clean(resolved) == "/"
}

func knownInstallRoles() []string {
	known := installspec.Roles()

	roles := make([]string, 0, len(known))
	for _, role := range known {
		roles = append(roles, string(role))
	}

	return roles
}
