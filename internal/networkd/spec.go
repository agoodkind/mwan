// Package networkd describes a provider link the way the network manager
// brings it up, and maps each typed leaf of the network configuration to the
// unit-file section and key it renders to. The network configuration loader
// builds a Spec per rendered link and calls Validate on it, so a key the
// typed layer and the free-form layer both set fails the load rather than
// silently overriding one another.
package networkd

import (
	"fmt"
	"net/netip"
	"strings"
)

// FileKind names one of the three unit files an interface can produce.
type FileKind string

// The unit file kinds, spelled as the model's networkd/file/kind enumeration
// spells them.
const (
	FileLink    FileKind = "link"
	FileNetwork FileKind = "network"
	FileNetdev  FileKind = "netdev"
)

// Spec is one interface's link as the loader built it from its entry: the
// identity the network manager matches and names the device by, how each
// family is addressed, and the free-form sections the typed leaves do not
// name. Every typed value here has a row in the placements table except the
// gateway, the route metric, and the source addresses: the first two are
// built into repeated [Route] sections rather than looked up, and the last
// never reaches a unit file because the routing module reads it.
type Spec struct {
	// Name is the interface's name, the entry's key and the name the .link
	// file asks the network manager to give the device.
	Name string
	// TableID is the provider's routing table, which the second route a
	// static gateway contributes names.
	TableID int
	// Match is how the network manager recognizes the device. Exactly one of
	// its leaves is set unless VLAN is set instead.
	Match Match
	// HardwareAddress is the address to set on the device, which is not
	// always the address it is matched by. Empty sets none.
	HardwareAddress string
	// VLAN is set when the interface is created as a VLAN on another
	// interface rather than bound to a device.
	VLAN *VLAN
	// IPv4 and IPv6 are the per-family containers as the entry carries them;
	// nil is a family the entry does not configure.
	IPv4 *FamilyV4
	IPv6 *FamilyV6
	// Files are the free-form sections, one entry per unit file kind, in
	// document order.
	Files []File
}

// Match is how the network manager recognizes the device, by the driver it
// presents or by its hardware address.
type Match struct {
	Driver          string
	HardwareAddress string
}

// VLAN names the interface a VLAN is created on and the tag it carries. The
// integer widths here and below are the model's own, so a value arrives
// already inside its leaf's range and is widened, never narrowed, on the way
// to a unit file or the served tree.
type VLAN struct {
	Parent string
	ID     uint16
}

// Address is one static address with its prefix length.
type Address struct {
	IP           netip.Addr
	PrefixLength uint8
}

// Family is what both address families carry: whether the interface
// forwards, its static addresses, whether a DHCP client runs, the next hop
// for a static address, and the metric of the default route it contributes.
// Booleans and the metric are pointers so an absent leaf is distinguishable
// from a false or a zero the entry typed.
type Family struct {
	Forwarding  *bool
	Addresses   []Address
	DHCP        *bool
	Gateway     netip.Addr
	RouteMetric *uint32
}

// FamilyV4 is the IPv4 container: the shared family leaves plus the
// addresses beyond the link's own that the provider routes back.
type FamilyV4 struct {
	Family
	SourceAddresses []netip.Addr
}

// FamilyV6 is the IPv6 container: the shared family leaves, whether router
// advertisements are accepted, and the delegation the interface requests.
type FamilyV6 struct {
	Family
	AcceptRA   *bool
	Delegation *Delegation
}

// Delegation is the delegation the interface asks for and the identity it
// asks with. The identity is a public identifier, not a secret.
type Delegation struct {
	Hint                  netip.Prefix
	DUIDType              string
	DUID                  string
	WithoutRA             string
	UseDelegatedPrefix    *bool
	RouterLifetimeSeconds *uint32
}

// File is the free-form content of one unit file kind.
type File struct {
	Kind     FileKind
	Sections []Section
}

// Section is one free-form heading and its lines, keyed by position because
// the network manager reads a repeated heading as one section.
type Section struct {
	Index   uint16
	Name    string
	Entries []Entry
}

// Entry is one free-form line, keyed by position because the network manager
// reads a repeated key within a section as a list.
type Entry struct {
	Index uint16
	Key   string
	Value string
}

// placement names the file, section, and key one typed leaf renders to.
// This table is the only place in the daemon where a networkd key name
// appears, so a new option is a row here and a leaf in the model, never a
// change to the renderer.
type placement struct {
	File    FileKind
	Section string
	Key     string
}

