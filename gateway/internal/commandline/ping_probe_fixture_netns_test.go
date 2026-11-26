//go:build linux && (protocol || systemd)

package commandline_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	debugProbeNamespaceChild  = "MWAN_DEBUG_PROBE_TEST_CHILD"
	debugProbeNamespaceBinary = "MWAN_DEBUG_PROBE_TEST_BINARY"
)

func runDebugProbeIsolatedTest(t *testing.T, run func(*testing.T, string), cloneFlags uintptr) {
	t.Helper()
	if os.Getenv(debugProbeNamespaceChild) == t.Name() {
		if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
			t.Fatal(err)
		}
		run(t, os.Getenv(debugProbeNamespaceBinary))
		return
	}
	binary, supplied := os.LookupEnv("MWAN_PROTOCOL_TEST_BINARY")
	if supplied {
		if !filepath.IsAbs(binary) {
			t.Fatalf("MWAN_PROTOCOL_TEST_BINARY must be an absolute path: %q", binary)
		}
		info, err := os.Stat(binary)
		if err != nil {
			t.Fatalf("inspect MWAN_PROTOCOL_TEST_BINARY: %v", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("MWAN_PROTOCOL_TEST_BINARY must be a regular executable file: %q", binary)
		}
	}
	if os.Geteuid() != 0 {
		if supplied || os.Getenv("MWAN_PROTOCOL_ACCEPTANCE") == "1" {
			t.Fatal("explicit debug validation requires root for network and mount namespaces")
		}
		t.Skip("network and mount namespaces require root")
	}
	if !supplied {
		binary = testMwanBinary(t)
	}
	t.Logf("public debug executable: %s", binary)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	child.Env = append(
		os.Environ(),
		debugProbeNamespaceChild+"="+t.Name(),
		debugProbeNamespaceBinary+"="+binary,
	)
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: cloneFlags}
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated public debug command: %v: %s", err, output)
	}
}

func bindDebugProbePath(t *testing.T, source, target string) {
	t.Helper()
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
}

func installDebugProbeNetwork(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	retained := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if bytes.Contains(entry, []byte(`"goodkind-mwan-steering:wan"`)) &&
			!bytes.Contains(entry, []byte(`"name": "att"`)) {
			continue
		}
		retained = append(retained, bytes.ReplaceAll(entry, []byte("enatt0"), []byte("lo")))
	}
	interfaces["interface"], err = json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, root, "/etc/mwan")
	schema, err := filepath.Abs(filepath.Join("..", "..", "yangpub", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, schema, "/usr/local/share/wanconfig/yang")
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return config
}
