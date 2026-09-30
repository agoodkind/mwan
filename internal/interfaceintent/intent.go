// Package interfaceintent defines the configured interface and acquired assignment contracts.
package interfaceintent

import (
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

// Owner selects the component responsible for an interface.
type Owner string

const (
	// OwnerExternal assigns interface management to an external component.
	OwnerExternal Owner = "external"
	// OwnerNetworkd assigns interface management to networkd.
	OwnerNetworkd Owner = "networkd"
	// OwnerMWAN assigns interface management to mwan.
	OwnerMWAN Owner = "mwan"
)

// Role marks an interface function in the gateway.
type Role uint8

const (
	// RoleProvider marks a steering provider.
	RoleProvider Role = 1 << iota
	// RoleParent marks a parent of another interface.
	RoleParent
	// RoleInternal marks the router-facing interface.
	RoleInternal
	// RoleManagement marks a management interface.
	RoleManagement
)

// Kind identifies the configured link form.
type Kind string

const (
	// KindPhysical selects a physical link.
	KindPhysical Kind = "physical"
	// KindVLAN selects a tagged VLAN link.
	KindVLAN Kind = "vlan"
	// KindBridge selects a bridge link.
	KindBridge Kind = "bridge"
)

// AddressPurpose identifies how the gateway uses an address.
type AddressPurpose string

const (
	// PurposeLocal marks an address assigned locally.
	PurposeLocal AddressPurpose = "local"
	// PurposeForward marks an address forwarded to the edge.
	PurposeForward AddressPurpose = "forward-to-edge"
)

// AddressDelivery identifies how an address is assigned to the gateway.
type AddressDelivery string

const (
	// DeliveryLocal marks an address assigned on this gateway.
	DeliveryLocal AddressDelivery = "local"
	// DeliveryRouted marks an address routed to this gateway.
	DeliveryRouted AddressDelivery = "routed"
)

// Connection defines one configured interface and its network intent.
type Connection struct {
	ID         connectionid.ID
	Name       string
	Type       string
	Enabled    *bool
	Roles      Role
	Owner      Owner
	Link       *Link
	IPv4       *IPv4
	IPv6       *IPv6
	LeaseStore string
	Networkd   []UnitFile
}

// Link defines the device identity and link settings for a connection.
type Link struct {
	Kind            Kind
	Match           Match
	HardwareAddress string
	MTU             *uint32
	VLAN            *VLAN
	BridgeMaster    string
}

// Match selects a physical device by driver or hardware address.
type Match struct {
	Driver          string
	HardwareAddress string
}

// VLAN defines a tagged child interface and its parent.
type VLAN struct {
	Parent string
	ID     uint16
}

// Address defines a static prefix and its purpose.
type Address struct {
	Prefix  netip.Prefix
	Purpose AddressPurpose
}

// Family defines settings shared by IPv4 and IPv6.
type Family struct {
	Enabled       *bool
	Forwarding    *bool
	Addresses     []Address
	DHCP          *bool
	Gateway       netip.Addr
	RouteMetric   *uint32
	Routes        []RouteIntent
	DNS           []netip.Addr
	SearchDomains []string
}

// IPv4 defines IPv4 settings for a connection.
type IPv4 struct {
	Family
	SourceAddresses []netip.Addr
	DHCPv4          *DHCPv4
}

// IPv6 defines IPv6 settings for a connection.
type IPv6 struct {
	Family
	AcceptRA             *bool
	AutoConf             *bool
	AcceptRADefaultRoute *bool
	UseRADNS             *bool
	Delegation           *Delegation
	DHCPv6               *DHCPv6
	ForwardingAddresses  []ForwardingAddress
}

// DHCPv4 defines IPv4 client options.
type DHCPv4 struct {
	ClientID  string
	UseDNS    *bool
	UseRoutes *bool
}

// DHCPv6 defines IPv6 client options.
type DHCPv6 struct {
	DUIDType       string
	DUID           string
	IANAIAID       *uint32
	IAPDIAID       *uint32
	RequestAddress *bool
	RequestPrefix  *bool
	WithoutRA      string
	UseDNS         *bool
}

// Delegation defines an IPv6 prefix request.
type Delegation struct {
	Hint                  netip.Prefix
	DUIDType              string
	DUID                  string
	WithoutRA             string
	UseDelegatedPrefix    *bool
	RouterLifetimeSeconds *uint32
	IAID                  *uint32
}

// ForwardingAddress defines an IPv6 address forwarded by an interface.
type ForwardingAddress struct {
	Address  netip.Addr
	Delivery AddressDelivery
}

// UnitFile defines free-form networkd sections for one file kind.
type UnitFile struct {
	Kind     string
	Sections []UnitSection
}

// UnitSection defines one ordered networkd section.
type UnitSection struct {
	Index   uint16
	Name    string
	Entries []UnitEntry
}

// UnitEntry defines one ordered networkd key and value.
type UnitEntry struct {
	Index uint16
	Key   string
	Value string
}

// ResourceKind identifies a network resource claimed by a writer.
type ResourceKind string

const (
	// ResourceLink identifies a network link.
	ResourceLink ResourceKind = "link"
	// ResourceStaticAddress identifies a configured address.
	ResourceStaticAddress ResourceKind = "static-address"
	// ResourceAcquiredAddress identifies a leased address.
	ResourceAcquiredAddress ResourceKind = "acquired-address"
	// ResourceMappedAddress identifies a translated address.
	ResourceMappedAddress ResourceKind = "mapped-address"
	// ResourceNPTExternalAddress identifies an NPT external address.
	ResourceNPTExternalAddress ResourceKind = "npt-external-address"
	// ResourceMainRoute identifies a route in the main table.
	ResourceMainRoute ResourceKind = "main-route"
	// ResourceProviderRoute identifies a provider route.
	ResourceProviderRoute ResourceKind = "provider-route"
	// ResourcePolicyRule identifies a policy routing rule.
	ResourcePolicyRule ResourceKind = "policy-rule"
	// ResourceDHCPv4 identifies an IPv4 DHCP client.
	ResourceDHCPv4 ResourceKind = "dhcpv4"
	// ResourceDHCPv6 identifies an IPv6 DHCP client.
	ResourceDHCPv6 ResourceKind = "dhcpv6"
	// ResourceRASLAAC identifies router advertisement address setup.
	ResourceRASLAAC ResourceKind = "ra-slaac"
)

// Writer identifies the component that manages a resource.
type Writer string

const (
	// WriterExternal identifies an external resource writer.
	WriterExternal Writer = "external"
	// WriterNetworkd identifies the networkd resource writer.
	WriterNetworkd Writer = "networkd"
	// WriterMWANLink identifies the mwan link writer.
	WriterMWANLink Writer = "mwan-link"
	// WriterMWANAddress identifies the mwan address writer.
	WriterMWANAddress Writer = "mwan-address"
	// WriterMWANProtocol identifies the mwan protocol writer.
	WriterMWANProtocol Writer = "mwan-protocol"
	// WriterMWANRoute identifies the mwan route writer.
	WriterMWANRoute Writer = "mwan-route"
	// WriterWANRoutes identifies the provider route writer.
	WriterWANRoutes Writer = "wan-routes"
	// WriterNPT identifies the NPT address writer.
	WriterNPT Writer = "npt"
	// WriterKernel identifies the kernel resource writer.
	WriterKernel Writer = "kernel"
)

// Claim assigns one network resource to a writer and connection.
type Claim struct {
	ConnectionID connectionid.ID
	Kind         ResourceKind
	Family       string
	Key          string
	Writer       Writer
}

// AssignmentKind identifies the source of an acquired assignment.
type AssignmentKind string

const (
	// AssignmentStatic identifies a configured static assignment.
	AssignmentStatic AssignmentKind = "static"
	// AssignmentStaticRoute identifies a configured main-table route.
	AssignmentStaticRoute AssignmentKind = "static-route"
	// AssignmentDHCPv4 identifies an IPv4 DHCP assignment.
	AssignmentDHCPv4 AssignmentKind = "dhcpv4"
	// AssignmentDHCPv6IANA identifies an IPv6 DHCP IA NA assignment.
	AssignmentDHCPv6IANA AssignmentKind = "dhcpv6-ia-na"
	// AssignmentDHCPv6IAPD identifies an IPv6 DHCP IA PD assignment.
	AssignmentDHCPv6IAPD AssignmentKind = "dhcpv6-ia-pd"
	// AssignmentSLAAC identifies a stateless IPv6 address assignment.
	AssignmentSLAAC AssignmentKind = "slaac"
	// AssignmentRARoute identifies a router advertisement route.
	AssignmentRARoute AssignmentKind = "ra-route"
	// AssignmentMapped identifies a translated address assignment.
	AssignmentMapped AssignmentKind = "mapped"
	// AssignmentNPTExternal identifies an NPT external assignment.
	AssignmentNPTExternal AssignmentKind = "npt-external"
)

// RouteIntent defines an assigned route independently of its source.
type RouteIntent struct {
	Destination netip.Prefix
	Gateway     netip.Addr
	TableID     uint32
	Metric      uint32
}

// Assignment records an address or route assigned to a connection.
type Assignment struct {
	ConnectionID   connectionid.ID
	Family         string
	Kind           AssignmentKind
	Source         string
	Purpose        AddressPurpose
	Value          netip.Prefix
	Route          *RouteIntent
	ClientID       string
	DUID           string
	IAID           *uint32
	AcquiredAt     time.Time
	RenewAt        *time.Time
	RebindAt       *time.Time
	PreferredUntil *time.Time
	ValidUntil     *time.Time
	Valid          bool
}
