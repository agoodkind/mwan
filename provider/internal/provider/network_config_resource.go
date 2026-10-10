package provider

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

type networkConfigResource struct{}

func newNetworkConfigResource() resource.Resource {
	return &networkConfigResource{}
}

func (r *networkConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network_config"
}

const (
	networkConfigSchemaVersion      int64 = 1
	networkConfigRouteSchemaVersion int64 = 0

	networkUint32Maximum int64 = math.MaxUint32
)

// The version 0 state upgrader uses these attributes to decode prior state.
func networkConfigRouteAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"interfaces": schema.MapAttribute{
			Required:    true,
			ElementType: networkInterfaceType(),
			Description: interfacesDescription,
		},
		"routes": schema.MapAttribute{
			Required:    true,
			ElementType: networkRouteType(),
			Description: routesDescription,
		},
		"provider_defaults": schema.MapAttribute{
			Required:    true,
			ElementType: networkProviderDefaultType(),
			Description: providerDefaultsDescription,
		},
	}
}

func emptyNetworkMap(elementType types.ObjectType) types.Map {
	return types.MapValueMust(elementType, map[string]attr.Value{})
}

func optionalNetworkMap(elementType types.ObjectType, description string) schema.MapAttribute {
	return schema.MapAttribute{
		Optional:    true,
		Computed:    true,
		ElementType: elementType,
		Description: description,
		Default:     mapdefault.StaticValue(emptyNetworkMap(elementType)),
	}
}

func (r *networkConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := networkConfigRouteAttributes()
	attributes["policy_rules"] = optionalNetworkMap(networkPolicyRuleType(), policyRulesDescription)
	attributes["firewall_chains"] = optionalNetworkMap(networkFirewallChainType(), firewallChainsDescription)
	attributes["firewall_rules"] = optionalNetworkMap(networkFirewallRuleType(), firewallRulesDescription)
	attributes["firewall_sets"] = optionalNetworkMap(networkFirewallSetType(), firewallSetsDescription)
	resp.Schema = schema.Schema{
		Version: networkConfigSchemaVersion,
		Description: "The resource stores planned network maps in OpenTofu state. " +
			"The resource makes no network call and writes no guest file.",
		Attributes: attributes,
	}
}

func (r *networkConfigResource) UpgradeState(_ context.Context) map[int64]resource.StateUpgrader {
	routeSchema := schema.Schema{Attributes: networkConfigRouteAttributes()}
	return map[int64]resource.StateUpgrader{
		networkConfigRouteSchemaVersion: {
			PriorSchema:   &routeSchema,
			StateUpgrader: upgradeNetworkConfigRouteState,
		},
	}
}

// The upgrader initializes the rule maps because version 0 state predates those attributes.
func upgradeNetworkConfigRouteState(
	ctx context.Context,
	req resource.UpgradeStateRequest,
	resp *resource.UpgradeStateResponse,
) {
	upgraded := networkMaps{
		Interfaces:       types.MapNull(networkInterfaceType()),
		Routes:           types.MapNull(networkRouteType()),
		ProviderDefaults: types.MapNull(networkProviderDefaultType()),
		PolicyRules:      emptyNetworkMap(networkPolicyRuleType()),
		FirewallChains:   emptyNetworkMap(networkFirewallChainType()),
		FirewallRules:    emptyNetworkMap(networkFirewallRuleType()),
		FirewallSets:     emptyNetworkMap(networkFirewallSetType()),
	}
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("interfaces"), &upgraded.Interfaces)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("routes"), &upgraded.Routes)...)
	resp.Diagnostics.Append(
		req.State.GetAttribute(ctx, path.Root("provider_defaults"), &upgraded.ProviderDefaults)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &upgraded)...)
}

func (r *networkConfigResource) ValidateConfig(
	ctx context.Context,
	req resource.ValidateConfigRequest,
	resp *resource.ValidateConfigResponse,
) {
	var configured networkMaps
	resp.Diagnostics.Append(req.Config.Get(ctx, &configured)...)
	if resp.Diagnostics.HasError() {
		return
	}
	forEachNetworkEntry(ctx, configured.Interfaces, path.Root("interfaces"), &resp.Diagnostics,
		func(key string, entryPath path.Path, model networkInterfaceModel, diagnostics *diag.Diagnostics) {
			if networkKnown(model.Name) && key != model.Name.ValueString() {
				addKeyMismatch(diagnostics, entryPath, key, model.Name.ValueString())
			}
		})
	forEachNetworkEntry(ctx, configured.Routes, path.Root("routes"), &resp.Diagnostics,
		func(key string, entryPath path.Path, model networkRouteModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
			validateNetworkFamily(diagnostics, entryPath, model.Family)
			validateRouteSource(diagnostics, entryPath, model.Source)
		})
	forEachNetworkEntry(ctx, configured.ProviderDefaults, path.Root("provider_defaults"), &resp.Diagnostics,
		func(key string, entryPath path.Path, model networkProviderDefaultModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
			validateNetworkFamily(diagnostics, entryPath, model.Family)
		})
	validateRuleMaps(ctx, configured, &resp.Diagnostics)
	validateFirewallMaps(ctx, configured, &resp.Diagnostics)
}

