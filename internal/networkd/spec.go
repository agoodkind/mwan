package networkd

import (
	"fmt"
	"strings"

	"goodkind.io/mwan/internal/interfaceintent"
)

// FileKind identifies the networkd unit file category.
type FileKind string

const (
	// FileLink identifies a .link unit.
	FileLink FileKind = "link"
	// FileNetwork identifies a .network unit.
	FileNetwork FileKind = "network"
	// FileNetdev identifies a .netdev unit.
	FileNetdev FileKind = "netdev"
)

type placement struct {
	File    FileKind
	Section string
	Key     string
}

var placements = map[string]placement{
	"match.driver":                       {FileLink, "Match", "Driver"},
	"match.hardware-address":             {FileLink, "Match", "MACAddress"},
	"name":                               {FileLink, "Link", "Name"},
	"hardware-address":                   {FileLink, "Link", "MACAddress"},
	"mtu":                                {FileLink, "Link", "MTUBytes"},
	"vlan.id":                            {FileNetdev, "VLAN", "Id"},
	"bridge-master":                      {FileNetwork, "Network", "Bridge"},
	"ipv4.address":                       {FileNetwork, "Network", "Address"},
	"ipv6.address":                       {FileNetwork, "Network", "Address"},
	"ipv4.dhcp":                          {FileNetwork, "Network", "DHCP"},
	"ipv6.dhcp":                          {FileNetwork, "Network", "DHCP"},
	"ipv6.accept-ra":                     {FileNetwork, "Network", "IPv6AcceptRA"},
	"ipv4.forwarding":                    {FileNetwork, "Network", "IPv4Forwarding"},
	"ipv6.forwarding":                    {FileNetwork, "Network", "IPv6Forwarding"},
	"ipv4.route-metric":                  {FileNetwork, "DHCPv4", "RouteMetric"},
	"ipv6.route-metric":                  {FileNetwork, "IPv6AcceptRA", "RouteMetric"},
	"route.destination":                  {FileNetwork, "Route", "Destination"},
	"route.gateway":                      {FileNetwork, "Route", "Gateway"},
	"route.table":                        {FileNetwork, "Route", "Table"},
	"route.metric":                       {FileNetwork, "Route", "Metric"},
	"delegation.duid-type":               {FileNetwork, "DHCPv6", "DUIDType"},
	"delegation.duid":                    {FileNetwork, "DHCPv6", "DUIDRawData"},
	"delegation.hint":                    {FileNetwork, "DHCPv6", "PrefixDelegationHint"},
	"delegation.without-ra":              {FileNetwork, "DHCPv6", "WithoutRA"},
	"delegation.use-delegated-prefix":    {FileNetwork, "DHCPv6", "UseDelegatedPrefix"},
	"delegation.router-lifetime-seconds": {FileNetwork, "IPv6PrefixDelegation", "RouterLifetimeSec"},
}

// Validate rejects free-form keys that a typed field already sets.
func Validate(connection interfaceintent.Connection) error {
	occupied := make(map[placement]string, len(placements))
	for _, leaf := range typedLeaves(connection) {
		occupied[placements[leaf]] = leaf
	}
	for _, file := range connection.Networkd {
		for _, section := range file.Sections {
			for _, entry := range section.Entries {
				key := placement{File: FileKind(file.Kind), Section: section.Name, Key: entry.Key}
				if leaf, taken := occupied[key]; taken {
					return fmt.Errorf("networkd section %s key %s is set by the %s leaf; remove one",
						section.Name, entry.Key, leafLabel(leaf))
				}
			}
		}
	}
	return nil
}

func typedLeaves(connection interfaceintent.Connection) []string {
	leaves := []string{"name"}
	if connection.Link == nil {
		return leaves
	}
	link := connection.Link
	if link.Match.Driver != "" {
		leaves = append(leaves, "match.driver")
	}
	if link.Match.HardwareAddress != "" {
		leaves = append(leaves, "match.hardware-address")
	}
	if link.HardwareAddress != "" {
		leaves = append(leaves, "hardware-address")
	}
	if link.MTU != nil {
		leaves = append(leaves, "mtu")
	}
	if link.VLAN != nil {
		leaves = append(leaves, "vlan.id")
	}
	if link.BridgeMaster != "" {
		leaves = append(leaves, "bridge-master")
	}
	if connection.IPv4 != nil {
		leaves = append(leaves, familyLeaves("ipv4", connection.IPv4.Family)...)
	}
	if connection.IPv6 != nil {
		leaves = append(leaves, familyLeaves("ipv6", connection.IPv6.Family)...)
		if connection.IPv6.AcceptRA != nil {
			leaves = append(leaves, "ipv6.accept-ra")
		}
		if connection.IPv6.Delegation != nil {
			leaves = append(leaves, delegationLeaves(*connection.IPv6.Delegation)...)
		}
	}
	return leaves
}

func familyLeaves(name string, family interfaceintent.Family) []string {
	var leaves []string
	if len(family.Routes) > 0 {
		leaves = append(leaves, "route.destination", "route.table", "route.metric")
		for _, route := range family.Routes {
			if route.Gateway.IsValid() {
				leaves = append(leaves, "route.gateway")
				break
			}
		}
	}
	if family.Forwarding != nil {
		leaves = append(leaves, name+".forwarding")
	}
	if len(family.Addresses) > 0 {
		leaves = append(leaves, name+".address")
	}
	if family.DHCP != nil {
		leaves = append(leaves, name+".dhcp")
	}
	if leasesMetric(family) {
		leaves = append(leaves, name+".route-metric")
	}
	return leaves
}

func leasesMetric(family interfaceintent.Family) bool {
	return family.RouteMetric != nil && !family.Gateway.IsValid()
}

func delegationLeaves(delegation interfaceintent.Delegation) []string {
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

func leafLabel(leaf string) string {
	return strings.ReplaceAll(leaf, ".", " ")
}
