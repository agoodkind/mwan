package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/yangpub"
)

// recordingEnabler stands in for the system bus, which a test host does not
// have. It records what it was asked to enable and nothing else; the files on
// disk are what the assertions are about.
type recordingEnabler struct {
	calls [][]string
}

func (r *recordingEnabler) enable(_ context.Context, units []string) error {
	r.calls = append(r.calls, units)
	return nil
}

// TestInstallUnitsWritesTheWanRoleUnits proves a wan-role install puts the
// three units the gateway runs into the systemd directory with the bytes the
// binary carries, and asks systemd to enable the wan instance rather than the
// template.
func TestInstallUnitsWritesTheWanRoleUnits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}

	outcome, err := installUnits(t.Context(), roleWAN, root, enabler.enable)
	if err != nil {
		t.Fatalf("installUnits: %v", err)
	}

	wantFiles := []string{
		"mwan-agent.service",
		"mwan-ifmgr@.service",
		"mwan-trace-boot.service",
	}
	if len(outcome.changed) != len(wantFiles) {
		t.Fatalf("changed = %v, want %d files", outcome.changed, len(wantFiles))
	}
	for _, file := range wantFiles {
		path := filepath.Join(root, systemdUnitDir, file)
		onDisk, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		embedded, embedErr := unitFS.ReadFile(file)
		if embedErr != nil {
			t.Fatalf("read embedded %s: %v", file, embedErr)
		}
		if !bytes.Equal(onDisk, embedded) {
			t.Fatalf("%s on disk differs from the embedded copy", file)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm() != systemdUnitMode {
			t.Fatalf("%s mode = %v, want %v", file, info.Mode().Perm(), systemdUnitMode)
		}
	}

	// The template cannot be enabled, only an instance of it, so a run that
	// enabled "mwan-ifmgr@.service" would leave the gateway's daemon off
	// after a reboot.
	wantEnable := []string{
		"mwan-agent.service",
		"mwan-ifmgr@wan.service",
		"mwan-trace-boot.service",
	}
	if len(enabler.calls) != 1 {
		t.Fatalf("enable called %d times, want 1", len(enabler.calls))
	}
	if strings.Join(enabler.calls[0], " ") != strings.Join(wantEnable, " ") {
		t.Fatalf("enabled %v, want %v", enabler.calls[0], wantEnable)
	}
}

// TestInstallUnitsIsIdempotent proves the second run of the same install
// changes no file and does not touch systemd, which is what lets a deploy run
// the verb every time without restarting anything.
func TestInstallUnitsIsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}

	if _, err := installUnits(t.Context(), roleWAN, root, enabler.enable); err != nil {
		t.Fatalf("first installUnits: %v", err)
	}
	unitPath := filepath.Join(root, systemdUnitDir, "mwan-agent.service")
	before, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat after the first run: %v", err)
	}

	second, err := installUnits(t.Context(), roleWAN, root, enabler.enable)
	if err != nil {
		t.Fatalf("second installUnits: %v", err)
	}

	if len(second.changed) != 0 {
		t.Fatalf("second run changed %v, want nothing", second.changed)
	}
	if len(second.enabled) != 0 {
		t.Fatalf("second run enabled %v, want nothing", second.enabled)
	}
	if len(enabler.calls) != 1 {
		t.Fatalf("enable called %d times across two runs, want 1", len(enabler.calls))
	}
	after, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat after the second run: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("second run rewrote %s", unitPath)
	}
}

// TestInstallUnitsRewritesAChangedUnit proves the verb repairs a unit an
// operator edited on the host, which is the case that makes the release pin
// mean something: whatever is on disk, the run puts the release's bytes back.
func TestInstallUnitsRewritesAChangedUnit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}
	if _, err := installUnits(t.Context(), roleHost, root, enabler.enable); err != nil {
		t.Fatalf("first installUnits: %v", err)
	}
	unitPath := filepath.Join(root, systemdUnitDir, "mwan-ifmgr.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/false\n"), 0o644); err != nil {
		t.Fatalf("overwrite the unit: %v", err)
	}

	outcome, err := installUnits(t.Context(), roleHost, root, enabler.enable)
	if err != nil {
		t.Fatalf("second installUnits: %v", err)
	}

	if len(outcome.changed) != 1 || outcome.changed[0] != unitPath {
		t.Fatalf("changed = %v, want just %s", outcome.changed, unitPath)
	}
	onDisk, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read %s: %v", unitPath, err)
	}
	embedded, err := unitFS.ReadFile("mwan-ifmgr.service")
	if err != nil {
		t.Fatalf("read the embedded unit: %v", err)
	}
	if !bytes.Equal(onDisk, embedded) {
		t.Fatal("the edited unit was not put back to the embedded bytes")
	}
	if len(enabler.calls) != 2 {
		t.Fatalf("enable called %d times, want 2", len(enabler.calls))
	}
}

