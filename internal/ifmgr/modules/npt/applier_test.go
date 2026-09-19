package npt

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
)

// fakeConn captures the batch the applier builds so tests can assert on the
// rules, the flush-then-add ordering, and the single-transaction guarantee
// without a kernel netlink socket.
type fakeConn struct {
	ops        []string
	rules      []*nftables.Rule
	flushCount int
	flushErr   error
}

func (f *fakeConn) FlushChain(c *nftables.Chain) {
	f.ops = append(f.ops, "flushchain:"+c.Name)
}

func (f *fakeConn) AddRule(r *nftables.Rule) *nftables.Rule {
	f.ops = append(f.ops, "addrule:"+r.Chain.Name)
	f.rules = append(f.rules, r)
	return r
}

func (f *fakeConn) Flush() error {
	f.flushCount++
	f.ops = append(f.ops, "flush")
	return f.flushErr
}

func desiredForTest() desiredRules {
	var d desiredRules
	d.add(buildWANRules(wanInputForTest()))
	return d
}

// TestApplierSingleTransaction is the traffic-safety property: one reconcile
// commits both chains in exactly one transaction (one Flush), with each chain
// flushed then refilled in the same batch so no packet sees an empty chain.
func TestApplierSingleTransaction(t *testing.T) {
	t.Parallel()

	fake := &fakeConn{ops: nil, rules: nil, flushCount: 0, flushErr: nil}
	app := &nftApplier{newConn: func() (nftConn, error) { return fake, nil }}

	desired := desiredForTest()
	if err := app.Apply(context.Background(), slog.Default(), desired); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if fake.flushCount != 1 {
		t.Fatalf("Flush called %d times, want exactly 1 (single atomic transaction)", fake.flushCount)
	}

	wantOps := []string{"flushchain:postrouting"}
	for range desired.Postrouting {
		wantOps = append(wantOps, "addrule:postrouting")
	}
	wantOps = append(wantOps, "flushchain:prerouting")
	for range desired.Prerouting {
		wantOps = append(wantOps, "addrule:prerouting")
	}
	wantOps = append(wantOps, "flush")

	if len(fake.ops) != len(wantOps) {
		t.Fatalf("op sequence length = %d, want %d\ngot:  %v\nwant: %v",
			len(fake.ops), len(wantOps), fake.ops, wantOps)
	}
	for i := range wantOps {
		if fake.ops[i] != wantOps[i] {
			t.Fatalf("op[%d] = %q, want %q\nfull: %v", i, fake.ops[i], wantOps[i], fake.ops)
		}
	}
}

// TestApplierGuardRuleShape asserts the ct-status guard translates to a ct load,
// a bitwise mask, a not-equal compare, and a return verdict, with no NAT expr.
func TestApplierGuardRuleShape(t *testing.T) {
	t.Parallel()

	fake := &fakeConn{ops: nil, rules: nil, flushCount: 0, flushErr: nil}
	app := &nftApplier{newConn: func() (nftConn, error) { return fake, nil }}
	if err := app.Apply(context.Background(), slog.Default(), desiredForTest()); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// The guard is the first postrouting rule.
	guard := fake.rules[0]
	var sawCt, sawVerdict, sawNAT, sawMask bool
	for _, e := range guard.Exprs {
		switch v := e.(type) {
		case *expr.Ct:
			sawCt = true
			if v.Key != expr.CtKeySTATUS {
				t.Fatalf("guard ct key = %v, want STATUS", v.Key)
			}
		case *expr.Bitwise:
			sawMask = true
			// The guard must mask IPS_DST_NAT (0x20), the bit nft's
			// `ct status dnat` sets, not any other conntrack status bit.
			want := binaryutil.NativeEndian.PutUint32(0x20)
			if !bytes.Equal(v.Mask, want) {
				t.Fatalf("guard mask = %v, want IPS_DST_NAT %v", v.Mask, want)
			}
		case *expr.Verdict:
			if v.Kind != expr.VerdictReturn {
				t.Fatalf("guard verdict kind = %v, want Return", v.Kind)
			}
			sawVerdict = true
		case *expr.NAT:
			sawNAT = true
		}
	}
	if !sawCt || !sawVerdict || !sawMask {
		t.Fatalf("guard exprs missing ct/mask/verdict: ct=%v mask=%v verdict=%v", sawCt, sawMask, sawVerdict)
	}
	if sawNAT {
		t.Fatal("guard rule must not carry a NAT expression")
	}
}

