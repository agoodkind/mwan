package steering

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	// steerTableName and steerChainName are the table and chain this module
	// owns outright. Nothing else writes them, so the module may empty the
	// chain wholesale on every pass.
	steerTableName = "mwan_steer"
	steerChainName = "prerouting"

	// steerChainPriority puts the chain one step after the ruleset file's
	// mangle chain at -150, so that chain's connection-mark restore and its
	// per-link ingress marks have already run when this chain's mark-zero guard
	// looks. Reversing the two would overwrite the control-plane pins.
	steerChainPriority nftables.ChainPriority = -149

	// ipv4SaddrOffset, ipv4DaddrOffset, and ipv4AddrLen locate the IPv4
	// addresses relative to the network header.
	ipv4SaddrOffset uint32 = 12
	ipv4DaddrOffset uint32 = 16
	ipv4AddrLen     uint32 = 4
	ipv4AddrBits    int    = 32

	// ipv6SaddrOffset, ipv6DaddrOffset, and ipv6AddrLen locate the IPv6
	// addresses relative to the network header.
	ipv6SaddrOffset uint32 = 8
	ipv6DaddrOffset uint32 = 24
	ipv6AddrLen     uint32 = 16
	ipv6AddrBits    int    = 128

	// ctStateNew is NF_CT_STATE_BIT(IP_CT_NEW) from
	// linux/netfilter/nf_conntrack_common.h: 1 shifted left by IP_CT_NEW plus
	// one, with IP_CT_NEW equal to 2. It is the bit nft's `ct state new` masks,
	// and the ruleset file's ingress rules already match on it.
	ctStateNew uint32 = 0x08
	// ctStateLen is the width of the conntrack state word.
	ctStateLen uint32 = 4

	// hashSeed is the seed every hashed balancer uses. It is deliberately not
	// zero: google/nftables v0.3.0 omits NFTA_HASH_SEED when the field is zero
	// (expr/hash.go:61), and a rule that carries no seed does not carry a seed
	// this daemon controls, so one source would land on a different provider
	// after a reconcile. A fixed seed keeps a source on a provider for as long
	// as the provider set and the weights hold.
	hashSeed uint32 = 0x6d77616e

	// hashKeyRegister is the first 32-bit register. The hash modes load the
	// addresses into the 32-bit register file rather than into registers 1 and
	// 2, because those are four 32-bit slots apart and a concatenation must be
	// contiguous for one hash to read both addresses. This is the register
	// layout nft itself compiles a concatenated hash into.
	hashKeyRegister uint32 = unix.NFT_REG32_00
	// hashKeyRegisterV4Second holds the IPv4 destination address, one 32-bit
	// slot after the four-byte source address.
	hashKeyRegisterV4Second uint32 = unix.NFT_REG32_01
	// hashKeyRegisterV6Second holds the IPv6 destination address, four 32-bit
	// slots after the sixteen-byte source address.
	hashKeyRegisterV6Second uint32 = unix.NFT_REG32_04
)

// nftConn is the subset of *nftables.Conn the applier drives. Injecting it lets
// tests capture the batch without opening a kernel netlink socket.
type nftConn interface {
	AddTable(t *nftables.Table) *nftables.Table
	AddChain(c *nftables.Chain) *nftables.Chain
	FlushChain(c *nftables.Chain)
	AddSet(s *nftables.Set, vals []nftables.SetElement) error
	AddRule(r *nftables.Rule) *nftables.Rule
	Flush() error
}

// applier commits a desired steering rule set. The module depends on this
// interface so its tests can substitute a fake that records what was computed.
type applier interface {
	Apply(ctx context.Context, log *slog.Logger, desired []steerRule) error
}

// nftApplier translates a desired rule set into google/nftables operations and
// replaces the chain's contents in one atomic transaction.
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
		slog.Warn("steering: open nftables netlink connection failed", "err", err)
		return nil, fmt.Errorf("nftables.New: %w", err)
	}
	return conn, nil
}

// Apply creates the table and the chain, replaces the chain's contents with
// desired, and commits everything in one netlink batch. The table and the chain
// are created on every pass rather than once at Init, because an nftables
// reload begins with a ruleset flush that deletes them; AddTable and AddChain
// carry the create flag without the exclusive flag, so a pass over structures
// that still exist is a no-op. Nothing here ever deletes a table or a chain.
func (a *nftApplier) Apply(ctx context.Context, log *slog.Logger, desired []steerRule) error {
	conn, err := a.newConn()
	if err != nil {
		return err
	}

	table := &nftables.Table{
		Name:   steerTableName,
		Use:    0,
		Flags:  0,
		Family: nftables.TableFamilyINet,
	}
	conn.AddTable(table)

	policy := nftables.ChainPolicyAccept
	priority := steerChainPriority
	chain := &nftables.Chain{
		Name:     steerChainName,
		Table:    table,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: &priority,
		Type:     nftables.ChainTypeFilter,
		Policy:   &policy,
		Device:   "",
	}
	conn.AddChain(chain)

	// The chain is emptied and refilled inside the same batch, committed by a
	// single Flush, so no packet ever sees a half-written chain.
	conn.FlushChain(chain)
	for _, rule := range desired {
		exprs, buildErr := ruleExprs(conn, table, rule)
		if buildErr != nil {
			return fmt.Errorf("build steering rule %s: %w", rule, buildErr)
		}
		conn.AddRule(&nftables.Rule{
			Table: table, Chain: chain, Position: 0, Handle: 0,
			Flags: 0, Exprs: exprs, UserData: nil,
		})
	}

	if err := conn.Flush(); err != nil {
		log.WarnContext(ctx, "steering: nft flush failed", "err", err)
		return fmt.Errorf("nft flush: %w", err)
	}
	return nil
}

