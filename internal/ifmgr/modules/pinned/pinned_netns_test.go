//go:build linux && netns

package pinned

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// The probe chain and the dummy link the matching test builds. The link gives
// both families an on-link route, so a datagram to an address the set stores
// and a datagram to an address it omits take the same route and reach the same
// hook. Set membership is then the only difference between them.
const (
	probeChainName = "pinned_probe_output"
	probeIface     = "pinprobe0"

	probeNetV4 = "192.0.2.1/24"
	probeNetV6 = "2001:db8:ff::1/64"

	// The first refresh pins the upper half of each on-link prefix.
	firstRangeV4 = "192.0.2.128/25"
	firstRangeV6 = "2001:db8:ff::8000/113"

	// The second refresh pins the lower half instead, which is what proves the
	// flush replaced the contents rather than adding to them.
	secondRangeV4 = "192.0.2.0/25"
	secondRangeV6 = "2001:db8:ff::/113"

	// One address inside each half, chosen so that swapping the ranges swaps
	// which address the kernel matches.
	upperAddrV4 = "192.0.2.130"
	lowerAddrV4 = "192.0.2.5"
	upperAddrV6 = "2001:db8:ff::8005"
	lowerAddrV6 = "2001:db8:ff::5"

	// Discard. Nothing listens, and nothing needs to: the output hook runs
	// before the packet leaves the host.
	probePort = 9
)

// ipv4DstOffset and ipv6DstOffset are where each network header stores the
// destination address, and the widths are the key widths of the two sets.
const (
	ipv4DstOffset = 16
	ipv4DstLen    = 4
	ipv6DstOffset = 24
	ipv6DstLen    = 16
)

// dadPollInterval and dadPollAttempts bound the wait for duplicate address
// detection to finish on the link's IPv6 address.
const (
	dadPollInterval = 50 * time.Millisecond
	dadPollAttempts = 100
)

// enterPrivateNetworkNamespace locks the test goroutine to its OS thread and
// moves that thread into a new, empty network namespace, so the links, routes
// and nftables ruleset the test creates never reach the host. The thread is
// never unlocked because a goroutine that exits while locked takes its thread
// with it, so no other test ever runs in the namespace. A missing privilege
// fails the test rather than skipping it.
func enterPrivateNetworkNamespace(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Fatalf("unshare a network namespace (needs CAP_SYS_ADMIN, and CAP_NET_ADMIN for the links): %v", err)
	}
}

// TestApplyFillsTheKernelSets runs the production applier against a real
// kernel. The recorded tests prove which bytes the applier queues; this one
// proves the kernel accepts them, stores the range as a start and an end
// marker, and reports the set back with the interval flag the marking rules
// need.
func TestApplyFillsTheKernelSets(t *testing.T) {
	enterPrivateNetworkNamespace(t)
	conn := openConn(t)
	addMangleTable(t, conn)

	desired := desiredSets{
		V4: mergePrefixes(mustPrefixes(t, "192.0.2.0/24", "198.51.100.7/32")),
		V6: mergePrefixes(mustPrefixes(t, "2600:1000::/28")),
	}
	applyInKernel(t, desired)

	assertSequence(t, kernelElementKeys(t, conn, setV4Name), []string{
		"192.0.2.0", "192.0.3.0 (end)",
		"198.51.100.7", "198.51.100.8 (end)",
	})
	assertSequence(t, kernelElementKeys(t, conn, setV6Name), []string{
		"2600:1000::", "2600:1010:: (end)",
	})

	for name, wantBytes := range map[string]uint32{setV4Name: ipv4DstLen, setV6Name: ipv6DstLen} {
		set := liveSet(t, conn, name)
		if !set.Interval {
			t.Errorf("kernel reports set %s without the interval flag", name)
		}
		if set.KeyType.Bytes != wantBytes {
			t.Errorf("set %s key width = %d bytes, want %d", name, set.KeyType.Bytes, wantBytes)
		}
	}
}

// TestPinnedSetsMatchTheIntendedAddresses is the test the recorded ones cannot
// replace. It sends real datagrams through the kernel's output path and reads
// the counters on two rules that look the destination up in the pinned sets, so
// a hit proves the kernel matched an address the configuration pinned and a
// miss proves it left an address the configuration did not pin alone.
//
// The second half re-runs the refresh with the halves swapped. Each address
// then changes verdict, which proves the second refresh replaced the contents
// rather than adding to them, and proves the applier writes a set the table
// already stores without redeclaring it.
func TestPinnedSetsMatchTheIntendedAddresses(t *testing.T) {
	enterPrivateNetworkNamespace(t)
	conn := openConn(t)
	table := addMangleTable(t, conn)

	applyInKernel(t, desiredSets{
		V4: mergePrefixes(mustPrefixes(t, firstRangeV4)),
		V6: mergePrefixes(mustPrefixes(t, firstRangeV6)),
	})

	chain := addProbeChain(t, conn, table)
	buildProbeLink(t, []string{upperAddrV4, lowerAddrV4, upperAddrV6, lowerAddrV6})

	// The upper half is pinned, so only the upper address counts.
	assertMatch(t, conn, table, chain, familyV4, upperAddrV4, true)
	assertMatch(t, conn, table, chain, familyV4, lowerAddrV4, false)
	assertMatch(t, conn, table, chain, familyV6, upperAddrV6, true)
	assertMatch(t, conn, table, chain, familyV6, lowerAddrV6, false)

	applyInKernel(t, desiredSets{
		V4: mergePrefixes(mustPrefixes(t, secondRangeV4)),
		V6: mergePrefixes(mustPrefixes(t, secondRangeV6)),
	})
	assertSequence(t, kernelElementKeys(t, conn, setV4Name), []string{
		"192.0.2.0", "192.0.2.128 (end)",
	})

	// The lower half is pinned now, so every verdict flips.
	assertMatch(t, conn, table, chain, familyV4, upperAddrV4, false)
	assertMatch(t, conn, table, chain, familyV4, lowerAddrV4, true)
	assertMatch(t, conn, table, chain, familyV6, upperAddrV6, false)
	assertMatch(t, conn, table, chain, familyV6, lowerAddrV6, true)
}

