//go:build linux && cgo && vpp_integration

package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/networkload"
)

func TestInstallRouterProvidesUsableNetworkSchema(t *testing.T) {
	binary, supplied := os.LookupEnv("MWAN_PROTOCOL_TEST_BINARY")
	if !supplied || !filepath.IsAbs(binary) {
		t.Fatal("set MWAN_PROTOCOL_TEST_BINARY to the absolute path of the built mwan command")
	}
	status, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("inspect MWAN_PROTOCOL_TEST_BINARY: %v", err)
	}
	if !status.Mode().IsRegular() || status.Mode().Perm()&0o111 == 0 {
		t.Fatalf("MWAN_PROTOCOL_TEST_BINARY must be a regular executable file: %q", binary)
	}
	root := t.TempDir()
	command := exec.CommandContext(t.Context(), binary, "install", "--role", "router", "--apply", "--root", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install router files with the built mwan command: %v\n%s", err, output)
	}
	policy, err := networkload.Load(filepath.Join("..", "..", "internal", "networkjson", "testdata", "vpp-router.json"), filepath.Join(root, networkjson.DefaultSchemaDir))
	if err != nil {
		t.Fatalf("load router policy with installed schema: %v", err)
	}
	if policy.VPP == nil || policy.VPP.NAT64 == nil {
		t.Fatal("installed router schema did not produce NAT64 policy")
	}
}
