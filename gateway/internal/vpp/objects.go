package vpp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.fd.io/govpp/api"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/ip"
	"go.fd.io/govpp/binapi/ip_neighbor"
	"go.fd.io/govpp/binapi/memclnt"
	"go.fd.io/govpp/binapi/nat64"
	"go.fd.io/govpp/binapi/tapv2"
)

// nat64_db_init returns zero in VPP 26.06.
// The enable operation uses result 1 for an already enabled feature.
const errNat64AlreadyEnabled = api.VPPApiError(1)

type ownedObject struct {
	Interface string                               `json:"Interface"`
	Address   *interfaces.SwInterfaceAddDelAddress `json:"Address"`
	Route     *ip.IPRouteAddDel                    `json:"Route"`
	Neighbor  *ip_neighbor.IPNeighborAddDel        `json:"Neighbor"`
	NAT64     *nat64.Nat64AddDelInterface          `json:"NAT64"`
	Pool      *nat64.Nat64AddDelPoolAddrRange      `json:"Pool"`
	Prefix    *nat64.Nat64AddDelPrefix             `json:"Prefix"`
}

func (object ownedObject) validate() error {
	count := 0
	for _, present := range []bool{object.Address != nil, object.Route != nil, object.Neighbor != nil, object.NAT64 != nil, object.Pool != nil, object.Prefix != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("an ownership object must contain exactly one typed resource")
	}
	if object.Route != nil && len(object.Route.Route.Paths) != 1 {
		return fmt.Errorf("an owned VPP route requires exactly one next hop")
	}
	return nil
}

func (object ownedObject) interfaceIndex() (interface_types.InterfaceIndex, bool) {
	switch {
	case object.Address != nil:
		return object.Address.SwIfIndex, true
	case object.Route != nil && len(object.Route.Route.Paths) == 1:
		return interface_types.InterfaceIndex(object.Route.Route.Paths[0].SwIfIndex), true
	case object.Neighbor != nil:
		return object.Neighbor.Neighbor.SwIfIndex, true
	case object.NAT64 != nil:
		return object.NAT64.SwIfIndex, true
	default:
		return 0, false
	}
}

func (object ownedObject) key() string {
	switch {
	case object.Address != nil:
		return fmt.Sprintf("address/%s/%s", object.Interface, object.Address.Prefix)
	case object.Route != nil:
		route := object.Route.Route
		return fmt.Sprintf("route/%s/%d/%s/%v", object.Interface, route.TableID, route.Prefix, route.Paths)
	case object.Neighbor != nil:
		return fmt.Sprintf("neighbor/%s/%s/%s", object.Interface, object.Neighbor.Neighbor.IPAddress, object.Neighbor.Neighbor.MacAddress)
	case object.NAT64 != nil:
		return fmt.Sprintf("nat64/%s/%d", object.Interface, object.NAT64.Flags)
	case object.Pool != nil:
		return fmt.Sprintf("pool/%s/%d", object.Pool.StartAddr, object.Pool.VrfID)
	case object.Prefix != nil:
		return fmt.Sprintf("prefix/%s/%d", object.Prefix.Prefix, object.Prefix.VrfID)
	default:
		return "invalid"
	}
}

func (client *Client) configureTAP(ctx context.Context, resource TAP, index interface_types.InterfaceIndex, policy Policy) (failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "configure VPP TAP failed", "connection_id", resource.ConnectionID, "error", failure)
		}
	}()
	service := interfaces.NewServiceClient(client.connection)
	flags := interface_types.IF_STATUS_API_FLAG_ADMIN_UP
	for _, connection := range policy.Connections {
		if connection.ID.String() == resource.ConnectionID && connection.Enabled != nil && !*connection.Enabled {
			flags = 0
		}
		if connection.ID.String() == resource.ConnectionID && connection.Link != nil && connection.Link.MTU != nil {
			if _, err := service.SwInterfaceSetMtu(ctx, &interfaces.SwInterfaceSetMtu{SwIfIndex: index, Mtu: []uint32{*connection.Link.MTU, 0, 0, 0}}); err != nil {
				return fmt.Errorf("set VPP interface MTU: %w", err)
			}
		}
	}
	if _, err := service.SwInterfaceSetFlags(ctx, &interfaces.SwInterfaceSetFlags{SwIfIndex: index, Flags: flags}); err != nil {
		return fmt.Errorf("set VPP TAP state: %w", err)
	}
	if _, err := service.SwInterfaceSetRxMode(ctx, &interfaces.SwInterfaceSetRxMode{SwIfIndex: index, Mode: resource.RXMode}); err != nil {
		return fmt.Errorf("set VPP TAP RX mode: %w", err)
	}
	for queue := range uint32(resource.Create.NumRxQueues) {
		if _, err := service.SwInterfaceSetRxPlacement(ctx, &interfaces.SwInterfaceSetRxPlacement{SwIfIndex: index, QueueID: queue, WorkerID: resource.Worker}); err != nil {
			return fmt.Errorf("set VPP TAP queue placement: %w", err)
		}
	}
	return nil
}

