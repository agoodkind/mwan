package networkd_test

import (
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/mwan/internal/networkd"
)

const systemBusAddressEnv = "DBUS_SYSTEM_BUS_ADDRESS"

// An absent system bus socket is the early-boot state before dbus starts.
// networkd is not running then and reads the unit files when it starts.
func TestReloadIfRunningTreatsAbsentSystemBusAsNotRunning(t *testing.T) {
	missingSocket := filepath.Join(t.TempDir(), "system_bus_socket")
	t.Setenv(systemBusAddressEnv, "unix:path="+missingSocket)

	if err := networkd.ReloadIfRunning(t.Context()); err != nil {
		t.Fatalf("ReloadIfRunning with an absent system bus socket returned %v; want nil", err)
	}
}

// A path that exists but accepts no connection is a real connection failure.
func TestReloadIfRunningReturnsErrorForOtherConnectionFailures(t *testing.T) {
	notASocket := filepath.Join(t.TempDir(), "system_bus_socket")
	if err := os.WriteFile(notASocket, nil, 0o600); err != nil {
		t.Fatalf("create regular file: %v", err)
	}
	t.Setenv(systemBusAddressEnv, "unix:path="+notASocket)

	if err := networkd.ReloadIfRunning(t.Context()); err == nil {
		t.Fatal("ReloadIfRunning with a non-socket path returned nil; want an error")
	}
}
