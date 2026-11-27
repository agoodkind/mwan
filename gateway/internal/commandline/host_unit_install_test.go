//go:build !systemd

package commandline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"

	"goodkind.io/mwan/gateway/installspec"
	"goodkind.io/mwan/gateway/yangpub/schema"
)

type recordingEnabler struct {
	calls   [][]string
	reloads []bool
}

func (r *recordingEnabler) enable(_ context.Context, units []string, reload bool) error {
	r.calls = append(r.calls, units)
	r.reloads = append(r.reloads, reload)

	return nil
}

func TestInstallUnitsWritesTheWanRoleUnits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}

	outcome, err := installUnits(t.Context(), installspec.RoleWAN, root, enabler.enable)
	if err != nil {
		t.Fatalf("installUnits: %v", err)
	}

	wantFiles := []string{
		"mwan-agent.service",
		"mwan-ifmgr@.service",
	}
	const wantChanged = 6
	if len(outcome.changed) != wantChanged {
		t.Fatalf("changed = %v, want %d files", outcome.changed, wantChanged)
	}

	for _, file := range wantFiles {
		path := filepath.Join(root, installspec.SystemdUnitDir, file)
		onDisk, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}

		embedded, embedErr := installspec.Read(file)
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

		if info.Mode().Perm() != installspec.FileMode {
			t.Fatalf("%s mode = %v, want %v", file, info.Mode().Perm(), installspec.FileMode)
		}
	}

	wantEnable := []string{
		"mwan-agent.service",
		"mwan-ifmgr@wan.service",
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

func TestInstallUnitsIsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}

	if _, err := installUnits(t.Context(), installspec.RoleWAN, root, enabler.enable); err != nil {
		t.Fatalf("first installUnits: %v", err)
	}

	unitPath := filepath.Join(root, installspec.SystemdUnitDir, "mwan-agent.service")
	before, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat after the first run: %v", err)
	}

	second, err := installUnits(t.Context(), installspec.RoleWAN, root, enabler.enable)
	if err != nil {
		t.Fatalf("second installUnits: %v", err)
	}

	if len(second.changed) != 0 {
		t.Fatalf("second run changed %v, want nothing", second.changed)
	}

	wanSpec, _ := installspec.For(installspec.RoleWAN)
	if strings.Join(second.enabled, " ") != strings.Join(wanSpec.Enable, " ") {
		t.Fatalf("second run enabled %v, want %v", second.enabled, wanSpec.Enable)
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

func TestInstallUnitsRewritesAChangedUnit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enabler := &recordingEnabler{}
	if _, err := installUnits(t.Context(), installspec.RoleWAN, root, enabler.enable); err != nil {
		t.Fatalf("first installUnits: %v", err)
	}

	unitPath := filepath.Join(root, installspec.SystemdUnitDir, "mwan-agent.service")
	if err := os.WriteFile(
		unitPath,
		[]byte("[Service]\nExecStart=/bin/false\n"),
		0o644,
	); err != nil {
		t.Fatalf("overwrite the unit: %v", err)
	}

	outcome, err := installUnits(t.Context(), installspec.RoleWAN, root, enabler.enable)
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

	embedded, err := installspec.Read("mwan-agent.service")
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

type symlinkManager struct {
	root     string
	wantedBy string
	calls    []string
}

func (m *symlinkManager) ReloadContext(_ context.Context) error {
	m.calls = append(m.calls, "reload")

	return nil
}

func (m *symlinkManager) DisableUnitFilesContext(
	_ context.Context,
	files []string,
	_ bool,
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
	_ context.Context,
	files []string,
	_ bool,
	_ bool,
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

func TestReenableRemovesASymlinkUnderTheOldTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const unitName = "mwan-ifmgr@wan.service"
	if err := os.WriteFile(filepath.Join(root, unitName), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatalf("write the unit: %v", err)
	}

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

func TestInstallReenablesWhenNoFileChanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	linkRoot := t.TempDir()
	manager := &symlinkManager{root: linkRoot, wantedBy: "sysinit.target", calls: nil}
	enabler := func(ctx context.Context, units []string, reload bool) error {
		return reenableUnits(ctx, manager, units, reload)
	}
	if _, err := installUnits(t.Context(), installspec.RoleWAN, root, enabler); err != nil {
		t.Fatalf("first installUnits: %v", err)
	}

	const unitName = "mwan-agent.service"
	staleDir := filepath.Join(linkRoot, "multi-user.target.wants")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("create the old target directory: %v", err)
	}

	stale := filepath.Join(staleDir, unitName)
	if err := os.Symlink(filepath.Join(linkRoot, unitName), stale); err != nil {
		t.Fatalf("create the stale symlink: %v", err)
	}

	manager.calls = nil

	outcome, err := installUnits(t.Context(), installspec.RoleWAN, root, enabler)
	if err != nil {
		t.Fatalf("second installUnits: %v", err)
	}

	if len(outcome.changed) != 0 {
		t.Fatalf("second run changed %v, want nothing", outcome.changed)
	}

	wanSpec, _ := installspec.For(installspec.RoleWAN)
	if strings.Join(outcome.enabled, " ") != strings.Join(wanSpec.Enable, " ") {
		t.Fatalf("second run enabled %v, want %v", outcome.enabled, wanSpec.Enable)
	}

	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("the symlink under the old target survived (err %v)", err)
	}

	if _, err := os.Lstat(filepath.Join(linkRoot, "sysinit.target.wants", unitName)); err != nil {
		t.Errorf("no symlink under the new target: %v", err)
	}

	if strings.Join(manager.calls, " ") != "disable enable" {
		t.Errorf("call sequence = %v, want disable enable", manager.calls)
	}
}

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

func TestInstallWithoutApplyWritesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	output := runChild(t, []string{childMainEnv + "=1"}, "install", "--role", "wan", "--root", root)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read the root: %v", err)
	}

	if len(entries) != 0 {
		t.Fatalf("a run without --apply created %d entries under the root", len(entries))
	}

	const rousetteDir = "/opt/mwan-wanconfig/share/yang/modules/rousette/"
	wantLine := "installs " + strings.Join(
		[]string{
			rousetteDir + "ietf-restconf@2017-01-26.yang",
			rousetteDir + "ietf-restconf-monitoring@2017-01-26.yang",
			rousetteDir + "ietf-yang-patch@2017-02-22.yang",
			rousetteDir + "ietf-subscribed-notifications@2019-09-09.yang",
			rousetteDir + "ietf-restconf-subscribed-notifications@2019-11-17.yang",
		},
		" ",
	) + "\n"
	if !strings.Contains(output, wantLine) {
		t.Errorf("run output:\n%s\nwant a line %q", output, wantLine)
	}

	if count := strings.Count(output, rousetteDir+"ietf-restconf@"); count != 1 {
		t.Errorf("run output:\n%s\nlines with %s = %d, want 1", output, rousetteDir, count)
	}
}

func TestInstallApplyFailsWithoutTheStackModules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	command := exec.Command(os.Args[0], "install", "--apply", "--role", "wan", "--root", root)
	command.Env = append(os.Environ(), childMainEnv+"=1")

	output, err := command.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitInstallFailed {
		t.Fatalf("err = %v, want exit code %d\n%s", err, exitInstallFailed, output)
	}

	if !strings.Contains(string(output), "ietf-restconf@2017-01-26.yang") {
		t.Errorf("run output:\n%s\nwant %s", output, "ietf-restconf@2017-01-26.yang")
	}
}

func TestInstallApplyUnderARootSkipsSystemd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	output := runInstallChild(t, root)
	if !strings.Contains(output, "would enable mwan-agent.service") {
		t.Fatalf("rooted install output = %q, want planned unit enablement", output)
	}

	unitPath := filepath.Join(root, installspec.SystemdUnitDir, "mwan-ifmgr@.service")
	onDisk, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read %s: %v", unitPath, err)
	}

	embedded, err := installspec.Read("mwan-ifmgr@.service")
	if err != nil {
		t.Fatalf("read the embedded unit: %v", err)
	}

	if !bytes.Equal(onDisk, embedded) {
		t.Fatal("the rooted run did not write the embedded bytes")
	}
}

