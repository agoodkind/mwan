//go:build linux && netns

package slaachealth_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/slaachealth"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
)

func TestReconcileRestoresIPv6AfterCancellation(t *testing.T) {
	const childEnvironment = "MWAN_SLAAC_CANCEL_CHILD"
	if os.Getenv(childEnvironment) != "1" {
		child := exec.Command(os.Args[0], "-test.run=^TestReconcileRestoresIPv6AfterCancellation$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnvironment+"=1")
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("run isolated SLAAC recovery: %v: %s", err, output)
		}
		return
	}

	const iface = "slaac-test0"
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: iface}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("create isolated interface: %v", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("enable isolated interface: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	module, err := slaachealth.New(slaachealth.Config{
		Iface:             iface,
		AlertAfter:        time.Hour,
		MaxTogglesPerHour: 1,
	})
	if err != nil {
		t.Fatalf("create SLAAC health module: %v", err)
	}
	env := &ifmgr.Env{
		Log:    log,
		Sysctl: netif.NewProcSysctlRunner(log, false),
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(notify.EmailConfig{}, notify.Config{}, log, "mwan-ifmgr")),
	}
	if err := module.Init(context.Background(), env); err != nil {
		t.Fatalf("initialize SLAAC health module: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- module.Reconcile(ctx, log) }()

	const sysctlPath = "/proc/sys/net/ipv6/conf/" + iface + "/disable_ipv6"
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	disabled := false
	for !disabled {
		select {
		case <-ticker.C:
			value, readErr := os.ReadFile(sysctlPath)
			if readErr != nil {
				t.Fatalf("read isolated IPv6 setting: %v", readErr)
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("1")) {
				cancel()
				disabled = true
			}
		case reconcileErr := <-completed:
			t.Fatalf("SLAAC recovery finished before IPv6 was disabled: %v", reconcileErr)
		case <-deadline.C:
			t.Fatal("SLAAC recovery did not disable IPv6")
		}
	}
	select {
	case reconcileErr := <-completed:
		if reconcileErr != nil {
			t.Fatalf("complete canceled SLAAC recovery: %v", reconcileErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled SLAAC recovery did not finish")
	}
	value, err := os.ReadFile(sysctlPath)
	if err != nil {
		t.Fatalf("read restored IPv6 setting: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(value), []byte("0")) {
		t.Fatalf("IPv6 remained disabled after cancellation: %s", value)
	}
}
