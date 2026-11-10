//go:build linux && netns

package netif_test

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/netif"
)

const (
	forwardPathUIDNamespaceChild  = "MWAN_FORWARD_PATH_UID_NAMESPACE_CHILD"
	forwardPathMappedKernelUID    = 65536
	forwardPathAmbiguousKernelUID = 1000
	forwardPathNonrootHostUID     = 1
	forwardPathLXCUIDBase         = 100000
	forwardPathLXCUIDCount        = 65536
	forwardPathPacketUIDPriority  = forwardPathRootUIDPriority - 1
)

func TestForwardPathLookupIPv6InputUIDNamespaces(t *testing.T) {
	mode := os.Getenv(forwardPathUIDNamespaceChild)
	if mode != "" {
		runForwardPathUIDNamespace(t, mode)
		return
	}
	binary := copyForwardPathUIDTestBinary(t)
	for _, current := range []string{"mapped", "ambiguous", "unmapped"} {
		t.Run(current, func(t *testing.T) {
			var attributes syscall.SysProcAttr
			attributes.Cloneflags = unix.CLONE_NEWUSER | unix.CLONE_NEWNET
			attributes.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: forwardPathNonrootHostUID, Size: 1}}
			if current == "mapped" {
				attributes.UidMappings = append(attributes.UidMappings, syscall.SysProcIDMap{ContainerID: forwardPathMappedKernelUID, HostID: 0, Size: 1})
			} else if current == "ambiguous" {
				attributes.UidMappings = append(attributes.UidMappings, syscall.SysProcIDMap{ContainerID: forwardPathAmbiguousKernelUID, HostID: 0, Size: 1})
			} else {
				attributes.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: forwardPathLXCUIDBase, Size: forwardPathLXCUIDCount}}
			}
			attributes.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: forwardPathNonrootHostUID, Size: 1}}
			var user syscall.Credential
			user.Uid, user.Gid = 0, 0
			user.NoSetGroups = true
			attributes.Credential = &user
			arguments := []string{"-test.run=^TestForwardPathLookupIPv6InputUIDNamespaces$", "-test.v"}
			if deadline, bounded := t.Deadline(); bounded {
				remaining := deadline.Sub(clock.Real{}.Now())
				if remaining <= 0 {
					t.Fatal("user namespace test deadline expired before child startup")
				}
				arguments = append(arguments, "-test.timeout="+remaining.String())
			}
			command := exec.CommandContext(t.Context(), binary, arguments...)
			command.SysProcAttr = &attributes
			command.Env = append(os.Environ(), forwardPathUIDNamespaceChild+"="+current)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("IPv6 input UID %s namespace: %v: %s", current, err, output)
			}
			t.Log(string(output))
		})
	}
}

func runForwardPathUIDNamespace(t *testing.T, mode string) {
	t.Helper()
	if mode != "mapped" && mode != "ambiguous" && mode != "unmapped" {
		t.Fatalf("unknown input UID namespace mode %q", mode)
	}
	if os.Getuid() != 0 {
		t.Fatalf("namespace caller UID=%d, expected its local root UID", os.Getuid())
	}
	mapping, err := os.ReadFile("/proc/thread-self/uid_map")
	if err != nil {
		t.Fatalf("inspect input UID namespace mapping: %v", err)
	}
	t.Logf("input UID namespace: mode=%s caller_uid=%d uid_map=%s", mode, os.Getuid(), mapping)
	fixture := newForwardPathFixture(t, unix.AF_INET6, "um")
	fixture.selector.Mark = 0
	forwarding := netlink.NewRule()
	forwarding.Family, forwarding.Priority, forwarding.Table = unix.AF_INET6, forwardPathRootUIDPriority+1, forwardPathTable
	forwarding.IifName = fixture.input.Attrs().Name
	if err := netlink.RuleAdd(forwarding); err != nil {
		t.Fatalf("install user namespace forwarding policy: %v", err)
	}
	endpoints := isolateForwardPathEndpoints(t, fixture)
	requireForwardPathPacket(t, fixture, endpoints, 1, "uid-independent-policy")
	caller := netlink.NewRule()
	caller.Family, caller.Priority, caller.Table = unix.AF_INET6, forwardPathRootUIDPriority, unix.RT_TABLE_MAIN
	caller.UIDRange = &netlink.RuleUIDRange{Start: 0, End: 0}
	if err := netlink.RuleAdd(caller); err != nil {
		t.Fatalf("install caller UID policy: %v", err)
	}
	if mode != "mapped" {
		path, usable, queryErr := netif.ForwardPathLookup(t.Context(), slog.Default(), fixture.selector)
		var requirement *netif.EgressObservationRequiredError
		if usable || path.Link != nil || !errors.As(queryErr, &requirement) || requirement.Reason != netif.EgressObservationUIDMapping {
			t.Fatalf("%s kernel UID did not require egress observation: usable=%v path=%+v error=%v", mode, usable, path, queryErr)
		}
		observed := observeForwardPathPacket(t, fixture, endpoints, "unmapped-kernel-uid")
		if observed.output != 1 || observed.hopLimit != forwardPathInitialHopLimit-1 {
			t.Fatalf("kernel forwarding failed while UID certification was unavailable: %+v", observed)
		}
		t.Logf("%s input UID: query rejected certification, wire=%+v error=%v", mode, observed, queryErr)
		return
	}
	requireForwardPathPacket(t, fixture, endpoints, 1, "caller-uid-rule")
	input := netlink.NewRule()
	input.Family, input.Priority, input.Table = unix.AF_INET6, forwardPathPacketUIDPriority, unix.RT_TABLE_MAIN
	input.UIDRange = &netlink.RuleUIDRange{Start: forwardPathMappedKernelUID, End: forwardPathMappedKernelUID}
	if err := netlink.RuleAdd(input); err != nil {
		t.Fatalf("install translated packet UID policy: %v", err)
	}
	requireForwardPathPacket(t, fixture, endpoints, 0, "translated-packet-uid-rule")
}

func copyForwardPathUIDTestBinary(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "mwan-forward-uid-")
	if err != nil {
		t.Fatalf("create user namespace executable directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove user namespace executable directory: %v", err)
		}
	})
	// The child maps local UID zero to a different host UID.
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("permit mapped UID access to the test executable directory: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("inspect the user namespace test executable: %v", err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatalf("open the user namespace test executable: %v", err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			t.Errorf("close the source test executable: %v", err)
		}
	}()
	path := filepath.Join(directory, "netif.test")
	destination, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatalf("create the mapped UID test executable: %v", err)
	}
	_, copyErr := io.Copy(destination, source)
	if err := errors.Join(copyErr, destination.Close()); err != nil {
		t.Fatalf("copy the mapped UID test executable: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("permit mapped UID execution of the test binary: %v", err)
	}
	return path
}
