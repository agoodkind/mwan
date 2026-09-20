package pinned

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/google/nftables"
)

// recordedConn captures the batch the applier builds, so a test can assert the
// exact elements, the order of the operations, and the single-transaction
// guarantee without a kernel netlink socket.
type recordedConn struct {
	ops        []string
	elements   map[string][]nftables.SetElement
	sets       map[string]*nftables.Set
	flushCount int
	flushErr   error
}

func newRecordedConn() *recordedConn {
	return &recordedConn{
		ops:        nil,
		elements:   map[string][]nftables.SetElement{},
		sets:       map[string]*nftables.Set{},
		flushCount: 0,
		flushErr:   nil,
	}
}

func (c *recordedConn) AddSet(s *nftables.Set, vals []nftables.SetElement) error {
	c.ops = append(c.ops, "addset:"+s.Name)
	c.sets[s.Name] = s
	c.elements[s.Name] = append(c.elements[s.Name], vals...)
	return nil
}

func (c *recordedConn) FlushSet(s *nftables.Set) {
	c.ops = append(c.ops, "flushset:"+s.Name)
	c.elements[s.Name] = nil
}

func (c *recordedConn) SetAddElements(s *nftables.Set, vals []nftables.SetElement) error {
	c.ops = append(c.ops, "addelements:"+s.Name)
	c.elements[s.Name] = append(c.elements[s.Name], vals...)
	return nil
}

func (c *recordedConn) Flush() error {
	c.flushCount++
	c.ops = append(c.ops, "flush")
	return c.flushErr
}

func applierOn(conn *recordedConn) *nftApplier {
	return &nftApplier{newConn: func() (nftConn, error) { return conn, nil }}
}

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

// elementKeys renders one set's recorded elements as readable addresses, with
// an end marker named as one, so a mismatch reads as addresses rather than as
// raw bytes.
func elementKeys(t *testing.T, elements []nftables.SetElement) []string {
	t.Helper()
	keys := make([]string, 0, len(elements))
	for _, element := range elements {
		addr, ok := netip.AddrFromSlice(element.Key)
		if !ok {
			t.Fatalf("element key %v is not an address", element.Key)
		}
		text := addr.String()
		if element.IntervalEnd {
			text += " (end)"
		}
		keys = append(keys, text)
	}
	return keys
}

