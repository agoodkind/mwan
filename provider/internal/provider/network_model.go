package provider

import (
	"context"
	"net/netip"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/firewall"
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
	policyRulesDescription = "The map contains provider policy rules derived from the network configuration. " +
		"Each key uses the entry's values in the format <connection-id>|<family>|<kind>. " +
		"Key encoding replaces % with %25 and | with %7C inside each value. " +
		"The family value is ipv4 or ipv6. The kind value is fwmark or source. Each entry contains priority and table_id values. " +
		"The mark value is null for a source rule. The source_kind value identifies the rule's source kind. " +
		"The source value contains the rule's source prefix or null when the prefix is invalid. " +
		"The activation_conditions list contains the rule's conditions in their original order."
	firewallChainsDescription = "The map contains firewall chains from the configuration's firewall plan. " +
		"Each key uses the entry's values in the format <family>|<table>|<chain>. " +
		"Key encoding replaces % with %25 and | with %7C inside each value. " +
		"The rule_order list contains firewall_rules keys in the order specified by the firewall plan."
	firewallRulesDescription = "The map contains firewall rules from the configuration's firewall plan. " +
		"Each key uses the entry's values in the format <family>|<table>|<chain>|<purpose>|<scope>. " +
		"Key encoding replaces % with %25 and | with %7C inside each value. " +
		"The key value equals the map key. The expression value contains the nftables rule text. " +
		"The interface value selects the input interface. The output_interface value selects the output interface. " +
		"The source and destination values are null for invalid prefixes. The interface, output_interface, and mark values are null when absent. " +
		"The chain's rule_order list specifies the rule's position."
	firewallSetsDescription = "The map contains firewall sets from the configuration's firewall plan. " +
		"Each key uses the entry's values in the format <family>|<table>|<set>. " +
		"Key encoding replaces % with %25 and | with %7C inside each value. " +
		"The key_type value specifies the nftables element type. The elements set contains the plan's prefixes. " +
		"The elements set does not preserve prefix order."
	guestTypeDescription = "The guest_type attribute contains the decoded guest type."
)

type networkMaps struct {
	Interfaces       types.Map `tfsdk:"interfaces"`
	Routes           types.Map `tfsdk:"routes"`
	ProviderDefaults types.Map `tfsdk:"provider_defaults"`
	PolicyRules      types.Map `tfsdk:"policy_rules"`
	FirewallChains   types.Map `tfsdk:"firewall_chains"`
	FirewallRules    types.Map `tfsdk:"firewall_rules"`
	FirewallSets     types.Map `tfsdk:"firewall_sets"`
}

type networkPolicyRuleModel struct {
	ConnectionID         types.String `tfsdk:"connection_id"`
	Family               types.String `tfsdk:"family"`
	Kind                 types.String `tfsdk:"kind"`
	Priority             types.Int64  `tfsdk:"priority"`
	TableID              types.Int64  `tfsdk:"table_id"`
	Mark                 types.Int64  `tfsdk:"mark"`
	Source               types.String `tfsdk:"source"`
	SourceKind           types.String `tfsdk:"source_kind"`
	ActivationConditions types.List   `tfsdk:"activation_conditions"`
}

type networkFirewallChainModel struct {
	Family    types.String `tfsdk:"family"`
	Table     types.String `tfsdk:"table"`
	Chain     types.String `tfsdk:"chain"`
	RuleOrder types.List   `tfsdk:"rule_order"`
}

type networkFirewallRuleModel struct {
	Key             types.String `tfsdk:"key"`
	Family          types.String `tfsdk:"family"`
	Table           types.String `tfsdk:"table"`
	Chain           types.String `tfsdk:"chain"`
	Purpose         types.String `tfsdk:"purpose"`
	Scope           types.String `tfsdk:"scope"`
	Action          types.String `tfsdk:"action"`
	Expression      types.String `tfsdk:"expression"`
	Source          types.String `tfsdk:"source"`
	Destination     types.String `tfsdk:"destination"`
	Interface       types.String `tfsdk:"interface"`
	OutputInterface types.String `tfsdk:"output_interface"`
	Mark            types.Int64  `tfsdk:"mark"`
}

type networkFirewallSetModel struct {
	Family   types.String `tfsdk:"family"`
	Table    types.String `tfsdk:"table"`
	Set      types.String `tfsdk:"set"`
	KeyType  types.String `tfsdk:"key_type"`
	Elements types.Set    `tfsdk:"elements"`
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

func networkPolicyRuleType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"connection_id":         types.StringType,
		"family":                types.StringType,
		"kind":                  types.StringType,
		"priority":              types.Int64Type,
		"table_id":              types.Int64Type,
		"mark":                  types.Int64Type,
		"source":                types.StringType,
		"source_kind":           types.StringType,
		"activation_conditions": types.ListType{ElemType: types.StringType},
	}}
}

func networkFirewallChainType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"family":     types.StringType,
		"table":      types.StringType,
		"chain":      types.StringType,
		"rule_order": types.ListType{ElemType: types.StringType},
	}}
}

