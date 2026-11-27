//go:build linux && firewall

package firewall_test

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"goodkind.io/mwan/gateway/integration/internal/testenv"

	"golang.org/x/sys/unix"
)

func TestTunnelProviderDaemonRuntime(t *testing.T) {
	if os.Getenv(testenv.TunnelRuntimeChildEnv) == "1" {
		testenv.RunTunnelProviderDaemonRuntime(t)
		return
	}
	t.Parallel()
	binary := testenv.ProtocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestTunnelProviderDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(
		os.Environ(),
		testenv.TunnelRuntimeChildEnv+"=1",
		testenv.TunnelRuntimeBinaryEnv+"="+binary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated tunnel provider daemon test: %v: %s", err, output)
	}
}