func validateRuleMaps(ctx context.Context, configured networkMaps, diagnostics *diag.Diagnostics) {
	forEachNetworkEntry(ctx, configured.PolicyRules, path.Root("policy_rules"), diagnostics,
		func(key string, entryPath path.Path, model networkPolicyRuleModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
			validateNetworkFamily(diagnostics, entryPath, model.Family)
			validatePolicyRule(diagnostics, entryPath, model)
		})
	forEachNetworkEntry(ctx, configured.FirewallSets, path.Root("firewall_sets"), diagnostics,
		func(key string, entryPath path.Path, model networkFirewallSetModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
		})
}

func validateFirewallMaps(ctx context.Context, configured networkMaps, diagnostics *diag.Diagnostics) {
	chainOrders := map[string]firewallChainOrder{}
	forEachNetworkEntry(ctx, configured.FirewallChains, path.Root("firewall_chains"), diagnostics,
		func(key string, entryPath path.Path, model networkFirewallChainModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
			order, known := knownRuleOrder(ctx, model.RuleOrder, diagnostics)
			if known {
				chainOrders[key] = firewallChainOrder{path: entryPath.AtName("rule_order"), keys: order}
			}
		})
	ruleChains := map[string]string{}
	forEachNetworkEntry(ctx, configured.FirewallRules, path.Root("firewall_rules"), diagnostics,
		func(key string, entryPath path.Path, model networkFirewallRuleModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
			if networkKnown(model.Key) && key != model.Key.ValueString() {
				addKeyMismatch(diagnostics, entryPath.AtName("key"), key, model.Key.ValueString())
			}
			validateFirewallAction(diagnostics, entryPath, model.Action)
			validateNetworkUint32(diagnostics, entryPath.AtName("mark"), model.Mark)
			if chain, known := model.chainIdentity(); known {
				ruleChains[key] = chain
			}
		})
	// Unknown chain orders or rule chain identities defer rule order validation.
	chains, rules := configured.FirewallChains, configured.FirewallRules
	chainsKnown := !chains.IsUnknown() && len(chainOrders) == len(chains.Elements())
	rulesKnown := !rules.IsUnknown() && len(ruleChains) == len(rules.Elements())
	if chainsKnown && rulesKnown {
		validateFirewallRuleOrder(diagnostics, chainOrders, ruleChains)
	}
}

type firewallChainOrder struct {
	path path.Path
	keys []string
}

func knownRuleOrder(ctx context.Context, ruleOrder types.List, diagnostics *diag.Diagnostics) ([]string, bool) {
	if ruleOrder.IsUnknown() {
		return nil, false
	}
	var entries []types.String
	entryDiagnostics := ruleOrder.ElementsAs(ctx, &entries, false)
	diagnostics.Append(entryDiagnostics...)
	if entryDiagnostics.HasError() || !networkKnown(entries...) {
		return nil, false
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.ValueString())
	}
	return keys, true
}

func validateFirewallRuleOrder(
	diagnostics *diag.Diagnostics,
	chainOrders map[string]firewallChainOrder,
	ruleChains map[string]string,
) {
	listed := make(map[string]bool, len(ruleChains))
	for _, chainKey := range slices.Sorted(maps.Keys(chainOrders)) {
		chain := chainOrders[chainKey]
		seen := make(map[string]bool, len(chain.keys))
		for i, ruleKey := range chain.keys {
			entryPath := chain.path.AtListIndex(i)
			owner, found := ruleChains[ruleKey]
			switch {
			case seen[ruleKey]:
				diagnostics.AddAttributeError(entryPath, "Duplicated firewall rule order entry",
					fmt.Sprintf("The chain %q lists the rule key %q more than once.", chainKey, ruleKey))
			case !found:
				diagnostics.AddAttributeError(entryPath, "Unknown firewall rule order entry",
					fmt.Sprintf("The rule key %q has no firewall_rules entry.", ruleKey))
			case owner != chainKey:
				diagnostics.AddAttributeError(entryPath, "Firewall rule order entry from another chain",
					fmt.Sprintf("The rule key %q belongs to the chain %q, not %q.", ruleKey, owner, chainKey))
			default:
				listed[ruleKey] = true
			}
			seen[ruleKey] = true
		}
	}
	rulesPath := path.Root("firewall_rules")
	for _, ruleKey := range slices.Sorted(maps.Keys(ruleChains)) {
		if listed[ruleKey] {
			continue
		}
		diagnostics.AddAttributeError(rulesPath.AtMapKey(ruleKey), "Firewall rule missing from the chain order",
			fmt.Sprintf("The rule_order list of the chain %q must contain the rule key %q.",
				ruleChains[ruleKey], ruleKey))
	}
}

