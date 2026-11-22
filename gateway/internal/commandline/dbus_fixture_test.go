//go:build linux && systemd

package commandline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	systemdImageMarker      = "/etc/mwan-systemd-test-image"
	systemdCommandTimeout   = 15 * time.Second
	systemdCommandWaitDelay = 2 * time.Second
)

func RequireSystemd(t *testing.T) {
	t.Helper()
	marker, err := os.Lstat(systemdImageMarker)
	if err != nil {
		t.Fatalf("systemd tests require the dedicated image marker %s: %v", systemdImageMarker, err)
	}
	if !marker.Mode().IsRegular() {
		t.Fatalf("systemd test image marker %s must be a regular file", systemdImageMarker)
	}

	if os.Geteuid() != 0 {
		t.Fatal("systemd tests require root in the dedicated systemd test container")
	}

	initName, err := os.ReadFile("/proc/1/comm")
	if err != nil {
		t.Fatalf("read systemd test container PID 1: %v", err)
	}
	if strings.TrimSpace(string(initName)) != "systemd" {
		t.Fatalf(
			"systemd tests require systemd as PID 1 in the dedicated test container, found %q",
			strings.TrimSpace(string(initName)),
		)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "systemctl", "show", "--property=Version", "--value")
	command.WaitDelay = systemdCommandWaitDelay
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("connect to systemd in the test container: %v: %s", err, output)
	}
}

func SystemdFixtureOutput(name string, arguments ...string) ([]byte, error) {
	// Cleanup may run after the test context ends.
	ctx, cancel := context.WithTimeout(context.Background(), systemdCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, arguments...)
	command.WaitDelay = systemdCommandWaitDelay
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return output, fmt.Errorf("%s: %w", name, ctx.Err())
	}

	return output, err
}

func requireSystemdOutput(t *testing.T, name string, arguments ...string) string {
	t.Helper()
	output, err := SystemdFixtureOutput(name, arguments...)
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, arguments, err, output)
	}

	return string(output)
}

func removeSystemdPeerLinks(t *testing.T, namespace netns.NsHandle, names []string) {
	t.Helper()
	handle, err := netlink.NewHandleAt(namespace)
	if err != nil {
		t.Errorf("open fixture network namespace for cleanup: %v", err)
		return
	}
	defer handle.Close()

	for _, name := range names {
		link, err := handle.LinkByName(name)
		if _, gone := errors.AsType[netlink.LinkNotFoundError](err); gone {
			continue
		}
		if err != nil {
			t.Errorf("find fixture link %s for cleanup: %v", name, err)
			continue
		}
		if err := handle.LinkDel(link); err != nil && !errors.Is(err, unix.ENODEV) {
			t.Errorf("remove fixture link %s: %v", name, err)
		}
	}
}