// TestApplierNetmapRuleShape asserts the internal-prefix SNAT rule is a NETMAP:
// two immediates (range min/max) plus a NAT with Prefix set and a source type.
func TestApplierNetmapRuleShape(t *testing.T) {
	t.Parallel()

	fake := &fakeConn{ops: nil, rules: nil, flushCount: 0, flushErr: nil}
	app := &nftApplier{newConn: func() (nftConn, error) { return fake, nil }}
	if err := app.Apply(context.Background(), slog.Default(), desiredForTest()); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// The NETMAP SNAT is the fourth postrouting rule (index 3).
	netmap := fake.rules[3]
	immediates := 0
	var nat *expr.NAT
	for _, e := range netmap.Exprs {
		switch v := e.(type) {
		case *expr.Immediate:
			immediates++
		case *expr.NAT:
			nat = v
		}
	}
	if immediates != 2 {
		t.Fatalf("netmap immediates = %d, want 2 (range min/max)", immediates)
	}
	if nat == nil {
		t.Fatal("netmap rule missing NAT expression")
	}
	if !nat.Prefix {
		t.Fatal("netmap NAT must set Prefix (NF_NAT_RANGE_NETMAP)")
	}
	if nat.Type != expr.NATTypeSourceNAT {
		t.Fatalf("netmap NAT type = %v, want SourceNAT", nat.Type)
	}
	if nat.RegAddrMin != 1 || nat.RegAddrMax != 2 {
		t.Fatalf("netmap NAT regs = (%d,%d), want (1,2)", nat.RegAddrMin, nat.RegAddrMax)
	}
}

// TestApplierSingleSNATShape asserts an edge SNAT is a single-address NAT: one
// immediate, Prefix unset, only the min register.
func TestApplierSingleSNATShape(t *testing.T) {
	t.Parallel()

	fake := &fakeConn{ops: nil, rules: nil, flushCount: 0, flushErr: nil}
	app := &nftApplier{newConn: func() (nftConn, error) { return fake, nil }}
	if err := app.Apply(context.Background(), slog.Default(), desiredForTest()); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// The edge SNAT is the second postrouting rule (index 1).
	snat := fake.rules[1]
	immediates := 0
	var nat *expr.NAT
	for _, e := range snat.Exprs {
		switch v := e.(type) {
		case *expr.Immediate:
			immediates++
		case *expr.NAT:
			nat = v
		}
	}
	if immediates != 1 {
		t.Fatalf("single snat immediates = %d, want 1", immediates)
	}
	if nat == nil || nat.Prefix {
		t.Fatalf("single snat NAT must be present with Prefix unset: %#v", nat)
	}
	if nat.RegAddrMax != 0 {
		t.Fatalf("single snat RegAddrMax = %d, want 0", nat.RegAddrMax)
	}
}

// TestApplierEmptyStillOneTransaction confirms an empty desired set (all WANs
// missing PD) still flushes both chains in one transaction, rather than
// skipping the commit and leaving stale rules.
func TestApplierEmptyStillOneTransaction(t *testing.T) {
	t.Parallel()

	fake := &fakeConn{ops: nil, rules: nil, flushCount: 0, flushErr: nil}
	app := &nftApplier{newConn: func() (nftConn, error) { return fake, nil }}
	if err := app.Apply(context.Background(), slog.Default(), desiredRules{Postrouting: nil, Prerouting: nil}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if fake.flushCount != 1 {
		t.Fatalf("Flush called %d times, want 1", fake.flushCount)
	}
	if len(fake.rules) != 0 {
		t.Fatalf("added %d rules, want 0", len(fake.rules))
	}
	want := []string{"flushchain:postrouting", "flushchain:prerouting", "flush"}
	if len(fake.ops) != len(want) {
		t.Fatalf("ops = %v, want %v", fake.ops, want)
	}
}

// _ is a compile-time guard that *nftables.Conn satisfies nftConn, so the
// production applier can pass a real connection through the same seam.
var _ nftConn = (*nftables.Conn)(nil)