// ruleExprs translates one typed rule into its ordered expressions: the
// incoming-link match, the address-family match, the source-address match, the
// two guards, and the mark assignment. The family match is not optional: the
// chain lives in an inet table, where the network header layout is unknown
// until the protocol has been compared.
func ruleExprs(conn nftConn, table *nftables.Table, rule steerRule) ([]expr.Any, error) {
	exprs := make([]expr.Any, 0, 20)
	if rule.IifName != "" {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, SourceRegister: false, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname(rule.IifName)},
		)
	}
	exprs = append(exprs, familyMatchExprs(rule.Source)...)
	exprs = append(exprs, sourceMatchExprs(rule.Source)...)
	exprs = append(exprs, markZeroGuardExprs()...)
	exprs = append(exprs, ctStateNewExprs()...)
	assign, err := assignExprs(conn, table, rule)
	if err != nil {
		return nil, err
	}
	return append(exprs, assign...), nil
}

// familyMatchExprs compares the packet's protocol family, which an inet chain
// must do before it reads any network-header offset.
func familyMatchExprs(source netip.Prefix) []expr.Any {
	proto := byte(unix.NFPROTO_IPV6)
	if source.Addr().Is4() {
		proto = byte(unix.NFPROTO_IPV4)
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, SourceRegister: false, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
	}
}

// sourceMatchExprs matches the packet's source address against p. A host prefix
// is an exact compare; a shorter one masks the loaded address before comparing
// it to the network.
func sourceMatchExprs(source netip.Prefix) []expr.Any {
	source = source.Masked()
	offset, length, bits := ipv6SaddrOffset, ipv6AddrLen, ipv6AddrBits
	if source.Addr().Is4() {
		offset, length, bits = ipv4SaddrOffset, ipv4AddrLen, ipv4AddrBits
	}
	load := &expr.Payload{
		OperationType: expr.PayloadLoad, DestRegister: 1, SourceRegister: 0,
		Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: length,
		CsumType: expr.CsumTypeNone, CsumOffset: 0, CsumFlags: 0,
	}
	network := addrBytes(source.Addr())
	if source.Bits() == bits {
		return []expr.Any{
			load,
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: network},
		}
	}
	return []expr.Any{
		load,
		&expr.Bitwise{
			SourceRegister: 1, DestRegister: 1, Len: length,
			Mask: net.CIDRMask(source.Bits(), bits), Xor: make([]byte, length),
		},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: network},
	}
}

// markZeroGuardExprs matches a packet whose mark is still zero. It is what
// preserves the control-plane pins the ruleset file sets earlier in the pass: a
// packet already carrying a mark falls through this rule untouched.
func markZeroGuardExprs() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: false, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
	}
}

// ctStateNewExprs matches a packet opening a new flow. An established flow keeps
// the provider conntrack already assigned it, which is what the mangle chain's
// connection-mark restore depends on.
func ctStateNewExprs() []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, SourceRegister: false, Key: expr.CtKeySTATE, Direction: 0},
		&expr.Bitwise{
			SourceRegister: 1, DestRegister: 1, Len: ctStateLen,
			Mask: binaryutil.NativeEndian.PutUint32(ctStateNew),
			Xor:  binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
	}
}

// assignExprs builds the mark assignment. A single carrying provider is an
// immediate value; two or more are a generated slot looked up in an anonymous
// map from slot to mark, which is the shape the three lines in the ruleset file
// used, generalized past two providers and past equal weights.
func assignExprs(conn nftConn, table *nftables.Table, rule steerRule) ([]expr.Any, error) {
	if len(rule.Assign.Slots) == 0 {
		return []expr.Any{
			&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(rule.Assign.Mark)},
			&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
		}, nil
	}
	generator, err := generatorExprs(rule)
	if err != nil {
		return nil, err
	}
	set, err := slotMapSet(conn, table, rule.Assign.Slots)
	if err != nil {
		return nil, err
	}
	return append(generator,
		&expr.Lookup{
			SourceRegister: 1, DestRegister: 1, IsDestRegSet: true,
			SetID: set.ID, SetName: set.Name, Invert: false,
		},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
	), nil
}

