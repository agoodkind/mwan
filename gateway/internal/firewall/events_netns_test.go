//go:build linux && netns

package firewall_test

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/offload"
)

const nativePolicySocketBytes = 65536

func TestRulesetEventsCheckpointPolicy(t *testing.T) {
	const environment = "MWAN_RULESET_EVENTS_CHILD"
	if os.Getenv(environment) != "1" {
		if os.Geteuid() != 0 {
			t.Fatal("nftables observation proof requires a privileged network namespace")
		}
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRulesetEventsCheckpointPolicy$", "-test.v")
		if deadline, present := t.Deadline(); present {
			remaining := deadline.Sub(clock.Real{}.Now())
			if remaining <= 0 {
				t.Fatal("nftables observation child started after the test deadline")
			}
			command.Args = append(command.Args, "-test.timeout="+remaining.String())
		}
		command.Env = append(os.Environ(), environment+"=1")
		command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated nftables observation proof: %v: %s", err, output)
		}
		t.Log(string(output))
		return
	}
	deadline, present := t.Deadline()
	if !present {
		t.Fatal("nftables observation proof requires a test deadline")
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	events, err := offload.NewRulesetEvents(ctx, nativePolicySocketBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := events.Close(); err != nil {
			t.Errorf("close nftables observations: %v", err)
		}
	}()
	initial, err := events.Barrier(ctx)
	if err != nil {
		t.Fatalf("initial nftables checkpoint: %v", err)
	}
	policy := nativeExtraPolicy([]string{"ct state established,related accept", "ip ecn ect0 drop"})
	if err := firewall.Apply(ctx, policy); err != nil {
		t.Fatal(err)
	}
	changed, err := events.Barrier(ctx)
	if err != nil || changed <= initial {
		t.Fatalf("policy transaction did not advance its observation checkpoint: before=%d after=%d error=%v", initial, changed, err)
	}
	stable, err := events.Barrier(ctx)
	if err != nil || stable != changed {
		t.Fatalf("a checkpoint created a false policy change: before=%d after=%d error=%v", changed, stable, err)
	}
	policy = nativeExtraPolicy([]string{"ip ecn ect0 drop", "ct state established,related accept"})
	if err := firewall.Apply(ctx, policy); err != nil {
		t.Fatal(err)
	}
	unsafeGeneration, err := events.Barrier(ctx)
	if err != nil || unsafeGeneration <= stable {
		t.Fatalf("header policy change did not invalidate the checkpoint: before=%d after=%d error=%v", stable, unsafeGeneration, err)
	}
	if err := events.Err(); err != nil {
		t.Fatalf("nftables notifications lost certification: %v", err)
	}
}