// TestApplyRepairsASetTheRulesetRemoved covers the case the applier's
// set-resolution exists for: a ruleset reload recreated the table without the
// pinned sets, and the next refresh has to declare them again rather than fail.
func TestApplyRepairsASetTheRulesetRemoved(t *testing.T) {
	enterPrivateNetworkNamespace(t)
	conn := openConn(t)
	table := addMangleTable(t, conn)

	desired := desiredSets{
		V4: mergePrefixes(mustPrefixes(t, firstRangeV4)),
		V6: mergePrefixes(mustPrefixes(t, firstRangeV6)),
	}
	applyInKernel(t, desired)

	conn.DelTable(table)
	if err := conn.Flush(); err != nil {
		t.Fatalf("delete the mangle table: %v", err)
	}
	addMangleTable(t, conn)
	if _, err := conn.GetSetByName(table, setV4Name); err == nil {
		t.Fatalf("set %s survived the table delete, so this test proves nothing", setV4Name)
	}

	applyInKernel(t, desired)
	assertSequence(t, kernelElementKeys(t, conn, setV4Name), []string{
		"192.0.2.128", "192.0.3.0 (end)",
	})
}

// openConn returns a netlink connection into the namespace the calling thread
// entered. Every helper here shares it, so a stray connection cannot reach the
// host ruleset.
func openConn(t *testing.T) *nftables.Conn {
	t.Helper()
	conn, err := nftables.New()
	if err != nil {
		t.Fatalf("open an nftables netlink connection: %v", err)
	}
	return conn
}

// addMangleTable creates the inet mangle table the ruleset owns. The module
// never creates a table, so without this the refresh has nothing to write into.
func addMangleTable(t *testing.T, conn *nftables.Conn) *nftables.Table {
	t.Helper()
	table := conn.AddTable(&nftables.Table{
		Family: nftables.TableFamilyINet,
		Name:   mangleTableName,
	})
	if err := conn.Flush(); err != nil {
		t.Fatalf("create table inet %s: %v", mangleTableName, err)
	}
	return table
}

// applyInKernel runs the production applier, the one that opens its own
// netlink connection, against the namespace's ruleset.
func applyInKernel(t *testing.T, desired desiredSets) {
	t.Helper()
	if err := newNFTApplier().Apply(context.Background(), slog.Default(), desired); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func liveSet(t *testing.T, conn *nftables.Conn, name string) *nftables.Set {
	t.Helper()
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: mangleTableName}
	set, err := conn.GetSetByName(table, name)
	if err != nil {
		t.Fatalf("read set %s back from the kernel: %v", name, err)
	}
	return set
}

// kernelElementKeys reads one set back and renders its elements the way the
// recorded tests render the queued ones, so the two assertions read alike and a
// mismatch names addresses rather than bytes.
func kernelElementKeys(t *testing.T, conn *nftables.Conn, name string) []string {
	t.Helper()
	elements, err := conn.GetSetElements(liveSet(t, conn, name))
	if err != nil {
		t.Fatalf("read the elements of %s: %v", name, err)
	}
	return elementKeys(t, elements)
}

// addProbeChain adds an output-hook chain with one counting rule per family.
// Each rule narrows to its family, reads the destination out of the network
// header, and looks it up in that family's pinned set, which is what the
// marking rules in the real ruleset do before they set a mark.
func addProbeChain(t *testing.T, conn *nftables.Conn, table *nftables.Table) *nftables.Chain {
	t.Helper()
	chain := conn.AddChain(&nftables.Chain{
		Name:     probeChainName,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		// The chain only counts. An explicit accept keeps a probe datagram from
		// depending on what the kernel defaults a base chain to.
		Policy: chainPolicyAccept(),
	})
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: lookupExprs(unix.NFPROTO_IPV4, ipv4DstOffset, ipv4DstLen, setV4Name),
	})
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: lookupExprs(unix.NFPROTO_IPV6, ipv6DstOffset, ipv6DstLen, setV6Name),
	})
	if err := conn.Flush(); err != nil {
		t.Fatalf("add the probe chain and its rules: %v", err)
	}
	return chain
}