// TestInstallFailoverWritesTheUnitAndItsDropIn proves the failover container
// gets one shared unit body plus the drop-in that relaxes the sandbox, rather
// than a second full unit. The drop-in path is the one mwan-ifmgr.service's
// own ProtectKernelTunables comment prescribes.
func TestInstallFailoverWritesTheUnitAndItsDropIn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}

	outcome, err := installUnits(t.Context(), roleFailover, root, enabler.enable)
	if err != nil {
		t.Fatalf("installUnits: %v", err)
	}

	wantDests := []string{
		"mwan-agent.service",
		"mwan-ifmgr.service",
		"mwan-ifmgr.service.d/lxc-failover.conf",
	}
	if len(outcome.changed) != len(wantDests) {
		t.Fatalf("changed = %v, want %d files", outcome.changed, len(wantDests))
	}
	for _, dest := range wantDests {
		if _, statErr := os.Stat(filepath.Join(root, systemdUnitDir, dest)); statErr != nil {
			t.Fatalf("stat %s: %v", dest, statErr)
		}
	}

	// The unit body must be the same one the hypervisor gets. A second body
	// would put the sandbox defaults in two places.
	shared, err := os.ReadFile(filepath.Join(root, systemdUnitDir, "mwan-ifmgr.service"))
	if err != nil {
		t.Fatalf("read the installed unit: %v", err)
	}
	embedded, err := unitFS.ReadFile("mwan-ifmgr.service")
	if err != nil {
		t.Fatalf("read the embedded unit: %v", err)
	}
	if !bytes.Equal(shared, embedded) {
		t.Fatal("the failover role installed a different unit body from the embedded one")
	}

	// These three settings are what the failover container needs and what the
	// unit it replaces set. slaac_health writes IPv6 sysctls, so the tunables
	// must be writable and that path must be in ReadWritePaths; the empty
	// BindReadOnlyPaths keeps the base unit's /root/.ssh mount off a container
	// whose modules never read it.
	dropIn, err := os.ReadFile(
		filepath.Join(root, systemdUnitDir, "mwan-ifmgr.service.d", "lxc-failover.conf"))
	if err != nil {
		t.Fatalf("read the drop-in: %v", err)
	}
	for _, setting := range []string{
		"ProtectKernelTunables=false",
		"ReadWritePaths=/var/log /var/run /run /proc/sys/net/ipv6/conf",
		"BindReadOnlyPaths=",
	} {
		if !bytes.Contains(dropIn, []byte(setting)) {
			t.Errorf("the drop-in does not set %q", setting)
		}
	}
	// ReadWritePaths is a list and a drop-in appends to it, so the empty
	// assignment has to come first or the effective value repeats the base
	// unit's three paths. Real systemd showed that duplication before this
	// line existed.
	if !bytes.Contains(dropIn, []byte("ReadWritePaths=\nReadWritePaths=")) {
		t.Error("the drop-in does not reset ReadWritePaths before setting it")
	}

	wantEnable := []string{"mwan-agent.service", "mwan-ifmgr.service"}
	if len(enabler.calls) != 1 {
		t.Fatalf("enable called %d times, want 1", len(enabler.calls))
	}
	if strings.Join(enabler.calls[0], " ") != strings.Join(wantEnable, " ") {
		t.Fatalf("enabled %v, want %v", enabler.calls[0], wantEnable)
	}
}

// TestHostRoleGetsNoFailoverRelaxation proves the hypervisor's install does
// not carry the failover drop-in. The base unit keeps ProtectKernelTunables
// true on purpose, and a drop-in leaking onto another role would silently
// relax every ifmgr host.
func TestHostRoleGetsNoFailoverRelaxation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}

	if _, err := installUnits(t.Context(), roleHost, root, enabler.enable); err != nil {
		t.Fatalf("installUnits: %v", err)
	}

	dropInDir := filepath.Join(root, systemdUnitDir, "mwan-ifmgr.service.d")
	if _, err := os.Stat(dropInDir); !os.IsNotExist(err) {
		t.Fatalf("the host role created %s (err %v), want it absent", dropInDir, err)
	}
}