func assertSequence(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

// TestApplyOneTransaction is the safety property: one refresh creates each set,
// empties it, and refills it inside a single committed batch, so no packet sees
// a set that lost its contents and a rejected element leaves the old contents
// in place.
func TestApplyOneTransaction(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	desired := desiredSets{
		V4: mergePrefixes(mustPrefixes(t, "192.0.2.0/24")),
		V6: mergePrefixes(mustPrefixes(t, "2001:db8::/32")),
	}
	if err := applierOn(conn).Apply(context.Background(), slog.Default(), desired); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if conn.flushCount != 1 {
		t.Fatalf("Flush called %d times, want exactly 1", conn.flushCount)
	}
	wantOps := []string{
		"addset:" + setV4Name, "flushset:" + setV4Name, "addelements:" + setV4Name,
		"addset:" + setV6Name, "flushset:" + setV6Name, "addelements:" + setV6Name,
		"flush",
	}
	assertSequence(t, conn.ops, wantOps)
}

// TestApplyDeclaresTheSetsTheRulesetDeclares pins the definition used for a set
// this module has to create: an interval set of addresses of the right width,
// which is what the marking rules look up.
func TestApplyDeclaresTheSetsTheRulesetDeclares(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	if err := applierOn(conn).Apply(context.Background(), slog.Default(), desiredSets{V4: nil, V6: nil}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for name, wantType := range map[string]nftables.SetDatatype{
		setV4Name: nftables.TypeIPAddr,
		setV6Name: nftables.TypeIP6Addr,
	} {
		set, ok := conn.sets[name]
		if !ok {
			t.Fatalf("set %s was never created", name)
		}
		if set.KeyType.Name != wantType.Name {
			t.Errorf("set %s key type = %s, want %s", name, set.KeyType.Name, wantType.Name)
		}
		if !set.Interval {
			t.Errorf("set %s is not an interval set", name)
		}
		if set.Table.Name != mangleTableName || set.Table.Family != nftables.TableFamilyINet {
			t.Errorf("set %s table = %s family %d, want %s inet",
				name, set.Table.Name, set.Table.Family, mangleTableName)
		}
	}
}

// TestApplyEncodesRangesAsStartAndEndMarker pins the on-wire form of a range:
// its first address, then a marker element at the address just past its end.
// This is the encoding the kernel's interval backend reads, and it is what the
// whole refresh comes down to.
func TestApplyEncodesRangesAsStartAndEndMarker(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	desired := desiredSets{
		V4: mergePrefixes(mustPrefixes(t, "192.0.2.0/24", "198.51.100.7/32")),
		V6: mergePrefixes(mustPrefixes(t, "2600:1000::/28")),
	}
	if err := applierOn(conn).Apply(context.Background(), slog.Default(), desired); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	assertSequence(t, elementKeys(t, conn.elements[setV4Name]), []string{
		"192.0.2.0", "192.0.3.0 (end)",
		"198.51.100.7", "198.51.100.8 (end)",
	})
	assertSequence(t, elementKeys(t, conn.elements[setV6Name]), []string{
		"2600:1000::", "2600:1010:: (end)",
	})
}

// TestApplyLeavesARangeAtTheTopOfTheSpaceOpen covers the one range that cannot
// carry an end marker, because no address follows it. The kernel reads a start
// with no following marker as running to the top of the key space, which is
// exactly the range.
func TestApplyLeavesARangeAtTheTopOfTheSpaceOpen(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	desired := desiredSets{
		V4: mergePrefixes(mustPrefixes(t, "255.255.255.128/25")),
		V6: mergePrefixes(mustPrefixes(t, "::/0")),
	}
	if err := applierOn(conn).Apply(context.Background(), slog.Default(), desired); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertSequence(t, elementKeys(t, conn.elements[setV4Name]), []string{"255.255.255.128"})
	assertSequence(t, elementKeys(t, conn.elements[setV6Name]), []string{"::"})
}

// TestApplyKeyWidthMatchesTheSet checks the byte width each set's keys carry,
// because a key of the wrong width is rejected by the kernel rather than
// stored.
func TestApplyKeyWidthMatchesTheSet(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	desired := desiredSets{
		V4: mergePrefixes(mustPrefixes(t, "10.0.0.0/8")),
		V6: mergePrefixes(mustPrefixes(t, "2001:db8::/64")),
	}
	if err := applierOn(conn).Apply(context.Background(), slog.Default(), desired); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, element := range conn.elements[setV4Name] {
		if len(element.Key) != 4 {
			t.Fatalf("v4 key %v is %d bytes, want 4", element.Key, len(element.Key))
		}
	}
	for _, element := range conn.elements[setV6Name] {
		if len(element.Key) != 16 {
			t.Fatalf("v6 key %v is %d bytes, want 16", element.Key, len(element.Key))
		}
	}
	if !bytes.Equal(conn.elements[setV4Name][0].Key, []byte{10, 0, 0, 0}) {
		t.Fatalf("v4 start key = %v, want 10.0.0.0", conn.elements[setV4Name][0].Key)
	}
}

// TestApplyReportsAFailedCommit proves a rejected transaction reaches the
// caller, which is what makes the module retry rather than record a refresh
// that never landed.
func TestApplyReportsAFailedCommit(t *testing.T) {
	t.Parallel()

	conn := newRecordedConn()
	conn.flushErr = errors.New("netlink refused the batch")
	err := applierOn(conn).Apply(context.Background(), slog.Default(),
		desiredSets{V4: mergePrefixes(mustPrefixes(t, "192.0.2.0/24")), V6: nil})
	if err == nil {
		t.Fatal("Apply returned no error for a rejected commit")
	}
}