func chainPolicyAccept() *nftables.ChainPolicy {
	policy := nftables.ChainPolicyAccept
	return &policy
}

func lookupExprs(proto byte, offset, length uint32, setName string) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{
			DestRegister: 1,
			Base:         expr.PayloadBaseNetworkHeader,
			Offset:       offset,
			Len:          length,
		},
		&expr.Lookup{SourceRegister: 1, SetName: setName},
		&expr.Counter{},
	}
}

// packetCount reads the counter on the rule for fam. The rules are added in
// family order, so index zero counts IPv4 and index one counts IPv6.
func packetCount(
	t *testing.T, conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, fam family,
) uint64 {
	t.Helper()
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		t.Fatalf("read the probe rules: %v", err)
	}
	index := 0
	if fam.bits == familyV6.bits {
		index = 1
	}
	if len(rules) <= index {
		t.Fatalf("chain %s has %d rules, want at least %d", chain.Name, len(rules), index+1)
	}
	for _, e := range rules[index].Exprs {
		counter, ok := e.(*expr.Counter)
		if ok {
			return counter.Packets
		}
	}
	t.Fatalf("the %s probe rule declares no counter", fam.name)
	return 0
}

// assertMatch sends one datagram to target and reports whether the kernel
// counted it against that family's pinned set. The send itself is best-effort:
// the output hook runs before the packet leaves the host, so the counter is the
// evidence and a delivery failure past that point is not.
func assertMatch(
	t *testing.T,
	conn *nftables.Conn,
	table *nftables.Table,
	chain *nftables.Chain,
	fam family,
	target string,
	want bool,
) {
	t.Helper()
	before := packetCount(t, conn, table, chain, fam)
	sendProbe(t, fam, target)
	after := packetCount(t, conn, table, chain, fam)

	matched := after > before
	if matched != want {
		t.Fatalf("destination %s matched the %s pinned set = %t, want %t (counter %d -> %d)",
			target, fam.name, matched, want, before, after)
	}
	if matched && after != before+1 {
		t.Fatalf("destination %s counted %d times, want exactly 1", target, after-before)
	}
}

func sendProbe(t *testing.T, fam family, target string) {
	t.Helper()
	address := net.JoinHostPort(target, fmt.Sprint(probePort))
	network := "udp4"
	if fam.bits == familyV6.bits {
		network = "udp6"
	}
	socket, err := net.Dial(network, address)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	defer func() { _ = socket.Close() }()
	if _, err := socket.Write([]byte("pinned probe")); err != nil {
		t.Logf("write to %s failed past the output hook: %v", address, err)
	}
}

// buildProbeLink creates the dummy link that gives both families an on-link
// route, and pins a permanent neighbour entry for every destination, so no send
// waits on an address resolution the dummy link can never answer.
func buildProbeLink(t *testing.T, targets []string) {
	t.Helper()
	attributes := netlink.NewLinkAttrs()
	attributes.Name = probeIface
	link := &netlink.Dummy{LinkAttrs: attributes}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("add dummy link %s: %v", probeIface, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("set %s up: %v", probeIface, err)
	}
	for _, cidr := range []string{probeNetV4, probeNetV6} {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatalf("parse %s: %v", cidr, err)
		}
		if err := netlink.AddrAdd(link, address); err != nil {
			t.Fatalf("add %s to %s: %v", cidr, probeIface, err)
		}
	}
	// Duplicate address detection keeps a fresh IPv6 address tentative, and a
	// socket cannot bind a tentative source. Wait for the address to leave that
	// state before the first send.
	waitForIPv6Ready(t, link)

	for _, target := range targets {
		addPermanentNeighbour(t, link, target)
	}
}

// waitForIPv6Ready blocks until the link's IPv6 address has finished duplicate
// address detection.
func waitForIPv6Ready(t *testing.T, link netlink.Link) {
	t.Helper()
	for range dadPollAttempts {
		addresses, err := netlink.AddrList(link, unix.AF_INET6)
		if err != nil {
			t.Fatalf("list the IPv6 addresses on %s: %v", probeIface, err)
		}
		ready := false
		for _, address := range addresses {
			if address.Flags&unix.IFA_F_TENTATIVE == 0 && address.IP.IsGlobalUnicast() {
				ready = true
			}
		}
		if ready {
			return
		}
		time.Sleep(dadPollInterval)
	}
	t.Fatalf("the IPv6 address on %s never left duplicate address detection", probeIface)
}

func addPermanentNeighbour(t *testing.T, link netlink.Link, target string) {
	t.Helper()
	addr, err := netip.ParseAddr(target)
	if err != nil {
		t.Fatalf("parse %s: %v", target, err)
	}
	fam := unix.AF_INET
	if addr.Is6() {
		fam = unix.AF_INET6
	}
	neighbour := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       fam,
		State:        netlink.NUD_PERMANENT,
		IP:           net.IP(addr.AsSlice()),
		HardwareAddr: net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
	}
	if err := netlink.NeighSet(neighbour); err != nil {
		t.Fatalf("pin a neighbour entry for %s: %v", target, err)
	}
}