// placements maps each typed leaf, named by its path under the interface
// entry, to where it lands. The two DHCP rows fold into one emitted key,
// because the network manager takes one value naming the families. The VLAN
// parent has no row, because the line it produces lands on the parent's own
// file rather than this interface's.
//
// Each row reads file, section, key.
var placements = map[string]placement{
	"match.driver":                       {FileLink, "Match", "Driver"},
	"match.hardware-address":             {FileLink, "Match", "MACAddress"},
	"name":                               {FileLink, "Link", "Name"},
	"hardware-address":                   {FileLink, "Link", "MACAddress"},
	"vlan.id":                            {FileNetdev, "VLAN", "Id"},
	"ipv4.address":                       {FileNetwork, "Network", "Address"},
	"ipv6.address":                       {FileNetwork, "Network", "Address"},
	"ipv4.dhcp":                          {FileNetwork, "Network", "DHCP"},
	"ipv6.dhcp":                          {FileNetwork, "Network", "DHCP"},
	"ipv6.accept-ra":                     {FileNetwork, "Network", "IPv6AcceptRA"},
	"ipv4.forwarding":                    {FileNetwork, "Network", "IPv4Forwarding"},
	"ipv6.forwarding":                    {FileNetwork, "Network", "IPv6Forwarding"},
	"delegation.duid-type":               {FileNetwork, "DHCPv6", "DUIDType"},
	"delegation.duid":                    {FileNetwork, "DHCPv6", "DUIDRawData"},
	"delegation.hint":                    {FileNetwork, "DHCPv6", "PrefixDelegationHint"},
	"delegation.without-ra":              {FileNetwork, "DHCPv6", "WithoutRA"},
	"delegation.use-delegated-prefix":    {FileNetwork, "DHCPv6", "UseDelegatedPrefix"},
	"delegation.router-lifetime-seconds": {FileNetwork, "IPv6PrefixDelegation", "RouterLifetimeSec"},
}

// Validate refuses a spec whose free-form sections set a key one of its own
// typed leaves already sets. The typed layer always renders first, so the
// duplicate would either be read by the network manager as a list or silently
// win, and neither is what the operator typed twice on purpose. The error
// names the section, the key, and the leaf that already sets it.
func Validate(spec Spec) error {
	occupied := make(map[placement]string, len(placements))
	for _, leaf := range spec.typedLeaves() {
		occupied[placements[leaf]] = leaf
	}
	for _, file := range spec.Files {
		for _, section := range file.Sections {
			for _, entry := range section.Entries {
				key := placement{File: file.Kind, Section: section.Name, Key: entry.Key}
				leaf, taken := occupied[key]
				if !taken {
					continue
				}
				return fmt.Errorf("networkd section %s key %s is set by the %s leaf; remove one",
					section.Name, entry.Key, leafLabel(leaf))
			}
		}
	}
	return nil
}

// typedLeaves names every placement row this spec occupies. The name is
// always occupied, because every spec asks for one.
func (s Spec) typedLeaves() []string {
	leaves := []string{"name"}
	if s.Match.Driver != "" {
		leaves = append(leaves, "match.driver")
	}
	if s.Match.HardwareAddress != "" {
		leaves = append(leaves, "match.hardware-address")
	}
	if s.HardwareAddress != "" {
		leaves = append(leaves, "hardware-address")
	}
	if s.VLAN != nil {
		leaves = append(leaves, "vlan.id")
	}
	if s.IPv4 != nil {
		leaves = append(leaves, familyLeaves("ipv4", s.IPv4.Family)...)
	}
	if s.IPv6 != nil {
		leaves = append(leaves, familyLeaves("ipv6", s.IPv6.Family)...)
		if s.IPv6.AcceptRA != nil {
			leaves = append(leaves, "ipv6.accept-ra")
		}
		if s.IPv6.Delegation != nil {
			leaves = append(leaves, delegationLeaves(*s.IPv6.Delegation)...)
		}
	}
	return leaves
}

// familyLeaves names the placement rows one family's shared leaves occupy.
func familyLeaves(family string, shared Family) []string {
	var leaves []string
	if shared.Forwarding != nil {
		leaves = append(leaves, family+".forwarding")
	}
	if len(shared.Addresses) > 0 {
		leaves = append(leaves, family+".address")
	}
	if shared.DHCP != nil {
		leaves = append(leaves, family+".dhcp")
	}
	return leaves
}

// delegationLeaves names the placement rows a delegation occupies.
func delegationLeaves(delegation Delegation) []string {
	var leaves []string
	if delegation.Hint.IsValid() {
		leaves = append(leaves, "delegation.hint")
	}
	if delegation.DUIDType != "" {
		leaves = append(leaves, "delegation.duid-type")
	}
	if delegation.DUID != "" {
		leaves = append(leaves, "delegation.duid")
	}
	if delegation.WithoutRA != "" {
		leaves = append(leaves, "delegation.without-ra")
	}
	if delegation.UseDelegatedPrefix != nil {
		leaves = append(leaves, "delegation.use-delegated-prefix")
	}
	if delegation.RouterLifetimeSeconds != nil {
		leaves = append(leaves, "delegation.router-lifetime-seconds")
	}
	return leaves
}

// leafLabel renders a leaf's path the way the error names it: the container
// and the leaf separated by a space, so "delegation.hint" reads as "the
// delegation hint leaf".
func leafLabel(leaf string) string {
	return strings.ReplaceAll(leaf, ".", " ")
}
