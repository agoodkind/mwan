package vpp

import (
	"fmt"
	"log/slog"
	"net"

	"go.fd.io/govpp/binapi/ethernet_types"
	"go.fd.io/govpp/binapi/fib_types"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/ip"
	"go.fd.io/govpp/binapi/ip_neighbor"
	"go.fd.io/govpp/binapi/ip_types"
	"go.fd.io/govpp/binapi/nat64"
	"go.fd.io/govpp/binapi/nat_types"

	"goodkind.io/mwan/internal/interfaceintent"
)

func newOwnedObject(name string) ownedObject {
	return ownedObject{Interface: name, Address: nil, Route: nil, Neighbor: nil, NAT64: nil, Pool: nil, Prefix: nil}
}

func marshalDesiredObjects(policy Policy, indices map[string]interface_types.InterfaceIndex) ([]ownedObject, error) {
	var result []ownedObject
	for _, connection := range policy.Connections {
		index, mapped := indices[connection.Name]
		if !mapped {
			continue
		}
		objects, err := marshalConnectionObjects(connection, index)
		if err != nil {
			return nil, err
		}
		result = append(result, objects...)
	}
	for _, neighbor := range policy.Neighbors {
		object, err := marshalNeighborObject(neighbor, indices)
		if err != nil {
			return nil, err
		}
		result = append(result, object)
	}
	objects, err := marshalNAT64Objects(policy.NAT64, indices)
	if err != nil {
		return nil, err
	}
	return append(result, objects...), nil
}

func marshalConnectionObjects(connection interfaceintent.Connection, index interface_types.InterfaceIndex) ([]ownedObject, error) {
	var result []ownedObject
	for _, family := range []*interfaceintent.Family{ipv4Family(connection.IPv4), ipv6Family(connection.IPv6)} {
		if family == nil {
			continue
		}
		objects, err := marshalFamilyObjects(connection.Name, index, *family)
		if err != nil {
			return nil, err
		}
		result = append(result, objects...)
	}
	return result, nil
}

func marshalFamilyObjects(name string, index interface_types.InterfaceIndex, family interfaceintent.Family) (result []ownedObject, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("convert VPP interface policy failed", "interface", name, "error", failure)
		}
	}()
	for _, address := range family.Addresses {
		prefix, err := ip_types.ParseAddressWithPrefix(address.Prefix.String())
		if err != nil {
			return nil, fmt.Errorf("convert VPP address: %w", err)
		}
		object := newOwnedObject(name)
		object.Address = &interfaces.SwInterfaceAddDelAddress{SwIfIndex: index, Prefix: prefix}
		result = append(result, object)
	}
	if family.Gateway.IsValid() {
		return nil, fmt.Errorf("VPP interface %s requires an explicit route with a VPP table-id instead of a Linux gateway leaf", name)
	}
	for _, route := range family.Routes {
		object, err := marshalRouteObject(name, index, route)
		if err != nil {
			return nil, err
		}
		result = append(result, object)
	}
	return result, nil
}

func marshalRouteObject(name string, index interface_types.InterfaceIndex, route interfaceintent.RouteIntent) (result ownedObject, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("convert VPP route policy failed", "interface", name, "error", failure)
		}
	}()
	prefix, err := ip_types.ParsePrefix(route.Destination.String())
	if err != nil {
		return result, fmt.Errorf("convert VPP route: %w", err)
	}
	nextHop := ip_types.NewAddress(net.IP(route.Gateway.AsSlice()))
	protocol := fib_types.FIB_API_PATH_NH_PROTO_IP4
	if route.Destination.Addr().Is6() {
		protocol = fib_types.FIB_API_PATH_NH_PROTO_IP6
	}
	path := fib_types.FibPath{SwIfIndex: uint32(index), Weight: 1, Proto: protocol, Nh: fib_types.FibPathNh{Address: nextHop.Un}}
	result = newOwnedObject(name)
	result.Route = &ip.IPRouteAddDel{IsMultipath: true, Route: ip.IPRoute{TableID: route.TableID, Prefix: prefix, Paths: []fib_types.FibPath{path}}}
	return result, nil
}

func marshalNeighborObject(neighbor Neighbor, indices map[string]interface_types.InterfaceIndex) (result ownedObject, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("convert VPP neighbor policy failed", "interface", neighbor.Interface, "error", failure)
		}
	}()
	index, mapped := indices[neighbor.Interface]
	if !mapped {
		return result, fmt.Errorf("VPP neighbor requires a managed interface %s", neighbor.Interface)
	}
	mac, err := ethernet_types.ParseMacAddress(neighbor.MAC)
	if err != nil {
		return result, fmt.Errorf("convert VPP neighbor MAC: %w", err)
	}
	result = newOwnedObject(neighbor.Interface)
	result.Neighbor = &ip_neighbor.IPNeighborAddDel{Neighbor: ip_neighbor.IPNeighbor{SwIfIndex: index, Flags: ip_neighbor.IP_API_NEIGHBOR_FLAG_STATIC, MacAddress: mac, IPAddress: ip_types.NewAddress(net.IP(neighbor.Address.AsSlice()))}}
	return result, nil
}

func marshalNAT64Objects(policy *NAT64Policy, indices map[string]interface_types.InterfaceIndex) (result []ownedObject, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("convert NAT64 policy failed", "error", failure)
		}
	}()
	if policy == nil {
		return nil, nil
	}
	for _, entry := range []struct {
		name string
		flag nat_types.NatConfigFlags
	}{{policy.Inside, nat_types.NAT_IS_INSIDE}, {policy.Outside, nat_types.NAT_IS_OUTSIDE}} {
		index, exists := indices[entry.name]
		if !exists {
			return nil, fmt.Errorf("NAT64 requires managed TAP %s", entry.name)
		}
		object := newOwnedObject(entry.name)
		object.NAT64 = &nat64.Nat64AddDelInterface{SwIfIndex: index, Flags: entry.flag}
		result = append(result, object)
	}
	prefix, err := ip_types.ParseIP6Prefix(policy.Prefix.String())
	if err != nil {
		return nil, fmt.Errorf("convert NAT64 prefix: %w", err)
	}
	object := newOwnedObject("")
	object.Prefix = &nat64.Nat64AddDelPrefix{Prefix: prefix, VrfID: policy.VRF}
	result = append(result, object)
	for _, address := range policy.Pool {
		value := ip_types.IP4Address(address.As4())
		pool := newOwnedObject("")
		pool.Pool = &nat64.Nat64AddDelPoolAddrRange{StartAddr: value, EndAddr: value, VrfID: policy.PoolVRF}
		result = append(result, pool)
	}
	return result, nil
}