func (client *Client) ensureNAT64(ctx context.Context, resources nat64.Nat64PluginEnableDisable) (created bool, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "enable VPP NAT64 failed", "error", failure)
		}
	}()
	stream, err := nat64.NewServiceClient(client.connection).Nat64InterfaceDump(ctx, &nat64.Nat64InterfaceDump{})
	entries, err := collect(stream, err)
	if err != nil {
		return false, fmt.Errorf("read NAT64 interfaces: %w", err)
	}
	if len(entries) > 0 {
		return false, nil
	}
	resources.Enable = true
	if _, err := nat64.NewServiceClient(client.connection).Nat64PluginEnableDisable(ctx, &resources); err != nil {
		if errors.Is(err, errNat64AlreadyEnabled) {
			return false, nil
		}
		return false, fmt.Errorf("enable NAT64 before configuring its first interface: %w", err)
	}
	return true, nil
}

func (client *Client) applyObject(ctx context.Context, object ownedObject, add bool) (failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "apply VPP resource failed", "resource", object.key(), "error", failure)
		}
	}()
	var err error
	switch {
	case object.Address != nil:
		request := *object.Address
		request.IsAdd = add
		_, err = interfaces.NewServiceClient(client.connection).SwInterfaceAddDelAddress(ctx, &request)
	case object.Route != nil:
		request := *object.Route
		request.IsAdd = add
		_, err = ip.NewServiceClient(client.connection).IPRouteAddDel(ctx, &request)
	case object.Neighbor != nil:
		request := *object.Neighbor
		request.IsAdd = add
		_, err = ip_neighbor.NewServiceClient(client.connection).IPNeighborAddDel(ctx, &request)
	case object.NAT64 != nil:
		request := *object.NAT64
		request.IsAdd = add
		_, err = nat64.NewServiceClient(client.connection).Nat64AddDelInterface(ctx, &request)
	case object.Pool != nil:
		request := *object.Pool
		request.IsAdd = add
		_, err = nat64.NewServiceClient(client.connection).Nat64AddDelPoolAddrRange(ctx, &request)
	case object.Prefix != nil:
		request := *object.Prefix
		request.IsAdd = add
		_, err = nat64.NewServiceClient(client.connection).Nat64AddDelPrefix(ctx, &request)
	default:
		return fmt.Errorf("invalid VPP ownership object")
	}
	if err != nil {
		return fmt.Errorf("configure VPP %s: %w", object.key(), err)
	}
	return nil
}

func requiredMessages() []api.Message {
	return []api.Message{
		&memclnt.ControlPing{}, &memclnt.ControlPingReply{},
		&tapv2.TapCreateV3{}, &tapv2.TapCreateV3Reply{}, &tapv2.TapDeleteV2{}, &tapv2.TapDeleteV2Reply{},
		&tapv2.SwInterfaceTapV2Dump{}, &tapv2.SwInterfaceTapV2Details{},
		&interfaces.SwInterfaceDump{}, &interfaces.SwInterfaceDetails{},
		&interfaces.SwInterfaceSetFlags{}, &interfaces.SwInterfaceSetFlagsReply{},
		&interfaces.SwInterfaceSetMtu{}, &interfaces.SwInterfaceSetMtuReply{},
		&interfaces.SwInterfaceSetRxMode{}, &interfaces.SwInterfaceSetRxModeReply{},
		&interfaces.SwInterfaceSetRxPlacement{}, &interfaces.SwInterfaceSetRxPlacementReply{},
		&interfaces.SwInterfaceAddDelAddress{}, &interfaces.SwInterfaceAddDelAddressReply{},
		&ip.IPAddressDump{}, &ip.IPAddressDetails{}, &ip.IPRouteDump{}, &ip.IPRouteDetails{}, &ip.IPRouteAddDel{}, &ip.IPRouteAddDelReply{},
		&ip_neighbor.IPNeighborDump{}, &ip_neighbor.IPNeighborDetails{}, &ip_neighbor.IPNeighborAddDel{}, &ip_neighbor.IPNeighborAddDelReply{},
		&nat64.Nat64PluginEnableDisable{}, &nat64.Nat64PluginEnableDisableReply{},
		&nat64.Nat64InterfaceDump{}, &nat64.Nat64InterfaceDetails{}, &nat64.Nat64AddDelInterface{}, &nat64.Nat64AddDelInterfaceReply{},
		&nat64.Nat64PoolAddrDump{}, &nat64.Nat64PoolAddrDetails{}, &nat64.Nat64AddDelPoolAddrRange{}, &nat64.Nat64AddDelPoolAddrRangeReply{},
		&nat64.Nat64PrefixDump{}, &nat64.Nat64PrefixDetails{}, &nat64.Nat64AddDelPrefix{}, &nat64.Nat64AddDelPrefixReply{},
	}
}