func validatePolicyRule(diagnostics *diag.Diagnostics, entryPath path.Path, model networkPolicyRuleModel) {
	kinds := []interfaceintent.PolicyRuleKind{interfaceintent.PolicyRuleFwmark, interfaceintent.PolicyRuleSource}
	validateNetworkValue(diagnostics, entryPath.AtName("kind"), model.Kind, kinds)
	sourceKinds := []interfaceintent.PolicySourceKind{
		interfaceintent.PolicySourceNone,
		interfaceintent.PolicySourceConfigured,
		interfaceintent.PolicySourceRuntime,
	}
	validateNetworkValue(diagnostics, entryPath.AtName("source_kind"), model.SourceKind, sourceKinds)
	validateNetworkUint32(diagnostics, entryPath.AtName("priority"), model.Priority)
	validateNetworkUint32(diagnostics, entryPath.AtName("table_id"), model.TableID)
	validateNetworkUint32(diagnostics, entryPath.AtName("mark"), model.Mark)
}

func validateFirewallAction(diagnostics *diag.Diagnostics, entryPath path.Path, action types.String) {
	allowed := []firewall.RuleAction{
		firewall.ActionAccept, firewall.ActionDrop, firewall.ActionReturn, firewall.ActionMark,
		firewall.ActionSaveMark, firewall.ActionDNAT, firewall.ActionSNAT, firewall.ActionMasquerade,
	}
	validateNetworkValue(diagnostics, entryPath.AtName("action"), action, allowed)
}

func validateNetworkUint32(diagnostics *diag.Diagnostics, valuePath path.Path, value types.Int64) {
	if value.IsUnknown() || value.IsNull() {
		return
	}
	if number := value.ValueInt64(); number < 0 || number > networkUint32Maximum {
		diagnostics.AddAttributeError(
			valuePath,
			"Network value outside the uint32 range",
			fmt.Sprintf("The value %d must be between 0 and %d.", number, networkUint32Maximum),
		)
	}
}

type networkEntryModel interface {
	networkInterfaceModel | networkRouteModel | networkProviderDefaultModel | networkPolicyRuleModel |
		networkFirewallChainModel | networkFirewallRuleModel | networkFirewallSetModel
}

func forEachNetworkEntry[Model networkEntryModel](
	ctx context.Context,
	entries types.Map,
	mapPath path.Path,
	diagnostics *diag.Diagnostics,
	check func(key string, entryPath path.Path, model Model, diagnostics *diag.Diagnostics),
) {
	if entries.IsUnknown() || entries.IsNull() {
		return
	}
	for key, element := range entries.Elements() {
		entryPath := mapPath.AtMapKey(key)
		if element.IsUnknown() {
			continue
		}
		if element.IsNull() {
			diagnostics.AddAttributeError(entryPath, "Null network map entry", "Each map entry must be an object.")
			continue
		}
		object, isObject := element.(types.Object)
		if !isObject {
			diagnostics.AddAttributeError(entryPath, "Invalid network map entry", fmt.Sprintf("got %T", element))
			continue
		}
		var model Model
		objectDiagnostics := object.As(ctx, &model, basetypes.ObjectAsOptions{
			UnhandledNullAsEmpty:    false,
			UnhandledUnknownAsEmpty: false,
		})
		diagnostics.Append(objectDiagnostics...)
		if objectDiagnostics.HasError() {
			continue
		}
		check(key, entryPath, model, diagnostics)
	}
}

func addKeyMismatch(diagnostics *diag.Diagnostics, entryPath path.Path, key string, identity string) {
	diagnostics.AddAttributeError(
		entryPath,
		"Network map key differs from the entry identity",
		fmt.Sprintf("The key %q must equal %q, which the entry's fields derive.", key, identity),
	)
}

func validateNetworkFamily(diagnostics *diag.Diagnostics, entryPath path.Path, family types.String) {
	allowed := []networkjson.Family{networkjson.FamilyIPv4, networkjson.FamilyIPv6}
	validateNetworkValue(diagnostics, entryPath.AtName("family"), family, allowed)
}

func validateRouteSource(diagnostics *diag.Diagnostics, entryPath path.Path, source types.String) {
	allowed := []networkjson.RouteSource{networkjson.RouteSourceRoute, networkjson.RouteSourceGateway}
	validateNetworkValue(diagnostics, entryPath.AtName("source"), source, allowed)
}

func validateNetworkValue[Value ~string](
	diagnostics *diag.Diagnostics,
	valuePath path.Path,
	value types.String,
	allowed []Value,
) {
	if value.IsUnknown() {
		return
	}
	for _, candidate := range allowed {
		if !value.IsNull() && value.ValueString() == string(candidate) {
			return
		}
	}
	diagnostics.AddAttributeError(
		valuePath,
		"Invalid network value",
		fmt.Sprintf("The value %s must be one of %q.", value.String(), allowed),
	)
}

func (r *networkConfigResource) Create(_ context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.State.Raw = req.Plan.Raw
}

// Read returns the framework's prior state because the resource has no remote state to refresh.
func (r *networkConfigResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {
}

func (r *networkConfigResource) Update(_ context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State.Raw = req.Plan.Raw
}

func (r *networkConfigResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.State.RemoveResource(ctx)
}
