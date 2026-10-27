package vpp

import (
	"context"
	"fmt"
	"log/slog"

	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/ip"
	"go.fd.io/govpp/binapi/ip_neighbor"
	"go.fd.io/govpp/binapi/ip_types"
	"go.fd.io/govpp/binapi/nat64"
)

func (client *Client) objectPresent(ctx context.Context, object ownedObject) (present bool, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "inspect VPP resource failed", "resource", object.key(), "error", failure)
		}
	}()
	var err error
	switch {
	case object.Address != nil:
		present, err = client.addressPresent(ctx, object.Address)
	case object.Route != nil:
		present, err = client.routePresent(ctx, object.Route.Route)
	case object.Neighbor != nil:
		present, err = client.neighborPresent(ctx, object.Neighbor.Neighbor)
	case object.NAT64 != nil:
		present, err = client.nat64InterfacePresent(ctx, object.NAT64)
	case object.Pool != nil:
		present, err = client.nat64PoolPresent(ctx, object.Pool)
	case object.Prefix != nil:
		present, err = client.nat64PrefixPresent(ctx, object.Prefix)
	default:
		return false, fmt.Errorf("invalid VPP ownership object")
	}
	if err != nil {
		return false, fmt.Errorf("inspect VPP %s: %w", object.key(), err)
	}
	return present, nil
}

func (client *Client) addressPresent(ctx context.Context, request *interfaces.SwInterfaceAddDelAddress) (bool, error) {
	stream, err := ip.NewServiceClient(client.connection).IPAddressDump(ctx, &ip.IPAddressDump{SwIfIndex: request.SwIfIndex, IsIPv6: request.Prefix.Address.Af == ip_types.ADDRESS_IP6})
	entries, err := collect(stream, err)
	for _, entry := range entries {
		if entry.Prefix == request.Prefix {
			return true, nil
		}
	}
	return false, err
}

func (client *Client) routePresent(ctx context.Context, request ip.IPRoute) (bool, error) {
	stream, err := ip.NewServiceClient(client.connection).IPRouteDump(ctx, &ip.IPRouteDump{Table: ip.IPTable{TableID: request.TableID, IsIP6: request.Prefix.Address.Af == ip_types.ADDRESS_IP6}})
	entries, err := collect(stream, err)
	for _, entry := range entries {
		if entry.Route.Prefix != request.Prefix {
			continue
		}
		for _, path := range entry.Route.Paths {
			want := request.Paths[0]
			if path.SwIfIndex == want.SwIfIndex && path.Nh.Address == want.Nh.Address && path.Proto == want.Proto && path.Weight == want.Weight {
				return true, nil
			}
		}
	}
	return false, err
}

func (client *Client) neighborPresent(ctx context.Context, request ip_neighbor.IPNeighbor) (bool, error) {
	stream, err := ip_neighbor.NewServiceClient(client.connection).IPNeighborDump(ctx, &ip_neighbor.IPNeighborDump{SwIfIndex: request.SwIfIndex, Af: request.IPAddress.Af})
	entries, err := collect(stream, err)
	for _, entry := range entries {
		if entry.Neighbor.IPAddress != request.IPAddress {
			continue
		}
		if entry.Neighbor.MacAddress != request.MacAddress {
			return false, fmt.Errorf("VPP neighbor %s already uses another MAC", request.IPAddress)
		}
		return true, nil
	}
	return false, err
}

func (client *Client) nat64InterfacePresent(ctx context.Context, request *nat64.Nat64AddDelInterface) (bool, error) {
	stream, err := nat64.NewServiceClient(client.connection).Nat64InterfaceDump(ctx, &nat64.Nat64InterfaceDump{})
	entries, err := collect(stream, err)
	for _, entry := range entries {
		if entry.SwIfIndex == request.SwIfIndex && entry.Flags&request.Flags != 0 {
			return true, nil
		}
	}
	return false, err
}

func (client *Client) nat64PoolPresent(ctx context.Context, request *nat64.Nat64AddDelPoolAddrRange) (bool, error) {
	stream, err := nat64.NewServiceClient(client.connection).Nat64PoolAddrDump(ctx, &nat64.Nat64PoolAddrDump{})
	entries, err := collect(stream, err)
	for _, entry := range entries {
		if entry.Address != request.StartAddr {
			continue
		}
		if entry.VrfID != request.VrfID {
			return false, fmt.Errorf("NAT64 pool %s belongs to table %d", entry.Address, entry.VrfID)
		}
		return true, nil
	}
	return false, err
}

func (client *Client) nat64PrefixPresent(ctx context.Context, request *nat64.Nat64AddDelPrefix) (bool, error) {
	stream, err := nat64.NewServiceClient(client.connection).Nat64PrefixDump(ctx, &nat64.Nat64PrefixDump{})
	entries, err := collect(stream, err)
	for _, entry := range entries {
		if entry.VrfID != request.VrfID {
			continue
		}
		if entry.Prefix != request.Prefix {
			return false, fmt.Errorf("NAT64 table %d already uses prefix %s", entry.VrfID, entry.Prefix)
		}
		return true, nil
	}
	return false, err
}
