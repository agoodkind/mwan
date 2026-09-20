package pinned

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/google/nftables"
)

const (
	// mangleTableName and the two set names are the ruleset's own names for
	// the marking table and the pinned-destination sets. The rules that read
	// the sets live in that table and are written by the firewall ruleset, not
	// here, so these are a contract with it rather than a setting.
	mangleTableName = "mangle"
	setV4Name       = "att_pinned_v4"
	setV6Name       = "att_pinned_v6"
)

// nftConn is the subset of *nftables.Conn the applier drives. Injecting it
// lets tests record the batch (the set creations, the two set flushes, the
// elements, and the single Flush) without opening a kernel netlink socket.
type nftConn interface {
	AddSet(s *nftables.Set, vals []nftables.SetElement) error
	FlushSet(s *nftables.Set)
	SetAddElements(s *nftables.Set, vals []nftables.SetElement) error
	Flush() error
}

// applier commits one refresh's desired sets. The module depends on this
// interface so a test can drive the whole path onto a recorded connection.
type applier interface {
	Apply(ctx context.Context, log *slog.Logger, desired desiredSets) error
}

// nftApplier writes the desired contents through google/nftables.
type nftApplier struct {
	newConn func() (nftConn, error)
}

// newNFTApplier returns the production applier backed by a real netlink
// connection opened per Apply call.
func newNFTApplier() *nftApplier {
	return &nftApplier{newConn: defaultNFTConn}
}

func defaultNFTConn() (nftConn, error) {
	conn, err := nftables.New()
	if err != nil {
		slog.Warn("pinned: open nftables netlink connection failed", "err", err)
		return nil, fmt.Errorf("nftables.New: %w", err)
	}
	return conn, nil
}

// Apply replaces the contents of both sets in one netlink batch, committed by
// a single Conn.Flush. Each set is created first, which the kernel treats as a
// no-op where it already exists, so a ruleset reload that recreated the table
// without them is repaired rather than written past. Nothing else in the table
// is touched: no table, chain or rule is added or deleted, and only these two
// sets are flushed.
func (a *nftApplier) Apply(ctx context.Context, log *slog.Logger, desired desiredSets) error {
	conn, err := a.newConn()
	if err != nil {
		return err
	}

	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: mangleTableName, Use: 0, Flags: 0}
	writes := []struct {
		set    *nftables.Set
		ranges []addrRange
	}{
		{set: pinnedSet(table, setV4Name, nftables.TypeIPAddr), ranges: desired.V4},
		{set: pinnedSet(table, setV6Name, nftables.TypeIP6Addr), ranges: desired.V6},
	}

	for _, write := range writes {
		if err := conn.AddSet(write.set, nil); err != nil {
			log.WarnContext(ctx, "pinned: queue set creation failed",
				"set", write.set.Name, "err", err)
			return fmt.Errorf("add set %s: %w", write.set.Name, err)
		}
		// The flush and the adds ride the same transaction, so the set never
		// appears empty to a packet and a rejected element leaves the previous
		// contents in place.
		conn.FlushSet(write.set)
		elements := setElements(write.ranges)
		if len(elements) == 0 {
			continue
		}
		if err := conn.SetAddElements(write.set, elements); err != nil {
			log.WarnContext(ctx, "pinned: queue set elements failed",
				"set", write.set.Name, "err", err)
			return fmt.Errorf("add elements to %s: %w", write.set.Name, err)
		}
	}

	if err := conn.Flush(); err != nil {
		log.WarnContext(ctx, "pinned: nft flush failed", "err", err)
		return fmt.Errorf("nft flush: %w", err)
	}
	return nil
}

// pinnedSet describes one pinned-destination set exactly as the ruleset
// declares it: an interval set of addresses with automatic merging. The kernel
// keeps the existing definition where the set is already there, so this
// matters only for the set this module has to create.
func pinnedSet(table *nftables.Table, name string, keyType nftables.SetDatatype) *nftables.Set {
	return &nftables.Set{
		Table:         table,
		ID:            0,
		Name:          name,
		Anonymous:     false,
		Constant:      false,
		Interval:      true,
		AutoMerge:     true,
		IsMap:         false,
		HasTimeout:    false,
		Counter:       false,
		Dynamic:       false,
		Concatenation: false,
		Timeout:       0,
		KeyType:       keyType,
		DataType:      nftables.TypeInvalid,
		KeyByteOrder:  nil,
		Comment:       "",
		Size:          0,
	}
}

// setElements renders merged ranges as the elements an interval set holds. The
// kernel's interval backend stores a range as its first address followed by a
// marker element at the address just past its end, and reports a hit for a key
// at or after a start and before the following marker. A range that runs to
// the last address of its family therefore carries no marker, because there is
// no address past it and the run to the top of the key space is what the
// kernel reads an unterminated start as.
func setElements(ranges []addrRange) []nftables.SetElement {
	elements := make([]nftables.SetElement, 0, 2*len(ranges))
	for _, r := range ranges {
		elements = append(elements, setElement(r.start, false))
		if after := r.end.Next(); after.IsValid() {
			elements = append(elements, setElement(after, true))
		}
	}
	return elements
}

func setElement(addr netip.Addr, intervalEnd bool) nftables.SetElement {
	return nftables.SetElement{
		Key:         addrKey(addr),
		Val:         nil,
		KeyEnd:      nil,
		IntervalEnd: intervalEnd,
		VerdictData: nil,
		Timeout:     0,
		Expires:     0,
		Counter:     nil,
		Comment:     "",
	}
}

// addrKey returns the on-wire form of an address: four bytes for IPv4 and
// sixteen for IPv6, which is the key width each set declares.
func addrKey(addr netip.Addr) []byte {
	if addr.Is4() {
		octets := addr.As4()
		return octets[:]
	}
	octets := addr.As16()
	return octets[:]
}
