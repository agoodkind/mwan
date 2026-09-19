package steering

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// exprIndex returns the index of the first expression in exprs matching pred,
// or -1 if none matches. Used to assert relative expression order rather than
// mere presence.
func exprIndex(exprs []expr.Any, pred func(expr.Any) bool) int {
	for i, e := range exprs {
		if pred(e) {
			return i
		}
	}
	return -1
}

func isMarkZeroGuard(e expr.Any) bool {
	m, ok := e.(*expr.Meta)
	return ok && m.Key == expr.MetaKeyMARK && !m.SourceRegister
}

func isCtStateGuard(e expr.Any) bool {
	c, ok := e.(*expr.Ct)
	return ok && c.Key == expr.CtKeySTATE
}

func isGenerator(e expr.Any) bool {
	switch e.(type) {
	case *expr.Numgen, *expr.Hash:
		return true
	default:
		return false
	}
}

func isLookup(e expr.Any) bool {
	_, ok := e.(*expr.Lookup)
	return ok
}

func isMarkWrite(e expr.Any) bool {
	m, ok := e.(*expr.Meta)
	return ok && m.Key == expr.MetaKeyMARK && m.SourceRegister
}

// fakeConn captures the batch the applier builds so tests can assert on the
// table and chain creation, the flush-then-add ordering, the rules, and the
// single-transaction guarantee without a kernel netlink socket.
type fakeConn struct {
	ops        []string
	rules      []*nftables.Rule
	sets       []*nftables.Set
	elements   [][]nftables.SetElement
	flushCount int
	flushErr   error
}

func (f *fakeConn) AddTable(t *nftables.Table) *nftables.Table {
	f.ops = append(f.ops, "addtable:"+t.Name)
	return t
}

func (f *fakeConn) AddChain(c *nftables.Chain) *nftables.Chain {
	f.ops = append(f.ops, "addchain:"+c.Name)
	return c
}

func (f *fakeConn) FlushChain(c *nftables.Chain) {
	f.ops = append(f.ops, "flushchain:"+c.Name)
}

func (f *fakeConn) AddSet(s *nftables.Set, vals []nftables.SetElement) error {
	f.ops = append(f.ops, "addset")
	// The real connection allocates the id and the name here, which the rule
	// then references, so the fake does the same.
	s.ID = uint32(len(f.sets) + 1)
	s.Name = "__map%d"
	f.sets = append(f.sets, s)
	f.elements = append(f.elements, vals)
	return nil
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

func newFakeApplier() (*fakeConn, *nftApplier) {
	fake := &fakeConn{
		ops: nil, rules: nil, sets: nil, elements: nil, flushCount: 0, flushErr: nil,
	}
	return fake, &nftApplier{newConn: func() (nftConn, error) { return fake, nil }}
}

func spreadRulesForTest(mode hashMode) []steerRule {
	return buildRules(ruleInput{
		InternalIface:  "enmwanbr0",
		InternalNetV4:  netip.MustParsePrefix("10.250.250.0/29"),
		InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
		OpnsenseEdgeV6: netip.MustParseAddr("3d06:bad:b01:201::1"),
		Mode:           mode,
		Assign:         balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}},
	})
}

