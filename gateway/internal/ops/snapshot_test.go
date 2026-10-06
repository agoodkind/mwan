package ops_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ops"
)

const (
	testVMID       = "123"
	testSnapshot   = "known-good"
	scopeArgPrefix = "--scope\n--quiet\n--collect\n"
)

func installFakeCommand(t *testing.T, name, stdout string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, name+".args")
	script := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\nprintf '%%s' '%s'\nexit %d\n",
		argsFile, stdout, exitCode,
	)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func installFakeScopeRunner(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "systemd-run.args")
	script := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\nshift 3\nexec \"$@\"\n", argsFile,
	)
	if err := os.WriteFile(filepath.Join(dir, "systemd-run"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func newGuestOps(guestType config.GuestType) *ops.RealOps {
	var cfg config.Config
	cfg.GuestType = guestType
	return ops.NewRealOps(&cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func readRecordedArgs(t *testing.T, path string) string {
	t.Helper()
	recorded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("command was not run: %v", err)
	}
	return string(recorded)
}

func TestGuestOperationsRunTheDriverCommand(t *testing.T) {
	cases := []struct {
		name     string
		call     func(*ops.RealOps) error
		detached bool
		qemuArgs string
		lxcArgs  string
	}{
		{
			name: "snapshot",
			call: func(r *ops.RealOps) error {
				return r.VMSnapshot(context.Background(), testVMID, testSnapshot)
			},
			detached: true,
			qemuArgs: "snapshot\n123\nknown-good\n",
			lxcArgs:  "snapshot\n123\nknown-good\n",
		},
		{
			name: "delete snapshot",
			call: func(r *ops.RealOps) error {
				return r.VMDelSnapshot(context.Background(), testVMID, testSnapshot)
			},
			detached: true,
			qemuArgs: "delsnapshot\n123\nknown-good\n",
			lxcArgs:  "delsnapshot\n123\nknown-good\n",
		},
		{
			name: "forced delete snapshot",
			call: func(r *ops.RealOps) error {
				return r.VMDelSnapshotForce(context.Background(), testVMID, testSnapshot)
			},
			detached: true,
			qemuArgs: "delsnapshot\n123\nknown-good\n--force\n",
			lxcArgs:  "delsnapshot\n123\nknown-good\n--force\n",
		},
		{
			name: "rollback",
			call: func(r *ops.RealOps) error {
				return r.VMRollback(context.Background(), testVMID, testSnapshot)
			},
			detached: true,
			qemuArgs: "rollback\n123\nknown-good\n",
			lxcArgs:  "rollback\n123\nknown-good\n",
		},
		{
			name: "stop",
			call: func(r *ops.RealOps) error {
				return r.VMStop(context.Background(), testVMID)
			},
			detached: false,
			qemuArgs: "stop\n123\n--timeout\n30\n",
			lxcArgs:  "stop\n123\n",
		},
		{
			name: "start",
			call: func(r *ops.RealOps) error {
				return r.VMStart(context.Background(), testVMID)
			},
			detached: false,
			qemuArgs: "start\n123\n",
			lxcArgs:  "start\n123\n",
		},
		{
			name: "unlock",
			call: func(r *ops.RealOps) error {
				return r.VMUnlock(context.Background(), testVMID)
			},
			detached: false,
			qemuArgs: "unlock\n123\n",
			lxcArgs:  "unlock\n123\n",
		},
	}
	guests := []struct {
		guestType config.GuestType
		binary    string
		wantArgs  func(qemuArgs, lxcArgs string) string
	}{
		{
			guestType: config.GuestTypeQEMU,
			binary:    "qm",
			wantArgs:  func(qemuArgs, _ string) string { return qemuArgs },
		},
		{
			guestType: config.GuestTypeLXC,
			binary:    "pct",
			wantArgs:  func(_, lxcArgs string) string { return lxcArgs },
		},
	}
	for _, guest := range guests {
		for _, tc := range cases {
			t.Run(string(guest.guestType)+" "+tc.name, func(t *testing.T) {
				scopeArgsFile := installFakeScopeRunner(t)
				toolArgsFile := installFakeCommand(t, guest.binary, "", 0)

				if err := tc.call(newGuestOps(guest.guestType)); err != nil {
					t.Fatalf("operation failed: %v", err)
				}

				wantArgs := guest.wantArgs(tc.qemuArgs, tc.lxcArgs)
				if got := readRecordedArgs(t, toolArgsFile); got != wantArgs {
					t.Fatalf("%s args = %q, want %q", guest.binary, got, wantArgs)
				}
				if !tc.detached {
					return
				}
				wantScope := scopeArgPrefix + guest.binary + "\n" + wantArgs
				if got := readRecordedArgs(t, scopeArgsFile); got != wantScope {
					t.Fatalf("systemd-run args = %q, want %q", got, wantScope)
				}
			})
		}
	}
}

func TestGuestQueriesRunTheDriverCommand(t *testing.T) {
	for _, guest := range []struct {
		guestType config.GuestType
		binary    string
	}{
		{guestType: config.GuestTypeQEMU, binary: "qm"},
		{guestType: config.GuestTypeLXC, binary: "pct"},
	} {
		t.Run(string(guest.guestType), func(t *testing.T) {
			realOps := newGuestOps(guest.guestType)
			ctx := context.Background()

			statusArgs := installFakeCommand(t, guest.binary, "status: running\n", 0)
			running, err := realOps.VMStatus(ctx, testVMID)
			if err != nil || !running {
				t.Fatalf("VMStatus = %v, %v, want true, nil", running, err)
			}
			if got := readRecordedArgs(t, statusArgs); got != "status\n123\n" {
				t.Fatalf("status args = %q", got)
			}

			listArgs := installFakeCommand(t, guest.binary, "`-> known-good\n", 0)
			listing, err := realOps.VMSnapshots(ctx, testVMID)
			if err != nil || !strings.Contains(string(listing), "known-good") {
				t.Fatalf("VMSnapshots = %q, %v", listing, err)
			}
			if got := readRecordedArgs(t, listArgs); got != "listsnapshot\n123\n" {
				t.Fatalf("listsnapshot args = %q", got)
			}

			configArgs := installFakeCommand(t, guest.binary, "memory: 512\nlock: snapshot\n", 0)
			lock, err := realOps.VMLock(ctx, testVMID)
			if err != nil || lock != "snapshot" {
				t.Fatalf("VMLock = %q, %v, want snapshot, nil", lock, err)
			}
			if got := readRecordedArgs(t, configArgs); got != "config\n123\n" {
				t.Fatalf("config args = %q", got)
			}
		})
	}
}

func TestFilesystemFreezeControl(t *testing.T) {
	t.Run("qemu asks the guest agent", func(t *testing.T) {
		statusArgs := installFakeCommand(t, "qm", "thawed\n", 0)
		status, err := newGuestOps(config.GuestTypeQEMU).VMFSFreezeStatus(
			context.Background(), testVMID)
		if err != nil || status != "thawed" {
			t.Fatalf("VMFSFreezeStatus = %q, %v, want thawed, nil", status, err)
		}
		if got := readRecordedArgs(t, statusArgs); got != "agent\n123\nfsfreeze-status\n" {
			t.Fatalf("freeze status args = %q", got)
		}
		thawArgs := installFakeCommand(t, "qm", "", 0)
		if err := newGuestOps(config.GuestTypeQEMU).VMFSFreezeThaw(
			context.Background(), testVMID); err != nil {
			t.Fatalf("VMFSFreezeThaw: %v", err)
		}
		if got := readRecordedArgs(t, thawArgs); got != "agent\n123\nfsfreeze-thaw\n" {
			t.Fatalf("thaw args = %q", got)
		}
	})
	t.Run("lxc reports no freeze control and runs no command", func(t *testing.T) {
		pctArgs := installFakeCommand(t, "pct", "", 0)
		realOps := newGuestOps(config.GuestTypeLXC)
		if _, err := realOps.VMFSFreezeStatus(context.Background(), testVMID); err == nil {
			t.Fatal("VMFSFreezeStatus succeeded on an LXC guest")
		}
		if err := realOps.VMFSFreezeThaw(context.Background(), testVMID); err == nil {
			t.Fatal("VMFSFreezeThaw succeeded on an LXC guest")
		}
		if _, err := os.Stat(pctArgs); err == nil {
			t.Fatal("pct ran for a freeze operation")
		}
	})
}
