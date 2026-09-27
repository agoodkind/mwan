//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/networkjson"
)

const firewallCheckTimeout = 30 * time.Second

// runFirewallCheck validates the firewall configuration and applies its rules
// only in a separate network namespace created by unshare.
func runFirewallCheck(args []string) int {
	isolated := len(args) == 3 && args[2] == "--isolated"
	if len(args) != 2 && !isolated {
		fmt.Fprintln(os.Stderr, "usage: mwan deploy-gate check-firewall <network.json> <schema-dir>")
		return 1
	}
	loaded, err := networkjson.Load(args[0], args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	desired, err := firewall.Compile(loaded.Firewall)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !loaded.Firewall.Enabled {
		fmt.Fprintln(os.Stderr, "firewall ownership is disabled")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallCheckTimeout)
	defer cancel()
	if !isolated {
		return runFirewallCheckChild(ctx, args)
	}
	if err := verifyDifferentNamespace(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := firewall.Apply(ctx, desired); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	readback, err := firewall.Inspect(ctx, desired)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(readback)
	return 0
}

func runFirewallInspect(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mwan deploy-gate inspect-firewall <network.json> <schema-dir>")
		return 1
	}
	loaded, err := networkjson.Load(args[0], args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !loaded.Firewall.Enabled {
		fmt.Fprintln(os.Stderr, "firewall ownership is disabled")
		return 1
	}
	desired, err := firewall.Compile(loaded.Firewall)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallCheckTimeout)
	defer cancel()
	readback, err := firewall.Inspect(ctx, desired)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(readback)
	return 0
}

func runFirewallCheckChild(ctx context.Context, args []string) int {
	slog.InfoContext(ctx, "start isolated firewall check")
	parentNamespace, err := os.Open("/proc/self/ns/net")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer parentNamespace.Close()
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	command := &exec.Cmd{
		Path: "/usr/bin/unshare",
		Args: []string{
			"unshare", "--net", "--", binary,
			"deploy-gate", "check-firewall", args[0], args[1], "--isolated",
		},
	}
	command.ExtraFiles = []*os.File{parentNamespace}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start isolated firewall check: %v\n", err)
		return 1
	}
	stopKill := context.AfterFunc(ctx, func() { _ = command.Process.Kill() })
	defer stopKill()
	if err := command.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "isolated firewall check failed: %v\n", err)
		return 1
	}
	return 0
}

func verifyDifferentNamespace() error {
	parent, err := os.Readlink("/proc/self/fd/3")
	if err != nil {
		slog.Error("read caller network namespace failed", "err", err)
		return fmt.Errorf("cannot verify caller network namespace: %w", err)
	}
	current, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		slog.Error("read isolated network namespace failed", "err", err)
		return fmt.Errorf("cannot verify isolated network namespace: %w", err)
	}
	if !strings.HasPrefix(parent, "net:[") || !strings.HasPrefix(current, "net:[") || parent == current {
		return fmt.Errorf("firewall check did not enter a separate network namespace")
	}
	return nil
}