// TestApplierCreatesOwnsAndCommitsInOneTransaction is the traffic-safety
// property: one reconcile creates the table and the chain, empties the chain,
// refills it, and commits everything with exactly one Flush.
func TestApplierCreatesOwnsAndCommitsInOneTransaction(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if fake.flushCount != 1 {
		t.Fatalf("Flush called %d times, want exactly 1", fake.flushCount)
	}
	wantOps := []string{
		"addtable:mwan_steer",
		"addchain:prerouting",
		"flushchain:prerouting",
		"addset", "addrule:prerouting",
		"addset", "addrule:prerouting",
		"addset", "addrule:prerouting",
		"flush",
	}
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

// TestApplierChainPlacement pins where the chain hooks in. One step after the
// ruleset file's mangle chain at -150 is what lets the mark-zero guard see the
// marks that chain sets.
func TestApplierChainPlacement(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	chain := fake.rules[0].Chain
	if chain.Table.Family != nftables.TableFamilyINet || chain.Table.Name != "mwan_steer" {
		t.Fatalf("table = %v %q, want inet mwan_steer", chain.Table.Family, chain.Table.Name)
	}
	if chain.Type != nftables.ChainTypeFilter {
		t.Fatalf("chain type = %q, want filter", chain.Type)
	}
	if chain.Hooknum == nil || *chain.Hooknum != *nftables.ChainHookPrerouting {
		t.Fatalf("chain hook = %v, want prerouting", chain.Hooknum)
	}
	if chain.Priority == nil || *chain.Priority != -149 {
		t.Fatalf("chain priority = %v, want -149", chain.Priority)
	}
	if chain.Policy == nil || *chain.Policy != nftables.ChainPolicyAccept {
		t.Fatalf("chain policy = %v, want accept", chain.Policy)
	}
}

// TestApplierSpreadRuleShape asserts one spread rule carries, in order, the
// incoming-link match, the family match, the source match, the mark-zero guard,
// the new-flow guard, the generator, the map lookup, and the mark write.
func TestApplierSpreadRuleShape(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	var numgen *expr.Numgen
	var lookup *expr.Lookup
	markWrites := 0
	for _, e := range fake.rules[0].Exprs {
		switch v := e.(type) {
		case *expr.Numgen:
			numgen = v
		case *expr.Lookup:
			lookup = v
		case *expr.Meta:
			if v.Key == expr.MetaKeyMARK && v.SourceRegister {
				markWrites++
			}
		}
	}
	if numgen == nil {
		t.Fatal("spread rule carries no generator")
	}
	if numgen.Type != unix.NFT_NG_RANDOM {
		t.Fatalf("generator type = %d, want random", numgen.Type)
	}
	if numgen.Modulus != 2 {
		t.Fatalf("generator modulus = %d, want 2", numgen.Modulus)
	}
	if lookup == nil || !lookup.IsDestRegSet {
		t.Fatalf("spread rule carries no map lookup writing a register: %#v", lookup)
	}
	if lookup.SetID != fake.sets[0].ID || lookup.SetName != fake.sets[0].Name {
		t.Fatalf("lookup names set %q/%d, want %q/%d",
			lookup.SetName, lookup.SetID, fake.sets[0].Name, fake.sets[0].ID)
	}
	if markWrites != 1 {
		t.Fatalf("mark writes = %d, want exactly 1", markWrites)
	}

	// The map is the slot-to-mark table, keyed in the kernel's own byte order
	// because both the generated slot and the mark are host-order words.
	wantElements := []nftables.SetElement{
		{Key: binaryutil.NativeEndian.PutUint32(0), Val: binaryutil.NativeEndian.PutUint32(1)},
		{Key: binaryutil.NativeEndian.PutUint32(1), Val: binaryutil.NativeEndian.PutUint32(2)},
	}
	got := fake.elements[0]
	if len(got) != len(wantElements) {
		t.Fatalf("map has %d elements, want %d", len(got), len(wantElements))
	}
	for i := range wantElements {
		if !bytes.Equal(got[i].Key, wantElements[i].Key) || !bytes.Equal(got[i].Val, wantElements[i].Val) {
			t.Fatalf("map element %d = %v -> %v, want %v -> %v",
				i, got[i].Key, got[i].Val, wantElements[i].Key, wantElements[i].Val)
		}
	}
	if !fake.sets[0].IsMap || !fake.sets[0].Anonymous || !fake.sets[0].Constant {
		t.Fatalf("set flags = map:%v anonymous:%v constant:%v, want all true",
			fake.sets[0].IsMap, fake.sets[0].Anonymous, fake.sets[0].Constant)
	}

	// Position, not just presence: the mark-zero guard and the new-flow guard
	// must run before the generator draws a slot, which must run before the
	// map lookup resolves it to a mark, which must run before the mark write
	// commits it. A rule that writes the mark and only then checks it against
	// zero always sees its own just-written value on every later packet, so a
	// reordering here silently stops the balancer from ever assigning a
	// provider.
	exprs := fake.rules[0].Exprs
	markZeroIndex := exprIndex(exprs, isMarkZeroGuard)
	ctStateIndex := exprIndex(exprs, isCtStateGuard)
	generatorIndex := exprIndex(exprs, isGenerator)
	lookupIndex := exprIndex(exprs, isLookup)
	markWriteIndex := exprIndex(exprs, isMarkWrite)
	if markZeroIndex < 0 || ctStateIndex < 0 || generatorIndex < 0 || lookupIndex < 0 || markWriteIndex < 0 {
		t.Fatalf("one or more expressions missing: markZero=%d ctState=%d generator=%d lookup=%d markWrite=%d",
			markZeroIndex, ctStateIndex, generatorIndex, lookupIndex, markWriteIndex)
	}
	if !(markZeroIndex < ctStateIndex && ctStateIndex < generatorIndex &&
		generatorIndex < lookupIndex && lookupIndex < markWriteIndex) {
		t.Fatalf("expression order wrong: markZero=%d ctState=%d generator=%d lookup=%d markWrite=%d, want strictly increasing",
			markZeroIndex, ctStateIndex, generatorIndex, lookupIndex, markWriteIndex)
	}
}

// TestApplierGuardsEverySpreadRule pins the two guards on every rule: the mark
// must still be zero, which is what preserves the control-plane pins the
// ruleset file sets earlier in the pass, and the flow must be new, which is
// what keeps an established flow on the provider it already has.
func TestApplierGuardsEverySpreadRule(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for index, rule := range fake.rules {
		sawMarkRead, sawCtState, sawCtMask := false, false, false
		for _, e := range rule.Exprs {
			switch v := e.(type) {
			case *expr.Meta:
				if v.Key == expr.MetaKeyMARK && !v.SourceRegister {
					sawMarkRead = true
				}
			case *expr.Ct:
				if v.Key == expr.CtKeySTATE {
					sawCtState = true
				}
			case *expr.Bitwise:
				if bytes.Equal(v.Mask, binaryutil.NativeEndian.PutUint32(0x08)) {
					sawCtMask = true
				}
			}
		}
		if !sawMarkRead || !sawCtState || !sawCtMask {
			t.Fatalf("rule %d missing a guard: mark=%v ct=%v mask=%v",
				index, sawMarkRead, sawCtState, sawCtMask)
		}

		// Position, not just presence: the mark-zero guard must run before the
		// new-flow guard, which must run before the balancer's own
		// generator/lookup/mark-write sequence, or a rule that already wrote a
		// mark could still be read as "unmarked" by a guard evaluated too late.
		markZeroIndex := exprIndex(rule.Exprs, isMarkZeroGuard)
		ctStateIndex := exprIndex(rule.Exprs, isCtStateGuard)
		generatorIndex := exprIndex(rule.Exprs, isGenerator)
		if !(markZeroIndex >= 0 && markZeroIndex < ctStateIndex && ctStateIndex < generatorIndex) {
			t.Fatalf("rule %d guard order wrong: markZero=%d ctState=%d generator=%d, want strictly increasing",
				index, markZeroIndex, ctStateIndex, generatorIndex)
		}
	}
}

// TestApplierSingleMemberSetsTheMarkOutright asserts that one carrying provider
// produces an immediate mark with no generator and no map, which is what the
// gateway runs while only one provider is healthy.
func TestApplierSingleMemberSetsTheMarkOutright(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	rules := buildRules(ruleInput{
		InternalIface:  "enmwanbr0",
		InternalNetV4:  netip.MustParsePrefix("10.250.250.0/29"),
		InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
		OpnsenseEdgeV6: netip.MustParseAddr("3d06:bad:b01:201::1"),
		Mode:           hashModeRandom,
		Assign:         balancer{Mark: 3, Modulus: 0, Slots: nil},
	})
	if err := app.Apply(context.Background(), slog.Default(), rules); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(fake.sets) != 0 {
		t.Fatalf("added %d maps for a single provider, want 0", len(fake.sets))
	}
	var immediate *expr.Immediate
	for _, e := range fake.rules[0].Exprs {
		if value, isImmediate := e.(*expr.Immediate); isImmediate {
			immediate = value
		}
		if _, isNumgen := e.(*expr.Numgen); isNumgen {
			t.Fatal("single-provider rule carries a generator")
		}
	}
	if immediate == nil {
		t.Fatal("single-provider rule carries no immediate mark")
	}
	if !bytes.Equal(immediate.Data, binaryutil.NativeEndian.PutUint32(3)) {
		t.Fatalf("immediate mark = %v, want 3", immediate.Data)
	}
}

// TestApplierSourceHashUsesJenkinsOverTheSourceAddress asserts the source mode
// derives the slot from the source address with the daemon's own seed, so one
// source keeps landing on one provider across reconciles.
func TestApplierSourceHashUsesJenkinsOverTheSourceAddress(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeSource)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	var hash *expr.Hash
	for _, e := range fake.rules[0].Exprs {
		if value, isHash := e.(*expr.Hash); isHash {
			hash = value
		}
		if _, isNumgen := e.(*expr.Numgen); isNumgen {
			t.Fatal("source mode carries a random generator")
		}
	}
	if hash == nil {
		t.Fatal("source mode carries no hash")
	}
	if hash.Type != expr.HashTypeJenkins {
		t.Fatalf("hash type = %d, want jenkins", hash.Type)
	}
	// The first rule matches the internal IPv4 network, so the hash reads the
	// four-byte IPv4 source address only.
	if hash.Length != 4 {
		t.Fatalf("hash length = %d, want 4 (IPv4 source only)", hash.Length)
	}
	if hash.Modulus != 2 {
		t.Fatalf("hash modulus = %d, want 2", hash.Modulus)
	}
	if hash.Seed == 0 {
		t.Fatal("hash seed is zero; the binding omits the attribute and the kernel picks its own")
	}
	if hash.SourceRegister != unix.NFT_REG32_00 {
		t.Fatalf("hash source register = %d, want NFT_REG32_00", hash.SourceRegister)
	}
}

