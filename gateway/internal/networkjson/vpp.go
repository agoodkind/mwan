package networkjson

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/vpp"
)

type vppWire struct {
	NAT64     *nat64Wire     `json:"nat64"`
	Neighbors []neighborWire `json:"neighbor"`
	Routes    []vppRouteWire `json:"route"`
}

type vppRouteWire struct {
	Interface   string  `json:"interface"`
	Destination string  `json:"destination"`
	Gateway     string  `json:"gateway"`
	TableID     *uint32 `json:"table-id"`
}

type nat64Wire struct {
	Inside  string   `json:"inside-interface"`
	Outside string   `json:"outside-interface"`
	Prefix  string   `json:"prefix"`
	Pool    []string `json:"pool-address"`
	VRF     *uint32  `json:"table-id"`
	PoolVRF *uint32  `json:"pool-table-id"`
}

type neighborWire struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
	MAC       string `json:"mac"`
}

func buildVPPNetwork(doc *document) (result *Config, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("build router VPP network configuration failed", "error", failure)
		}
	}()
	guestType, err := resolveGuestType(doc.Interfaces.GuestType)
	if err != nil {
		return nil, err
	}
	if doc.Interfaces.SteeringGroup.HashMode != "" {
		return nil, fmt.Errorf("VPP router policy cannot configure a WAN steering group")
	}
	ids, explicit, err := normalizeConnectionIDs(doc.Interfaces.Interface)
	if err != nil {
		return nil, err
	}
	policy := &vpp.Policy{Connections: nil, NAT64: nil, Neighbors: nil}
	for _, entry := range doc.Interfaces.Interface {
		if entry.WAN != nil || entry.Steering != nil || entry.ConnectionID == "" {
			return nil, fmt.Errorf("VPP interface %s requires an explicit connection ID and cannot configure WAN steering", entry.Name)
		}
		connection, err := buildConnection(entry, ids[entry.Name])
		if err != nil {
			return nil, err
		}
		policy.Connections = append(policy.Connections, connection)
	}
	policy.NAT64, err = buildNAT64Policy(doc.VPP.NAT64)
	if err != nil {
		return nil, err
	}
	for _, wire := range doc.VPP.Neighbors {
		address, err := netip.ParseAddr(wire.Address)
		if err != nil {
			return nil, fmt.Errorf("VPP neighbor address: %w", err)
		}
		mac, err := net.ParseMAC(wire.MAC)
		if err != nil {
			return nil, fmt.Errorf("VPP neighbor %s requires an Ethernet MAC: %w", wire.Address, err)
		}
		if len(mac) != 6 {
			return nil, fmt.Errorf("VPP neighbor %s requires an Ethernet MAC", wire.Address)
		}
		policy.Neighbors = append(policy.Neighbors, vpp.Neighbor{Interface: wire.Interface, Address: address, MAC: wire.MAC})
	}
	for _, wire := range doc.VPP.Routes {
		if err := addVPPRoute(policy, wire); err != nil {
			return nil, err
		}
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("validate router VPP network policy: %w", err)
	}
	loaded := new(Config)
	loaded.GuestType = guestType
	loaded.VPP = policy
	loaded.ConnectionIDs = ids
	loaded.ExplicitConnectionIDs = explicit
	loaded.Connections = []interfaceintent.Connection{}
	return loaded, nil
}

func buildNAT64Policy(wire *nat64Wire) (result *vpp.NAT64Policy, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("build router NAT64 network policy failed", "error", failure)
		}
	}()
	if wire == nil {
		return nil, nil
	}
	if wire.VRF == nil || wire.PoolVRF == nil {
		return nil, fmt.Errorf("VPP NAT64 table-id and pool-table-id are required")
	}
	prefix, err := netip.ParsePrefix(wire.Prefix)
	if err != nil {
		return nil, fmt.Errorf("VPP NAT64 prefix: %w", err)
	}
	policy := &vpp.NAT64Policy{Inside: wire.Inside, Outside: wire.Outside, Prefix: prefix, Pool: nil, VRF: *wire.VRF, PoolVRF: *wire.PoolVRF}
	for _, text := range wire.Pool {
		address, err := netip.ParseAddr(text)
		if err != nil {
			return nil, fmt.Errorf("VPP NAT64 pool address: %w", err)
		}
		policy.Pool = append(policy.Pool, address)
	}
	return policy, nil
}

func addVPPRoute(policy *vpp.Policy, wire vppRouteWire) error {
	destination, err := netip.ParsePrefix(wire.Destination)
	if err != nil || destination != destination.Masked() || destination.Addr().Is4In6() {
		return fmt.Errorf("VPP route %s requires a canonical IP prefix", wire.Destination)
	}
	gateway, err := netip.ParseAddr(wire.Gateway)
	if err != nil || gateway.Is4() != destination.Addr().Is4() || gateway.IsUnspecified() || gateway.IsMulticast() || gateway.Is4In6() {
		return fmt.Errorf("VPP route %s requires a same-family next hop", wire.Destination)
	}
	if wire.TableID == nil {
		return fmt.Errorf("VPP route %s requires table-id", wire.Destination)
	}
	for index := range policy.Connections {
		connection := &policy.Connections[index]
		if connection.Name != wire.Interface {
			continue
		}
		route := interfaceintent.RouteIntent{Destination: destination, Gateway: gateway, TableID: *wire.TableID, Metric: 0}
		if gateway.Is4() && connection.IPv4 != nil {
			connection.IPv4.Routes = append(connection.IPv4.Routes, route)
			return nil
		}
		if gateway.Is6() && connection.IPv6 != nil {
			connection.IPv6.Routes = append(connection.IPv6.Routes, route)
			return nil
		}
		return fmt.Errorf("VPP route %s requires its address family on %s", destination, wire.Interface)
	}
	return fmt.Errorf("VPP route %s requires declared interface %s", destination, wire.Interface)
}
