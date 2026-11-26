package commandline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"

	"goodkind.io/mwan/gateway/installspec"
	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/installfile"
	"goodkind.io/mwan/gateway/internal/yangpub"
)

const (
	exitInstallOK     = 0
	exitInstallFailed = 1
	exitInstallUsage  = 2
)

var errUnknownInstallRole = errors.New("unknown install role")

type unknownInstallRoleError struct {
	role installspec.Role
}

func (err unknownInstallRoleError) Error() string {
	return fmt.Sprintf("unknown role %q", err.role)
}

func (err unknownInstallRoleError) Unwrap() error {
	return errUnknownInstallRole
}

type installOutcome struct {
	changed      []string
	enabled      []string
	modules      []yangpub.ModuleChange
	nacmImported []yangpub.Datastore
}

func runInstallOptions(flags commandopts.Install, output, diagnostics io.Writer) int {
	if flags.PrintSchema != "" {
		if _, err := yangpub.WriteSchema(flags.PrintSchema); err != nil {
			fmt.Fprintf(diagnostics, "mwan install: %v\n", err)

			return exitInstallFailed
		}

		fmt.Fprintf(output, "schema written to %s\n", flags.PrintSchema)

		return exitInstallOK
	}

	role := installspec.Role(flags.Role)
	spec, known := installspec.For(role)
	if !known {
		fmt.Fprintf(
			diagnostics,
			"mwan install: unknown role %q; want one of %s\n",
			flags.Role,
			strings.Join(knownInstallRoles(), ", "),
		)

		return exitInstallUsage
	}

	rooted := flags.Root != ""
	ctx := context.Background()
	outcome, err := installUnits(ctx, role, flags.Root, enablerFor(rooted))
	if err == nil && spec.Schema {
		var schema schemaOutcome
		schema, err = installSchema(ctx, slog.Default(), flags.Root)
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

func enablerFor(rooted bool) unitEnabler {
	if !rooted {
		return realUnitEnabler
	}

	return func(_ context.Context, _ []string, _ bool) error { return nil }
}

type unitEnabler func(ctx context.Context, units []string, reload bool) error

// Install units and replace stale systemd links without restarting services.
func installUnits(
	ctx context.Context,
	role installspec.Role,
	root string,
	enabler unitEnabler,
) (installOutcome, error) {
	outcome := installOutcome{changed: nil, enabled: nil, modules: nil, nacmImported: nil}
	spec, known := installspec.For(role)
	if !known {
		return outcome, unknownInstallRoleError{role: role}
	}

	for _, file := range spec.Files {
		content, err := installspec.Read(file.Embedded)
		if err != nil {
			return outcome, installFailed("read the embedded file", file.Embedded, err)
		}

		path := filepath.Join(root, file.Dest)
		changed, err := installfile.Write(path, content, installspec.FileMode)
		if err != nil {
			return outcome, installFailed("install the file", file.Dest, err)
		}

		if changed {
			outcome.changed = append(outcome.changed, path)
		}
	}

	slog.InfoContext(ctx, "install: processed unit files", "written", outcome.changed)
	if err := enabler(ctx, spec.Enable, len(outcome.changed) > 0); err != nil {
		return outcome, err
	}

	outcome.enabled = spec.Enable

	return outcome, nil
}

// Replace stale WantedBy links without restarting services.
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

	// An empty change list can indicate a first install.
	if _, err := manager.DisableUnitFilesContext(ctx, units, false); err != nil {
		return installFailed("clear the install symlinks of", named, err)
	}

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
	if len(outcome.changed) == 0 {
		fmt.Fprintln(out, "no change")
	}

	for _, path := range outcome.changed {
		fmt.Fprintf(out, "wrote %s\n", path)
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

// Reject / before opening sysrepo.
// Rooted installs use a private shared-memory prefix.
// They cannot open the host repository.
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