// TestApplierSourceDestinationHashReadsBothAddresses asserts the concatenated
// mode loads the destination directly after the source in the 32-bit register
// file and hashes both together.
func TestApplierSourceDestinationHashReadsBothAddresses(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeSourceDestination)); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	// The third rule matches the internal IPv6 prefix, so both addresses are
	// sixteen bytes and the destination sits four 32-bit slots along.
	var hash *expr.Hash
	daddrLoads := 0
	for _, e := range fake.rules[2].Exprs {
		switch value := e.(type) {
		case *expr.Hash:
			hash = value
		case *expr.Payload:
			if value.Offset == 24 && value.DestRegister == unix.NFT_REG32_04 {
				daddrLoads++
			}
		}
	}
	if hash == nil {
		t.Fatal("source-destination mode carries no hash")
	}
	if daddrLoads != 1 {
		t.Fatalf("destination loads = %d, want 1", daddrLoads)
	}
	if hash.Length != 32 {
		t.Fatalf("hash length = %d, want 32 (both IPv6 addresses)", hash.Length)
	}
}

// TestApplierEmptyStillCreatesAndCommits confirms a pass with no healthy
// provider still creates the table and the chain and commits an empty chain, so
// the previous split is removed rather than left behind.
func TestApplierEmptyStillCreatesAndCommits(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	if err := app.Apply(context.Background(), slog.Default(), nil); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if fake.flushCount != 1 {
		t.Fatalf("Flush called %d times, want 1", fake.flushCount)
	}
	if len(fake.rules) != 0 {
		t.Fatalf("added %d rules, want 0", len(fake.rules))
	}
	want := []string{"addtable:mwan_steer", "addchain:prerouting", "flushchain:prerouting", "flush"}
	if len(fake.ops) != len(want) {
		t.Fatalf("ops = %v, want %v", fake.ops, want)
	}
}

// TestApplierPropagatesFlushError asserts a failed commit surfaces to the
// caller, so a rejected or partial netlink batch is never mistaken for a
// successful reconcile.
func TestApplierPropagatesFlushError(t *testing.T) {
	t.Parallel()

	fake, app := newFakeApplier()
	fake.flushErr = errors.New("netlink flush rejected")
	err := app.Apply(context.Background(), slog.Default(), spreadRulesForTest(hashModeRandom))
	if err == nil {
		t.Fatal("Apply returned nil error, want the wrapped flush error")
	}
	if !errors.Is(err, fake.flushErr) {
		t.Fatalf("Apply error = %v, want it to wrap %v", err, fake.flushErr)
	}
}

// _ is a compile-time guard that *nftables.Conn satisfies nftConn, so the
// production applier can pass a real connection through the same seam.
var _ nftConn = (*nftables.Conn)(nil)
