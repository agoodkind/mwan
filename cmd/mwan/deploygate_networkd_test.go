package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkd"
	"goodkind.io/mwan/internal/networkjson"
)

func runCheckNetworkdCommand(t *testing.T, arguments ...string) (int, string) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"deploy-gate", "check-networkd"}, arguments...)...)
	command.Env = append(os.Environ(), childMainEnv+"=1")
	output, err := command.CombinedOutput()
	if err == nil {
		return exitDeployGateOK, string(output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run check-networkd: %v\n%s", err, output)
	}
	return exitError.ExitCode(), string(output)
}

type checkedUnitState struct {
	Mode     fs.FileMode
	Modified time.Time
	Content  string
}

func checkedUnitDirectory(t *testing.T, directory string) map[string]checkedUnitState {
	t.Helper()
	states := map[string]checkedUnitState{}
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := checkedUnitState{Mode: info.Mode(), Modified: info.ModTime(), Content: ""}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.Content = string(content)
		}
		states[path] = state
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return states
}

func renderedNetworkdFixture(t *testing.T) (string, string) {
	t.Helper()
	body, err := os.ReadFile("../../yang/instances/network-freeform.json")
	if err != nil {
		t.Fatal(err)
	}
	parent := `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`
	vlan := `{"name":"provider397","type":"iana-if-type:l2vlan","goodkind-mwan-steering:link-files":"rendered","goodkind-mwan-steering:link":{"vlan":{"parent":"enwebpass0","id":397}}},`
	document := strings.Replace(string(body), parent, vlan+parent, 1)
	if document == string(body) {
		t.Fatal("fixture lacks internal interface")
	}
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := networkjson.Load(path, networkSchemaDirForTest(t))
	if err != nil || len(loaded.Rejected) != 0 {
		t.Fatalf("load fixture: %v; rejected=%v", err, loaded)
	}
	tables := make(map[connectionid.ID]int, len(loaded.WAN))
	for id, provider := range loaded.WAN {
		tables[connectionid.ID(id)] = provider.TableID
	}
	directory := t.TempDir()
	if _, err := networkd.WriteDir(directory, loaded.Connections, tables); err != nil {
		t.Fatal(err)
	}
	return path, directory
}

func TestCheckNetworkdCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		modify func(*testing.T, string)
		want   int
		report string
	}{
		{"real render with foreign units", func(t *testing.T, dir string) {
			writeCheckedUnit(t, dir, "10-att.network", "# Hand authored\n[Match]\nName=enatt0\n")
			writeCheckedUnit(t, dir, "10-legacy.network", networkd.Marker+" foreign\n[Match]\nName=legacy\n")
		}, exitDeployGateOK, "networkd units match configured intent"},
		{"missing expected unit", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "20-enwebpass0.link")); err != nil {
				t.Fatal(err)
			}
		}, exitDeployGateFailed, "20-enwebpass0.link"},
		{"corrupt expected unit", func(t *testing.T, dir string) {
			writeCheckedUnit(t, dir, "20-enwebpass0.network", "[Match]\nName=wrong\n")
		}, exitDeployGateFailed, "20-enwebpass0.network: content differs"},
		{"missing VLAN parent reference", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "20-enwebpass0.network")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(body), "VLAN=provider397\n", "", 1)
			if changed == string(body) {
				t.Fatal("parent unit lacks VLAN reference")
			}
			writeCheckedUnit(t, dir, filepath.Base(path), changed)
		}, exitDeployGateFailed, "20-enwebpass0.network: content differs"},
		{"nonregular expected unit", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "20-provider397.netdev")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, exitDeployGateFailed, "20-provider397.netdev: expected a regular unit file"},
		{"stale marked unit", func(t *testing.T, dir string) {
			writeCheckedUnit(t, dir, "20-retired.network", networkd.Marker)
		}, exitDeployGateFailed, "20-retired.network: stale generated unit"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			document, directory := renderedNetworkdFixture(t)
			testCase.modify(t, directory)
			before := checkedUnitDirectory(t, directory)
			code, report := runCheckNetworkdCommand(t, document, networkSchemaDirForTest(t), directory)
			if code != testCase.want || !strings.Contains(report, testCase.report) {
				t.Fatalf("check-networkd exit=%d, want=%d; report must include %q:\n%s", code, testCase.want, testCase.report, report)
			}
			if after := checkedUnitDirectory(t, directory); !reflect.DeepEqual(before, after) {
				t.Fatalf("check-networkd changed unit files: before=%v after=%v", before, after)
			}
			t.Logf("exit=%d report=%s", code, report)
		})
	}
	for _, count := range []int{0, 2, 4} {
		arguments := make([]string, count)
		code, report := runCheckNetworkdCommand(t, arguments...)
		if code != exitDeployGateUsage {
			t.Fatalf("argument count %d: exit=%d; %s", count, code, report)
		}
	}
	directory := t.TempDir()
	before := checkedUnitDirectory(t, directory)
	code, report := runCheckNetworkdCommand(t, webpassIPv6NoDHCP(t), networkSchemaDirForTest(t), directory)
	if code != exitDeployGateFailed || !strings.Contains(report, "provider entry rejected: interface enwebpass0") {
		t.Fatalf("partial rejected provider: exit=%d; %s", code, report)
	}
	if !reflect.DeepEqual(before, checkedUnitDirectory(t, directory)) {
		t.Fatal("rejected document changed unit files")
	}
}

func writeCheckedUnit(t *testing.T, directory string, name string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
