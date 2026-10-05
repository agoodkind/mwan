//go:build linux

package netif

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func dhcpRestartNamespace(t *testing.T) bool {
	t.Helper()
	const childEnvironment = "MWAN_DHCP_RESTART_NAMESPACE_CHILD"
	if os.Getenv(childEnvironment) == "1" {
		return true
	}
	if os.Geteuid() != 0 {
		t.Skip("raw DHCP transport and network namespaces require root")
	}
	// Host link policies can replace a veth MAC after its first DHCP exchange.
	child := exec.Command(os.Args[0], "-test.v", "-test.run=^TestDHCPRestartClientWithServer$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), childEnvironment+"=1")
	output, err := child.CombinedOutput()
	if errors.Is(err, syscall.EPERM) {
		t.Skipf("network namespace creation unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("isolated DHCP restart test: %v: %s", err, output)
	}
	t.Logf("isolated DHCP restart result: %s", output)
	return false
}
