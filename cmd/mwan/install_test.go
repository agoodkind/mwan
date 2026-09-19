package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"

	"goodkind.io/mwan/internal/yangpub"
)

// recordingEnabler stands in for the system bus, which a test host does not
// have. It records what it was asked to enable and whether it was asked to
// reload, and nothing else; the files on disk are what the assertions are
// about.
type recordingEnabler struct {
	calls   [][]string
	reloads []bool
}

func (r *recordingEnabler) enable(_ context.Context, units []string, reload bool) error {
	r.calls = append(r.calls, units)
	r.reloads = append(r.reloads, reload)
	return nil
}

// TestInstallUnitsWritesTheWanRoleUnits proves a wan-role install puts the
// three daemon units the gateway runs into the systemd directory with the bytes
// the binary carries, and asks systemd to enable the wan instance rather than
// the template, along with the wanconfig stack's two services.
func TestInstallUnitsWritesTheWanRoleUnits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}

	outcome, err := installUnits(t.Context(), roleWAN, root, enabler.enable)
	if err != nil {
		t.Fatalf("installUnits: %v", err)
	}

	// The role writes these three units plus the five files
	// TestInstallApplyWritesTheWanconfigAndHostFiles checks.
	wantFiles := []string{
		"mwan-agent.service",
		"mwan-ifmgr@.service",
		"mwan-trace-boot.service",
	}
	const wantChanged = 8
	if len(outcome.changed) != wantChanged {
		t.Fatalf("changed = %v, want %d files", outcome.changed, wantChanged)
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
		"rousette.service",
		"nghttpx-wanconfig.service",
	}
	if len(enabler.calls) != 1 {
		t.Fatalf("enable called %d times, want 1", len(enabler.calls))
	}
	if strings.Join(enabler.calls[0], " ") != strings.Join(wantEnable, " ") {
		t.Fatalf("enabled %v, want %v", enabler.calls[0], wantEnable)
	}
}

// TestInstallUnitsIsIdempotent proves the second run of the same install
// changes no file and does not reload systemd, which is what lets a deploy run
// the verb every time without restarting anything. It still re-enables the
// units, which changes only install symlinks.
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
	if strings.Join(second.enabled, " ") != strings.Join(installRoles[roleWAN].enable, " ") {
		t.Fatalf("second run enabled %v, want %v", second.enabled, installRoles[roleWAN].enable)
	}
	if len(enabler.reloads) != 2 || !enabler.reloads[0] || enabler.reloads[1] {
		t.Fatalf("reload requests across two runs = %v, want [true false]", enabler.reloads)
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

// symlinkManager stands in for the systemd manager, which a test host has no
// bus for. It keeps a real symlink tree under a temp root and applies the two
// rules this test depends on: disabling removes every install symlink
// pointing at the unit, wherever it sits, and enabling creates one under the
// target the unit's [Install] section currently names. Those are systemd's
// rules, checked separately against a running systemd.
type symlinkManager struct {
	root string
	// wantedBy is the target the unit file currently names, which is where
	// enabling puts its symlink.
	wantedBy string
	calls    []string
}

func (m *symlinkManager) ReloadContext(_ context.Context) error {
	m.calls = append(m.calls, "reload")
	return nil
}

func (m *symlinkManager) DisableUnitFilesContext(
	_ context.Context, files []string, _ bool,
) ([]systemddbus.DisableUnitFileChange, error) {
	m.calls = append(m.calls, "disable")
	targets, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if !strings.HasSuffix(target.Name(), ".wants") {
			continue
		}
		for _, file := range files {
			link := filepath.Join(m.root, target.Name(), file)
			if _, statErr := os.Lstat(link); statErr != nil {
				continue
			}
			if removeErr := os.Remove(link); removeErr != nil {
				return nil, removeErr
			}
		}
	}
	return nil, nil
}

func (m *symlinkManager) EnableUnitFilesContext(
	_ context.Context, files []string, _ bool, _ bool,
) (bool, []systemddbus.EnableUnitFileChange, error) {
	m.calls = append(m.calls, "enable")
	dir := filepath.Join(m.root, m.wantedBy+".wants")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, nil, err
	}
	for _, file := range files {
		link := filepath.Join(dir, file)
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if err := os.Symlink(filepath.Join(m.root, file), link); err != nil {
			return false, nil, err
		}
	}
	return false, nil, nil
}

// TestReenableRemovesASymlinkUnderTheOldTarget proves the enable path
// converges a unit whose WantedBy moved. Enabling alone creates symlinks only
// at the paths the current unit names, so the one under the old target would
// survive and the unit would be wanted by both. A gateway hit exactly this
// when mwan-ifmgr@.service moved from multi-user.target to sysinit.target.
func TestReenableRemovesASymlinkUnderTheOldTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const unitName = "mwan-ifmgr.service"
	if err := os.WriteFile(filepath.Join(root, unitName), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatalf("write the unit: %v", err)
	}
	// The host as an earlier deploy left it: wanted by the target the unit
	// used to name.
	staleDir := filepath.Join(root, "multi-user.target.wants")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("create the old target directory: %v", err)
	}
	stale := filepath.Join(staleDir, unitName)
	if err := os.Symlink(filepath.Join(root, unitName), stale); err != nil {
		t.Fatalf("create the stale symlink: %v", err)
	}
	manager := &symlinkManager{root: root, wantedBy: "sysinit.target", calls: nil}

	if err := reenableUnits(t.Context(), manager, []string{unitName}, true); err != nil {
		t.Fatalf("reenableUnits: %v", err)
	}

	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("the symlink under the old target survived (err %v)", err)
	}
	fresh := filepath.Join(root, "sysinit.target.wants", unitName)
	if _, err := os.Lstat(fresh); err != nil {
		t.Errorf("no symlink under the new target: %v", err)
	}
	if strings.Join(manager.calls, " ") != "reload disable enable" {
		t.Errorf("call sequence = %v, want reload disable enable", manager.calls)
	}
}