// generatorExprs produces the slot index the map is keyed on. random draws a
// fresh number per connection; the two hash modes derive it from the addresses,
// so one source, or one source and destination pair, keeps landing on the same
// provider while the provider set and the weights hold.
func generatorExprs(rule steerRule) ([]expr.Any, error) {
	switch rule.Mode {
	case hashModeRandom:
		return []expr.Any{
			&expr.Numgen{
				Register: 1, Modulus: rule.Assign.Modulus,
				Type: unix.NFT_NG_RANDOM, Offset: 0,
			},
		}, nil
	case hashModeSource:
		return hashExprs(rule.Source, rule.Assign.Modulus, false), nil
	case hashModeSourceDestination:
		return hashExprs(rule.Source, rule.Assign.Modulus, true), nil
	}
	return nil, fmt.Errorf("steering: unknown hash mode %q", string(rule.Mode))
}

// hashExprs loads the addresses into the 32-bit register file and hashes them
// into the slot index. The addresses are loaded again here rather than reused
// from the source match, because the two guards in between overwrite the
// register the match used, and a concatenation must sit in adjacent 32-bit
// slots for one hash to read both.
func hashExprs(source netip.Prefix, modulus uint32, withDestination bool) []expr.Any {
	saddrOffset, daddrOffset := ipv6SaddrOffset, ipv6DaddrOffset
	addrLen, secondRegister := ipv6AddrLen, hashKeyRegisterV6Second
	if source.Addr().Is4() {
		saddrOffset, daddrOffset = ipv4SaddrOffset, ipv4DaddrOffset
		addrLen, secondRegister = ipv4AddrLen, hashKeyRegisterV4Second
	}
	exprs := []expr.Any{
		&expr.Payload{
			OperationType: expr.PayloadLoad, DestRegister: hashKeyRegister, SourceRegister: 0,
			Base: expr.PayloadBaseNetworkHeader, Offset: saddrOffset, Len: addrLen,
			CsumType: expr.CsumTypeNone, CsumOffset: 0, CsumFlags: 0,
		},
	}
	keyLength := addrLen
	if withDestination {
		exprs = append(exprs, &expr.Payload{
			OperationType: expr.PayloadLoad, DestRegister: secondRegister, SourceRegister: 0,
			Base: expr.PayloadBaseNetworkHeader, Offset: daddrOffset, Len: addrLen,
			CsumType: expr.CsumTypeNone, CsumOffset: 0, CsumFlags: 0,
		})
		keyLength = addrLen * 2
	}
	return append(exprs, &expr.Hash{
		SourceRegister: hashKeyRegister, DestRegister: 1, Length: keyLength,
		Modulus: modulus, Seed: hashSeed, Offset: 0, Type: expr.HashTypeJenkins,
	})
}

// slotMapSet adds the anonymous slot-to-mark map one rule reads. The map is
// anonymous and constant, which is what nft builds for a literal map inside a
// rule: it lives and dies with the rule that references it, so the chain flush
// that starts every pass takes the previous one with it. Both the slot and the
// mark are host-order words, so both sides use the kernel's own byte order.
func slotMapSet(conn nftConn, table *nftables.Table, slots []uint32) (*nftables.Set, error) {
	set := &nftables.Set{
		Table: table, ID: 0, Name: "",
		Anonymous: true, Constant: true, Interval: false, AutoMerge: false,
		IsMap: true, HasTimeout: false, Counter: false, Dynamic: false,
		Concatenation: false, Timeout: 0,
		KeyType: nftables.TypeInteger, DataType: nftables.TypeMark,
		KeyByteOrder: binaryutil.NativeEndian, Comment: "", Size: 0,
	}
	elements := make([]nftables.SetElement, 0, len(slots))
	slotIndex := uint32(0)
	for _, mark := range slots {
		elements = append(elements, nftables.SetElement{
			Key:         binaryutil.NativeEndian.PutUint32(slotIndex),
			Val:         binaryutil.NativeEndian.PutUint32(mark),
			KeyEnd:      nil,
			IntervalEnd: false,
			VerdictData: nil,
			Timeout:     0,
			Expires:     0,
			Counter:     nil,
			Comment:     "",
		})
		slotIndex++
	}
	if err := conn.AddSet(set, elements); err != nil {
		slog.Warn("steering: add slot map failed", "slots", len(slots), "err", err)
		return nil, fmt.Errorf("add slot map: %w", err)
	}
	return set, nil
}

// addrBytes returns the on-wire form of a: four bytes for IPv4 and sixteen for
// IPv6, matching the payload the rule compares against.
func addrBytes(a netip.Addr) []byte {
	if a.Is4() {
		octets := a.As4()
		return octets[:]
	}
	octets := a.As16()
	return octets[:]
}

// ifname returns the NUL-terminated interface name used for the incoming-link
// compare.
func ifname(name string) []byte {
	return []byte(name + "\x00")
}
