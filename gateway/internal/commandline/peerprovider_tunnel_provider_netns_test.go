//go:build linux && firewall

package commandline

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTunnelProviderDaemonRuntime(t *testing.T) {
	if os.Getenv(tunnelRuntimeChildEnv) == "1" {
		runTunnelProviderDaemonRuntime(t)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^TestTunnelProviderDaemonRuntime$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
	}
	child.Env = append(os.Environ(), tunnelRuntimeChildEnv+"=1", tunnelRuntimeBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated tunnel provider daemon test: %v: %s", err, output)
	}
}