// TestInstallReenablesWhenNoFileChanged covers a host whose unit file is
// already current but whose install symlink is stale, which is what a host
// looks like after another tool wrote the new unit without re-enabling it. The
// second run changes no file and must still move the symlink.
func TestInstallReenablesWhenNoFileChanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	linkRoot := t.TempDir()
	manager := &symlinkManager{root: linkRoot, wantedBy: "sysinit.target", calls: nil}
	enabler := func(ctx context.Context, units []string, reload bool) error {
		return reenableUnits(ctx, manager, units, reload)
	}
	if _, err := installUnits(t.Context(), roleHost, root, enabler); err != nil {
		t.Fatalf("first installUnits: %v", err)
	}
	const unitName = "mwan-ifmgr.service"
	staleDir := filepath.Join(linkRoot, "multi-user.target.wants")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("create the old target directory: %v", err)
	}
	stale := filepath.Join(staleDir, unitName)
	if err := os.Symlink(filepath.Join(linkRoot, unitName), stale); err != nil {
		t.Fatalf("create the stale symlink: %v", err)
	}
	manager.calls = nil

	outcome, err := installUnits(t.Context(), roleHost, root, enabler)
	if err != nil {
		t.Fatalf("second installUnits: %v", err)
	}

	if len(outcome.changed) != 0 {
		t.Fatalf("second run changed %v, want nothing", outcome.changed)
	}
	if strings.Join(outcome.enabled, " ") != unitName {
		t.Fatalf("second run enabled %v, want %s", outcome.enabled, unitName)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("the symlink under the old target survived (err %v)", err)
	}
	if _, err := os.Lstat(filepath.Join(linkRoot, "sysinit.target.wants", unitName)); err != nil {
		t.Errorf("no symlink under the new target: %v", err)
	}
	// No file changed, so the manager must not be asked to reload.
	if strings.Join(manager.calls, " ") != "disable enable" {
		t.Errorf("call sequence = %v, want disable enable", manager.calls)
	}
}

// TestReenableSucceedsOnAUnitThatWasNeverEnabled proves a first install is not
// an error. Disabling a unit with no symlinks removes nothing and must not
// fail the run.
func TestReenableSucceedsOnAUnitThatWasNeverEnabled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const unitName = "mwan-agent.service"
	if err := os.WriteFile(filepath.Join(root, unitName), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatalf("write the unit: %v", err)
	}
	manager := &symlinkManager{root: root, wantedBy: "multi-user.target", calls: nil}

	if err := reenableUnits(t.Context(), manager, []string{unitName}, true); err != nil {
		t.Fatalf("reenableUnits on a unit that was never enabled: %v", err)
	}

	fresh := filepath.Join(root, "multi-user.target.wants", unitName)
	if _, err := os.Lstat(fresh); err != nil {
		t.Errorf("no symlink under the target: %v", err)
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

// TestInstallApplyWritesTheWanconfigAndHostFiles runs the verb for the wan
// role under a root and checks that each file the playbooks copy today lands
// at the host path the playbooks use, with their mode, holding the binary's
// bytes. The wan role also installs the schema into sysrepo, which binds a
// process to one repository, so the command runs in a child process; the
// child fails the test on a non-zero exit.
func TestInstallApplyWritesTheWanconfigAndHostFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	runInstallChild(t, root)

	wantFiles := map[string]string{
		"/etc/systemd/system/rousette.service":                         "rousette.service",
		"/etc/systemd/system/nghttpx-wanconfig.service":                "nghttpx-wanconfig.service",
		"/etc/systemd/system/nftables.service.d/override.conf":         "nftables-override.conf",
		"/etc/systemd/system/systemd-networkd.service.d/override.conf": "systemd-networkd-override.conf",
		"/etc/sysctl.d/99-quiet-console.conf":                          "99-quiet-console.conf",
	}
	for hostPath, embeddedName := range wantFiles {
		path := filepath.Join(root, hostPath)
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", hostPath, err)
			continue
		}
		embedded, err := unitFS.ReadFile(embeddedName)
		if err != nil {
			t.Errorf("read embedded %s: %v", embeddedName, err)
			continue
		}
		if !bytes.Equal(onDisk, embedded) {
			t.Errorf("%s on disk differs from the embedded %s", hostPath, embeddedName)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("stat %s: %v", hostPath, err)
			continue
		}
		if info.Mode().Perm() != systemdUnitMode {
			t.Errorf("%s mode = %v, want %v", hostPath, info.Mode().Perm(), systemdUnitMode)
		}
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
		switch unit {
		case "mwan-trace-boot.service":
			// The boot trace is a oneshot that has already exited by the time
			// anything inspects the running system, so it is not a unit the
			// debug view times.
			continue
		case "rousette.service", "nghttpx-wanconfig.service":
			// The wanconfig stack serves the management surface; it is not
			// on the forwarding path whose startup the debug view times.
			continue
		}
		if !focus[unit] {
			t.Errorf("the wan role enables %s, which the debug view does not name", unit)
		}
	}
}