func TestInstallApplyWritesTheWanconfigAndHostFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	runInstallChild(t, root)

	wantFiles := map[string]string{
		"/etc/systemd/system/rousette.service":                       "rousette.service",
		"/etc/systemd/system/nghttpx-wanconfig.service":              "nghttpx-wanconfig.service",
		"/etc/systemd/system/mwan-ifmgr@wan.service.d/firewall.conf": "mwan-ifmgr-wan.conf",
		"/etc/sysctl.d/99-quiet-console.conf":                        "99-quiet-console.conf",
	}
	for hostPath, embeddedName := range wantFiles {
		path := filepath.Join(root, hostPath)
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", hostPath, err)

			continue
		}

		embedded, err := installspec.Read(embeddedName)
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

		if info.Mode().Perm() != installspec.FileMode {
			t.Errorf("%s mode = %v, want %v", hostPath, info.Mode().Perm(), installspec.FileMode)
		}
	}
}

// Run the installer in a child process. The test rejects a path that resolves to the
// host root.
func TestInstallApplyRejectsARootThatIsTheHost(t *testing.T) {
	t.Parallel()
	hostLink := filepath.Join(t.TempDir(), "host")
	if err := os.Symlink("/", hostLink); err != nil {
		t.Fatalf("link to the host root: %v", err)
	}

	// Include a missing child and .. to test path cleanup before symlink resolution.
	for _, root := range []string{"/", "//", "/etc/..", hostLink, hostLink + "/missing/.."} {
		command := exec.Command(os.Args[0], "install", "--apply", "--role", "wan", "--root", root)
		command.Env = append(os.Environ(), childMainEnv+"=1")
		output, err := command.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitInstallUsage {
			t.Errorf(
				"--root %s: err = %v, want exit code %d\n%s",
				root,
				err,
				exitInstallUsage,
				output,
			)
		}
	}
}

func TestInstallApplyNeedsARole(t *testing.T) {
	t.Parallel()

	_, code := runMwanBinary(
		t,
		BuildTestBinary(t),
		t.TempDir(),
		"",
		"install",
		"--apply",
		"--root",
		t.TempDir(),
	)

	if code != exitInstallUsage {
		t.Fatalf("exit code = %d, want %d", code, exitInstallUsage)
	}
}

func TestInstallRejectsAnUnknownRole(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	_, code := runMwanBinary(
		t,
		BuildTestBinary(t),
		t.TempDir(),
		"",
		"install",
		"--apply",
		"--role",
		"gateway",
		"--root",
		root,
	)

	if code != exitInstallUsage {
		t.Fatalf("exit code = %d, want %d", code, exitInstallUsage)
	}

	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("an unknown role wrote %d entries (err %v)", len(entries), err)
	}
}

// Sysrepo uses the filename to resolve a schema's module and revision.
func TestInstallPrintSchemaWritesEveryModule(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "schema")

	_, code := runMwanBinary(
		t,
		BuildTestBinary(t),
		t.TempDir(),
		"",
		"install",
		"--print-schema",
		dir,
	)

	if code != exitInstallOK {
		t.Fatalf("exit code = %d, want %d", code, exitInstallOK)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	modules := schema.Modules()
	if len(entries) != len(modules) {
		t.Fatalf("wrote %d files, want %d", len(entries), len(modules))
	}

	for _, module := range modules {
		path := filepath.Join(dir, module.File)
		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		wantName, wantRevision, _ := strings.Cut(strings.TrimSuffix(module.File, ".yang"), "@")

		if !bytes.Contains(written, []byte("module "+wantName+" {")) {
			t.Errorf("%s does not declare module %s", module.File, wantName)
		}

		if !bytes.Contains(written, []byte("revision "+wantRevision)) {
			t.Errorf("%s does not carry revision %s", module.File, wantRevision)
		}
	}
}