func networkFirewallRuleType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"key":              types.StringType,
		"family":           types.StringType,
		"table":            types.StringType,
		"chain":            types.StringType,
		"purpose":          types.StringType,
		"scope":            types.StringType,
		"action":           types.StringType,
		"expression":       types.StringType,
		"source":           types.StringType,
		"destination":      types.StringType,
		"interface":        types.StringType,
		"output_interface": types.StringType,
		"mark":             types.Int64Type,
	}}
}

func networkFirewallSetType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"family":   types.StringType,
		"table":    types.StringType,
		"set":      types.StringType,
		"key_type": types.StringType,
		"elements": types.SetType{ElemType: types.StringType},
	}}
}

func newNetworkMaps(ctx context.Context, config *networkjson.Config) (networkMaps, diag.Diagnostics) {
	var diagnostics diag.Diagnostics
	maps := networkMaps{
		Interfaces:       types.MapNull(networkInterfaceType()),
		Routes:           types.MapNull(networkRouteType()),
		ProviderDefaults: types.MapNull(networkProviderDefaultType()),
		PolicyRules:      types.MapNull(networkPolicyRuleType()),
		FirewallChains:   types.MapNull(networkFirewallChainType()),
		FirewallRules:    types.MapNull(networkFirewallRuleType()),
		FirewallSets:     types.MapNull(networkFirewallSetType()),
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
	diagnostics.Append(maps.setPolicyRules(ctx, config)...)
	diagnostics.Append(maps.setFirewall(ctx, config)...)
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

func (m *networkMaps) setPolicyRules(ctx context.Context, config *networkjson.Config) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	configuredPolicyRules, err := config.PolicyRules()
	if err != nil {
		diagnostics.AddError("Invalid policy rule", err.Error())
		return diagnostics
	}
	policyRules := make(map[string]networkPolicyRuleModel, len(configuredPolicyRules))
	for key, rule := range configuredPolicyRules {
		model, ruleDiagnostics := newNetworkPolicyRule(ctx, key, rule)
		diagnostics.Append(ruleDiagnostics...)
		policyRules[key.String()] = model
	}
	policyRuleMap, mapDiagnostics := types.MapValueFrom(ctx, networkPolicyRuleType(), policyRules)
	diagnostics.Append(mapDiagnostics...)
	if diagnostics.HasError() {
		return diagnostics
	}
	m.PolicyRules = policyRuleMap
	return diagnostics
}

func (m *networkMaps) setFirewall(ctx context.Context, config *networkjson.Config) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	firewallPlan, err := config.FirewallPlan()
	if err != nil {
		diagnostics.AddError("Invalid firewall configuration", err.Error())
		return diagnostics
	}
	firewallChains := make(map[string]networkFirewallChainModel, len(firewallPlan.Chains))
	for key, chain := range firewallPlan.Chains {
		model, chainDiagnostics := newNetworkFirewallChain(ctx, key, chain)
		diagnostics.Append(chainDiagnostics...)
		firewallChains[key.String()] = model
	}
	firewallRules := make(map[string]networkFirewallRuleModel, len(firewallPlan.Rules))
	for key, rule := range firewallPlan.Rules {
		firewallRules[key.String()] = newNetworkFirewallRule(key, rule)
	}
	firewallSets := make(map[string]networkFirewallSetModel, len(firewallPlan.Sets))
	for key, set := range firewallPlan.Sets {
		model, setDiagnostics := newNetworkFirewallSet(ctx, key, set)
		diagnostics.Append(setDiagnostics...)
		firewallSets[key.String()] = model
	}
	firewallChainMap, mapDiagnostics := types.MapValueFrom(ctx, networkFirewallChainType(), firewallChains)
	diagnostics.Append(mapDiagnostics...)
	firewallRuleMap, mapDiagnostics := types.MapValueFrom(ctx, networkFirewallRuleType(), firewallRules)
	diagnostics.Append(mapDiagnostics...)
	firewallSetMap, mapDiagnostics := types.MapValueFrom(ctx, networkFirewallSetType(), firewallSets)
	diagnostics.Append(mapDiagnostics...)
	if diagnostics.HasError() {
		return diagnostics
	}
	m.FirewallChains = firewallChainMap
	m.FirewallRules = firewallRuleMap
	m.FirewallSets = firewallSetMap
	return diagnostics
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

func newNetworkPolicyRule(
	ctx context.Context,
	key networkjson.PolicyRuleKey,
	rule interfaceintent.PolicyRule,
) (networkPolicyRuleModel, diag.Diagnostics) {
	// Source rules use null because mark applies only to fwmark rules.
	mark := types.Int64Null()
	if rule.Kind == interfaceintent.PolicyRuleFwmark {
		mark = types.Int64Value(int64(rule.Mark))
	}
	conditions := make([]string, 0, len(rule.ActivationConditions))
	for _, condition := range rule.ActivationConditions {
		conditions = append(conditions, string(condition))
	}
	activationConditions, diagnostics := types.ListValueFrom(ctx, types.StringType, conditions)
	return networkPolicyRuleModel{
		ConnectionID:         types.StringValue(key.ConnectionID.String()),
		Family:               types.StringValue(string(key.Family)),
		Kind:                 types.StringValue(string(key.Kind)),
		Priority:             types.Int64Value(int64(rule.Priority)),
		TableID:              types.Int64Value(int64(rule.TableID)),
		Mark:                 mark,
		Source:               networkPrefix(rule.Source),
		SourceKind:           types.StringValue(string(rule.SourceKind)),
		ActivationConditions: activationConditions,
	}, diagnostics
}

func newNetworkFirewallChain(
	ctx context.Context,
	key networkjson.FirewallChainKey,
	chain networkjson.FirewallChain,
) (networkFirewallChainModel, diag.Diagnostics) {
	order := make([]string, 0, len(chain.RuleOrder))
	for _, ruleKey := range chain.RuleOrder {
		order = append(order, ruleKey.String())
	}
	ruleOrder, diagnostics := types.ListValueFrom(ctx, types.StringType, order)
	return networkFirewallChainModel{
		Family:    types.StringValue(key.Family),
		Table:     types.StringValue(key.Table),
		Chain:     types.StringValue(key.Chain),
		RuleOrder: ruleOrder,
	}, diagnostics
}

func newNetworkFirewallRule(key networkjson.FirewallRuleKey, rule firewall.Rule) networkFirewallRuleModel {
	iface := types.StringNull()
	if rule.Interface != "" {
		iface = types.StringValue(rule.Interface)
	}
	outputInterface := types.StringNull()
	if rule.OutputInterface != "" {
		outputInterface = types.StringValue(rule.OutputInterface)
	}
	mark := types.Int64Null()
	if rule.Mark != nil {
		mark = types.Int64Value(int64(*rule.Mark))
	}
	return networkFirewallRuleModel{
		Key:             types.StringValue(key.String()),
		Family:          types.StringValue(key.Family),
		Table:           types.StringValue(key.Table),
		Chain:           types.StringValue(key.Chain),
		Purpose:         types.StringValue(string(key.Purpose)),
		Scope:           types.StringValue(key.Scope),
		Action:          types.StringValue(string(rule.Action)),
		Expression:      types.StringValue(rule.Expression),
		Source:          networkPrefix(rule.Source),
		Destination:     networkPrefix(rule.Destination),
		Interface:       iface,
		OutputInterface: outputInterface,
		Mark:            mark,
	}
}

func newNetworkFirewallSet(
	ctx context.Context,
	key networkjson.FirewallSetKey,
	set firewall.Set,
) (networkFirewallSetModel, diag.Diagnostics) {
	members := make([]string, 0, len(set.Elements))
	for _, element := range set.Elements {
		members = append(members, element.String())
	}
	elements, diagnostics := types.SetValueFrom(ctx, types.StringType, members)
	return networkFirewallSetModel{
		Family:   types.StringValue(key.Family),
		Table:    types.StringValue(key.Table),
		Set:      types.StringValue(key.Set),
		KeyType:  types.StringValue(set.KeyType),
		Elements: elements,
	}, diagnostics
}

func networkPrefix(prefix netip.Prefix) types.String {
	if !prefix.IsValid() {
		return types.StringNull()
	}
	return types.StringValue(prefix.String())
}

func (m networkPolicyRuleModel) identity() (string, bool) {
	if !networkKnown(m.ConnectionID, m.Family, m.Kind) {
		return "", false
	}
	key := networkjson.PolicyRuleKey{
		ConnectionID: connectionid.ID(m.ConnectionID.ValueString()),
		Family:       networkjson.Family(m.Family.ValueString()),
		Kind:         interfaceintent.PolicyRuleKind(m.Kind.ValueString()),
	}
	return key.String(), true
}

func (m networkFirewallChainModel) identity() (string, bool) {
	return networkChainIdentity(m.Family, m.Table, m.Chain)
}

func (m networkFirewallRuleModel) identity() (string, bool) {
	if !networkKnown(m.Family, m.Table, m.Chain, m.Purpose, m.Scope) {
		return "", false
	}
	key := networkjson.FirewallRuleKey{
		Family: m.Family.ValueString(), Table: m.Table.ValueString(), Chain: m.Chain.ValueString(),
		Purpose: firewall.RulePurpose(m.Purpose.ValueString()), Scope: m.Scope.ValueString(),
	}
	return key.String(), true
}

func (m networkFirewallRuleModel) chainIdentity() (string, bool) {
	return networkChainIdentity(m.Family, m.Table, m.Chain)
}

func networkChainIdentity(family types.String, table types.String, chain types.String) (string, bool) {
	if !networkKnown(family, table, chain) {
		return "", false
	}
	key := networkjson.FirewallChainKey{
		Family: family.ValueString(), Table: table.ValueString(), Chain: chain.ValueString(),
	}
	return key.String(), true
}

func (m networkFirewallSetModel) identity() (string, bool) {
	if !networkKnown(m.Family, m.Table, m.Set) {
		return "", false
	}
	key := networkjson.FirewallSetKey{
		Family: m.Family.ValueString(), Table: m.Table.ValueString(), Set: m.Set.ValueString(),
	}
	return key.String(), true
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
