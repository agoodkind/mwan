package provider

import (
	"context"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

const (
	networkKeySeparator = "|"

	interfacesDescription = "The map contains interfaces keyed by the entry's name. Each entry contains name, type, enabled, owner, " +
		"connection_id, roles, and provider_name. The enabled value is null when the document omits it. " +
		"The roles set contains the applicable provider, parent, internal, and management roles. " +
		"The provider_name value is null for an interface without a provider."
	routesDescription = "The map contains configured main-table routes. Each key uses the entry's values in the format " +
		"<interface>|<family>|<destination>. The family value is ipv4 or ipv6. The gateway value is null for an on-link route. " +
		"The source value is route for a route-list entry or gateway for an interface gateway represented as the family default route."
	providerDefaultsDescription = "The map contains one provider table entry per provider family with a translation policy. Each key uses the entry's values in the format " +
		"<connection-id>|<family>|<table-id>. The connection_id value is the derived connection identifier. Each entry contains interface, family, and table_id values. " +
		"The family value is ipv4 or ipv6. The internal_destination value is internal-net-v4 for IPv4 or opnsense-edge-v6/128 for IPv6. The internal_interface value is internal-iface. The map omits the default gateway learned at runtime."
)

type networkMaps struct {
	Interfaces       types.Map `tfsdk:"interfaces"`
	Routes           types.Map `tfsdk:"routes"`
	ProviderDefaults types.Map `tfsdk:"provider_defaults"`
}

type networkInterfaceModel struct {
	Name         types.String `tfsdk:"name"`
	Type         types.String `tfsdk:"type"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	Owner        types.String `tfsdk:"owner"`
	ConnectionID types.String `tfsdk:"connection_id"`
	Roles        types.Set    `tfsdk:"roles"`
	ProviderName types.String `tfsdk:"provider_name"`
}

type networkRouteModel struct {
	Interface   types.String `tfsdk:"interface"`
	Family      types.String `tfsdk:"family"`
	Destination types.String `tfsdk:"destination"`
	Gateway     types.String `tfsdk:"gateway"`
	TableID     types.Int64  `tfsdk:"table_id"`
	Metric      types.Int64  `tfsdk:"metric"`
	Source      types.String `tfsdk:"source"`
}

type networkProviderDefaultModel struct {
	ConnectionID        types.String `tfsdk:"connection_id"`
	Interface           types.String `tfsdk:"interface"`
	Family              types.String `tfsdk:"family"`
	TableID             types.Int64  `tfsdk:"table_id"`
	InternalDestination types.String `tfsdk:"internal_destination"`
	InternalInterface   types.String `tfsdk:"internal_interface"`
}

func networkInterfaceType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":          types.StringType,
		"type":          types.StringType,
		"enabled":       types.BoolType,
		"owner":         types.StringType,
		"connection_id": types.StringType,
		"roles":         types.SetType{ElemType: types.StringType},
		"provider_name": types.StringType,
	}}
}

func networkRouteType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"interface":   types.StringType,
		"family":      types.StringType,
		"destination": types.StringType,
		"gateway":     types.StringType,
		"table_id":    types.Int64Type,
		"metric":      types.Int64Type,
		"source":      types.StringType,
	}}
}

func networkProviderDefaultType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"connection_id":        types.StringType,
		"interface":            types.StringType,
		"family":               types.StringType,
		"table_id":             types.Int64Type,
		"internal_destination": types.StringType,
		"internal_interface":   types.StringType,
	}}
}

func newNetworkMaps(ctx context.Context, config *networkjson.Config) (networkMaps, diag.Diagnostics) {
	var diagnostics diag.Diagnostics
	maps := networkMaps{
		Interfaces:       types.MapNull(networkInterfaceType()),
		Routes:           types.MapNull(networkRouteType()),
		ProviderDefaults: types.MapNull(networkProviderDefaultType()),
	}

	interfaces := make(map[string]networkInterfaceModel, len(config.Connections))
	for _, connection := range config.Connections {
		model, roleDiagnostics := newNetworkInterface(ctx, config, connection)
		diagnostics.Append(roleDiagnostics...)
		interfaces[connection.Name] = model
	}

	configuredRoutes := config.ConfiguredRoutes()
	routes := make(map[string]networkRouteModel, len(configuredRoutes))
	for key, route := range configuredRoutes {
		routes[key.String()] = newNetworkRoute(key, route)
	}

	defaults, err := config.ProviderDefaults()
	if err != nil {
		diagnostics.AddError("Invalid provider default", err.Error())
		return maps, diagnostics
	}
	providerDefaults := make(map[string]networkProviderDefaultModel, len(defaults))
	for key, value := range defaults {
		providerDefaults[key.String()] = newNetworkProviderDefault(key, value)
	}
	if diagnostics.HasError() {
		return maps, diagnostics
	}

	interfaceMap, mapDiagnostics := types.MapValueFrom(ctx, networkInterfaceType(), interfaces)
	diagnostics.Append(mapDiagnostics...)
	routeMap, mapDiagnostics := types.MapValueFrom(ctx, networkRouteType(), routes)
	diagnostics.Append(mapDiagnostics...)
	providerDefaultMap, mapDiagnostics := types.MapValueFrom(ctx, networkProviderDefaultType(), providerDefaults)
	diagnostics.Append(mapDiagnostics...)
	if diagnostics.HasError() {
		return maps, diagnostics
	}
	maps.Interfaces = interfaceMap
	maps.Routes = routeMap
	maps.ProviderDefaults = providerDefaultMap
	return maps, diagnostics
}

func newNetworkInterface(
	ctx context.Context,
	config *networkjson.Config,
	connection interfaceintent.Connection,
) (networkInterfaceModel, diag.Diagnostics) {
	roles, diagnostics := types.SetValueFrom(ctx, types.StringType, networkRoleNames(connection.Roles))
	enabled := types.BoolNull()
	if connection.Enabled != nil {
		enabled = types.BoolValue(*connection.Enabled)
	}
	providerName := types.StringNull()
	if provider, accepted := config.WAN[connection.ID.String()]; accepted {
		providerName = types.StringValue(provider.ProviderName)
	}
	return networkInterfaceModel{
		Name:         types.StringValue(connection.Name),
		Type:         types.StringValue(connection.Type),
		Enabled:      enabled,
		Owner:        types.StringValue(string(connection.Owner)),
		ConnectionID: types.StringValue(connection.ID.String()),
		Roles:        roles,
		ProviderName: providerName,
	}, diagnostics
}

func networkRoleNames(roles interfaceintent.Role) []string {
	known := []struct {
		role interfaceintent.Role
		name string
	}{
		{role: interfaceintent.RoleProvider, name: "provider"},
		{role: interfaceintent.RoleParent, name: "parent"},
		{role: interfaceintent.RoleInternal, name: "internal"},
		{role: interfaceintent.RoleManagement, name: "management"},
	}
	names := []string{}
	for _, entry := range known {
		if roles&entry.role != 0 {
			names = append(names, entry.name)
		}
	}
	return names
}

func newNetworkRoute(key networkjson.RouteKey, route networkjson.ConfiguredRoute) networkRouteModel {
	gateway := types.StringNull()
	if route.Gateway.IsValid() {
		gateway = types.StringValue(route.Gateway.String())
	}
	return networkRouteModel{
		Interface:   types.StringValue(key.Interface),
		Family:      types.StringValue(string(key.Family)),
		Destination: types.StringValue(key.Destination.String()),
		Gateway:     gateway,
		TableID:     types.Int64Value(int64(route.TableID)),
		Metric:      types.Int64Value(int64(route.Metric)),
		Source:      types.StringValue(string(route.Source)),
	}
}

func newNetworkProviderDefault(
	key networkjson.ProviderDefaultKey,
	value networkjson.ProviderDefault,
) networkProviderDefaultModel {
	return networkProviderDefaultModel{
		ConnectionID:        types.StringValue(key.ConnectionID.String()),
		Interface:           types.StringValue(value.Interface),
		Family:              types.StringValue(string(value.Family)),
		TableID:             types.Int64Value(int64(value.TableID)),
		InternalDestination: types.StringValue(value.InternalDestination.String()),
		InternalInterface:   types.StringValue(value.InternalInterface),
	}
}

func (m networkRouteModel) identity() (string, bool) {
	if !networkKnown(m.Interface, m.Family, m.Destination) {
		return "", false
	}
	return m.Interface.ValueString() + networkKeySeparator + m.Family.ValueString() +
		networkKeySeparator + m.Destination.ValueString(), true
}

func (m networkProviderDefaultModel) identity() (string, bool) {
	if !networkKnown(m.ConnectionID, m.Family) || m.TableID.IsUnknown() || m.TableID.IsNull() {
		return "", false
	}
	return m.ConnectionID.ValueString() + networkKeySeparator + m.Family.ValueString() +
		networkKeySeparator + strconv.FormatInt(m.TableID.ValueInt64(), 10), true
}

func networkKnown(values ...types.String) bool {
	for _, value := range values {
		if value.IsUnknown() || value.IsNull() {
			return false
		}
	}
	return true
}