// TestInstallWithoutApplyWritesNothing proves the default is safe: a run with
// no --apply prints its help and leaves the host alone.
func TestInstallWithoutApplyWritesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	code := runInstall([]string{"--role", "wan", "--root", root})

	if code != exitInstallOK {
		t.Fatalf("exit code = %d, want %d", code, exitInstallOK)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read the root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a run without --apply created %d entries under the root", len(entries))
	}
}

// TestInstallApplyUnderARootTouchesNoSystemd proves a rooted run writes the
// units and leaves the running machine's systemd alone, which is what makes
// --root safe to use on a host that is running the daemon.
func TestInstallApplyUnderARootTouchesNoSystemd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	code := runInstall([]string{"--apply", "--role", "host", "--root", root})

	if code != exitInstallOK {
		t.Fatalf("exit code = %d, want %d", code, exitInstallOK)
	}
	unitPath := filepath.Join(root, systemdUnitDir, "mwan-ifmgr.service")
	onDisk, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read %s: %v", unitPath, err)
	}
	embedded, err := unitFS.ReadFile("mwan-ifmgr.service")
	if err != nil {
		t.Fatalf("read the embedded unit: %v", err)
	}
	if !bytes.Equal(onDisk, embedded) {
		t.Fatal("the rooted run did not write the embedded bytes")
	}
}

// TestInstallApplyNeedsARole proves a run that would change the host refuses
// to guess which host it is on.
func TestInstallApplyNeedsARole(t *testing.T) {
	t.Parallel()

	code := runInstall([]string{"--apply", "--root", t.TempDir()})

	if code != exitInstallUsage {
		t.Fatalf("exit code = %d, want %d", code, exitInstallUsage)
	}
}

// TestInstallRejectsAnUnknownRole proves a typo in --role fails loudly rather
// than installing an empty set and reporting success.
func TestInstallRejectsAnUnknownRole(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	code := runInstall([]string{"--apply", "--role", "gateway", "--root", root})

	if code != exitInstallUsage {
		t.Fatalf("exit code = %d, want %d", code, exitInstallUsage)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("an unknown role wrote %d entries (err %v)", len(entries), err)
	}
}

// TestInstallPrintSchemaWritesEveryModule proves --print-schema materialises
// the whole model set, which is what the controller validates a rendered
// network document against, and that each file holds the module and revision
// its name claims. That second check is what the schema gates and the loader
// rely on: both name the directory rather than a file list, so a file whose
// name and contents disagree would silently install the wrong revision.
func TestInstallPrintSchemaWritesEveryModule(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "schema")

	code := runInstall([]string{"--print-schema", dir})

	if code != exitInstallOK {
		t.Fatalf("exit code = %d, want %d", code, exitInstallOK)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != len(yangpub.SchemaModules) {
		t.Fatalf("wrote %d files, want %d", len(entries), len(yangpub.SchemaModules))
	}
	for _, module := range yangpub.SchemaModules {
		path := filepath.Join(dir, module.File)
		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		wantName, wantRevision, found := strings.Cut(strings.TrimSuffix(module.File, ".yang"), "@")
		if !found {
			t.Fatalf("module file %s carries no @revision", module.File)
		}
		if !bytes.Contains(written, []byte("module "+wantName+" {")) {
			t.Errorf("%s does not declare module %s", module.File, wantName)
		}
		if !bytes.Contains(written, []byte("revision "+wantRevision)) {
			t.Errorf("%s does not carry revision %s", module.File, wantRevision)
		}
	}
}

// TestInstalledUnitsAreTheOnesTheDaemonNames proves every unit the install
// verb enables for the wan role is a unit the binary's own debug output
// reports on, so the two lists cannot drift apart silently.
func TestInstalledUnitsAreTheOnesTheDaemonNames(t *testing.T) {
	t.Parallel()
	focus := make(map[string]bool, len(debugSystemdFocusUnits))
	for _, name := range debugSystemdFocusUnits {
		focus[name] = true
	}

	for _, unit := range installRoles[roleWAN].enable {
		if unit == "mwan-trace-boot.service" {
			// The boot trace is a oneshot that has already exited by the time
			// anything inspects the running system, so it is not a unit the
			// debug view times.
			continue
		}
		if !focus[unit] {
			t.Errorf("the wan role enables %s, which the debug view does not name", unit)
		}
	}
}
