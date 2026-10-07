# Native network configuration planning contract

OpenTofu must validate known network documents against the authoritative YANG schema and show configured route, policy-rule, and firewall changes by stable identity. Keep one network.json source document per gateway. Do not require an HCL rewrite or independently applied route resources.

At baseline commit `37205aae3716c41c131f3d4cc24de7224a751918`, MWAN PR #202 has merged. Provider planning uses canonicalization and Go decoding. Apply and gateway startup use native network loading. Native provider validation and typed rule projections remain required work. This contract supersedes pure-Go provider packaging and permission for known schema errors to pass planning.

## Validation authority

Use direct cgo/libyang for provider validation on darwin/arm64, linux/amd64, and linux/arm64. Run the complete authoritative YANG validation and shared Go semantic validation whenever `content` is known. Reject null content. Defer unknown content through normal OpenTofu data-source evaluation; dependent attributes remain unknown until evaluation succeeds.

Planning must make no guest, API, or network calls. Planning must write no deployment files. An isolated temporary schema directory is permitted when embedded module loading requires files. Delete that directory when the schema handle closes.

Use the existing embedded [schema](../../../gateway/internal/yangpub/schema), module inventory, and enabled features. Preserve `LYD_PARSE_STRICT`, `LYD_PARSE_NO_STATE`, `LYD_VALIDATE_PRESENT`, and `LYD_VALIDATE_NO_STATE`. Unknown nodes, invalid enums, invalid ranges, missing mandatory nodes in present containers, and state nodes must fail validation. Do not create another YANG validator or permit a `GO_MK_CGO_OPTIONAL` bypass.

Native library and schema availability are mandatory. Initialization failures must report the missing dependency or schema cause. Never substitute semantic-only decoding after native initialization fails.

Reject malformed JSON, invalid UTF-8, trailing data, duplicate decoded member names, case-fold-equivalent member names, and lone UTF-16 surrogate escapes before native validation. Preserve the existing canonicalization rejection behavior.

Introduce a minimal libyang-only package without sysrepo, daemon services, Linux netlink, or global contexts. Reuse the existing binding and its lifecycle semantics. Keep embedded schema metadata authoritative; do not maintain another module inventory or schema-directory constant.

Share byte validation and decoding between provider evaluation and gateway loading. Validate and decode identical bytes. Preserve gateway path-qualified errors, logging, and existing provider-entry rejection behavior. Provider evaluation must convert every `Config.Rejected` entry into an error diagnostic with the interface and reason when available.

Apply must validate staged canonical bytes before installation. Gateway startup must retain native validation. Valid original and canonical documents must produce equal semantic configurations. Standalone `Decode` behavior does not establish schema acceptance.

## Canonical content and semantic order

Keep `Canonicalize` formatting-only. Remove insignificant whitespace and sort object members by decoded name. Preserve every array order, string value, and numeric literal. Use `json.Number` rather than floating-point conversion.

Typed maps ignore system-ordered interface and route list ordering. Typed values normalize configured defaults and address representations through shared decoding. Preserve user-defined and operational ordering, including networkd sections and entries, translation static mappings, DNS servers, health targets, and firewall rules. Treat reserved tables as a set.

Whitespace and object-member reordering must produce no canonical file change or typed-state change. System-ordered array reordering may produce a canonical file update while typed maps remain equal. Explicit defaults and equivalent IPv6 spelling may likewise change the canonical file without changing typed values.

A canonical file update changes the file write ID and restarts both network consumers through Configs. Do not promise that every semantically equivalent array reordering avoids a restart. Typed map changes and canonical file changes may appear in the same plan.

## Existing configured projections

Reuse `interfaceintent.Connection`, `interfaceintent.RouteIntent`, and existing normalized derivation. Preserve the current interfaces, routes, and provider_defaults contracts.

| Projection | Identity and values |
| --- | --- |
| `interfaces` | Key by interface name. Include name, type, enabled, owner, connection_id, roles, and provider_name. |
| `routes` | Use `<interface>|<family>|<destination>`. Include interface, family, destination, nullable gateway, table_id, metric, and source. |
| `provider_defaults` | Use `<connection-id>|<family>|<table-id>`. Include connection_id, interface, family, table_id, internal_destination, and internal_interface. |

Use `ipv4` and `ipv6` family values. Format prefixes with `netip.Prefix.String()`. Preserve null for absent enabled values, non-provider provider names, and on-link gateways.

Configured route destinations must be canonical same-family prefixes without host bits. Gateways must satisfy existing same-family and special-address checks. Configured route table IDs default to 254 and must equal 254. Metrics default to 0 and must satisfy uint32 checks. Preserve existing conflict detection, including the IPv6 metric 0 and 1024 equivalence used by kernel reconciliation.

Include interface gateway shorthand under the corresponding default prefix, `0.0.0.0/0` or `::/0`. Use table 254 and the configured route-metric, defaulting to 0. Preserve `source = "gateway"` for shorthand and `source = "route"` for route-list entries. Reject a route-list default combined with shorthand for the same family.

Preserve owner defaults. An absent owner becomes networkd when link-files is configured and external otherwise. Resolve connection identity from explicit connection-id, then provider name, then interface name.

Provider defaults exist only for configured translation families. DHCP or an IP container alone must not create an entry. Use the configured provider table. Derive the IPv4 internal destination from internal-net-v4 and the IPv6 internal destination from opnsense-edge-v6 with prefix length 128. Use internal-iface for both.

Keep learned provider gateways and installed provider-default metric behavior outside this projection. Interface gateway shorthand and route-metric affect main-table configured routes. Preserve explicit provider table uint32 checks and invalid internal-destination diagnostics. Retain the error format `wan %s: table-id %d is outside 0 to %d`.

## Configured policy rules

Add `policy_rules` from shared configuration-only derivation. Use `<connection-id>|<family>|<kind>` for each key. Keep mutable priority, mark, table, and selector values out of keys.

Use one shared identity encoder for all new keys. Encode `%` as `%25` and `|` as `%7C` inside each component before joining components with `|`. Keep existing `routes` and `provider_defaults` keys unchanged for compatibility.

Share derivation with wanroutes. Separate configuration calculation from runtime readiness and eligibility. Do not duplicate wanroutes rule calculations in the provider.

Expose `connection_id`, `family`, `kind`, `priority`, `table_id`, `mark`, `source`, `source_kind`, and `activation_conditions`. Use `fwmark` or `source` for `kind` in this scope. Use `none`, `configured`, or `runtime` for `source_kind`. Use `activation_conditions` to express shared daemon prerequisites. Use null for concrete values unavailable from configuration.

Do not read health results, discovered gateways, leases, delegated prefixes, or runtime translation results during planning. Do not invent kernel selector values. Exclude runtime-selected catch-all provider choice and health-selected fallback rules. Preserve a distinction between a configured conditional rule and a currently installed rule.

## Configured firewall projections

Add `firewall_chains`, `firewall_rules`, and applicable `firewall_sets` projections from the shared firewall compiler. Do not introduce a general firewall language.

Refactor `firewall.Compile` to return structured rule metadata with stable source identities. Preserve generated rules and final renderer validation. Runtime nft rendering and provider projection must consume the same compiler result.

Use `<family>|<table>|<chain>` for `firewall_chains`, `<family>|<table>|<chain>|<purpose>|<scope>` for `firewall_rules`, and `<family>|<table>|<set>` for `firewall_sets`. Encode each component with the shared identity encoder. Derive distinguishable source identities from existing YANG source keys and logical compiler purposes. Keep mutable fields out of new keys. Changing a natural source identity may remove one key and add another.

Never identify rules by a hash of the complete expression, kernel handles, or whole-chain position. Include deterministic occurrence disambiguation in `scope` for indistinguishable duplicates within the same source group. Do not require new user JSON IDs. Do not promise distinct identities for indistinguishable duplicates across arbitrary edits.

Expose `key`, `family`, `table`, `chain`, `purpose`, `scope`, `action`, and canonical `expression`. Include applicable typed match fields from existing compiler inputs, including `source`, `destination`, `interface`, and `mark` when applicable. Use null when a field does not apply. Each chain object must include an ordered `rule_order` list of rule keys. Rule order is separate from rule identity. Require every projected firewall rule key exactly once in its owning chain's `rule_order`. Reject missing, duplicated, or cross-chain keys. Require full compiler output coverage.

Project configured set definitions and configured members when the compiler uses sets. Exclude observed kernel handles and runtime-populated membership. Preserve the compiler's distinction between configured definitions and runtime values.

## Provider state and plan behavior

The `mwan_network` data source returns canonical_content and all configured projections. Return guest_type from shared decoding with its shared default; apply no provider-specific guest-type rule.

The `mwan_network_config` resource stores configured values in OpenTofu state. Preserve required interfaces, routes, and provider_defaults maps. Introduce schema version 1 with new optional maps defaulting to empty for compatibility. Upgrade version 0 state without changing existing route identities. The first state upgrade adds newly tracked rule maps. State additions alone do not prove new kernel rules. Do not force a document write or restart for a map-only state update when canonical content is unchanged. Use the recorded maps for subsequent typed comparisons. Preserve existing file/binary `write_id` behavior. Official Configs integration must supply every new projection.

Use shared object types and conversion for both provider objects. Validate identity-derived keys, allowed values, numeric ranges, and chain-order references. Keep configured attributes independent of computed copies. Add no import support or replacement modifiers.

Create and Update store planned values. Read preserves prior configured state without reading a guest or kernel. Delete removes state. OpenTofu performs ordinary typed map and list diffing; do not build another diff engine. Libyang tree-diff APIs are optional only for a required diagnostic and must match the pinned API.

The following examples are illustrative and have not been executed. Public plan tests must establish exact rendering.

```text
routes["bgp0|ipv4|198.51.100.0/24"].gateway
  "192.0.2.2" -> "192.0.2.3"
policy_rules["att|ipv4|fwmark"].priority
  100 -> 110
firewall_rules["inet|mangle|prerouting|provider-mark|att"].mark
  1 -> 2
```

Reordering firewall rules changes each chain's `rule_order` without changing rule keys.

Route additions and removals must appear under their route keys. Field changes must update existing keys in place. A pending canonical-file dependency must not hide known typed changes.

Keep outputs non-sensitive and exclude free-form networkd entries from typed maps. canonical_content includes the whole document. Secrets placed in free-form entries would appear in file-content plans; the steering schema defines no secret leaf.

## Native release packages

Publish providers for darwin/arm64, linux/amd64, and linux/arm64 with their native runtime dependencies. Prefer static libyang and PCRE2. Require target-specific proof before selecting static packaging. Permit correctly bundled libraries with relative loader paths when static packaging fails that proof.

Do not apply Linux full-static or libc assumptions to macOS. The provider must not link sysrepo. End users must not need a C compiler or separately installed libyang or PCRE2.

Pin native source commits and reuse one authoritative version set across gateway and provider builds. Preserve gateway static libyang/sysrepo behavior. Preserve signed archives, checksums, attestations, and complete platform publication.

Provider binaries, embedded schemas, and deployed mwan binaries must match the same release commit. Preserve release-tag commit matching and embedded role-schema output.

## Configs installation and runtime boundary

Preserve the existing [Configs integration](https://github.com/agoodkind/configs). Configs supplies a known per-gateway source document, installs canonical_content through pveguest_file, and records typed state only after the validated file write succeeds.

Validate staged content with the deployed release's check-network and check-firewall commands. Reconcile exact CLI syntax to that release. The deploy-gate rename belongs to another workstream. A userspace plan cannot establish kernel namespace or nft acceptance.

Preserve prerequisite ordering from host kernel-module support through container creation, binary and package installation, schema and role installation, staged validation, and unit startup. Verify required LXC capabilities and live isolated firewall execution.

Both mwan-ifmgr@wan.service and mwan-agent.service restart from network write_id changes. Retain binary and package restart dependencies. Container configuration must not depend on mutable document content or force guest replacement.

Retire each gateway's Ansible document writer before its first OpenTofu file adoption. pveguest_file detects installed-file drift. The configured-state resource detects no kernel drift. Live reload and live route mutation remain deferred; stable identities and prior state must support later updates without replacement.

Failed staged validation deletes the staged file and preserves the installed file, sha256, write_id, and prior typed state. The next plan must show the pending configured changes. A validate-only update does not establish staged-content acceptance. A failed service restart uses existing rollback and watchdog recovery.

## Acceptance

Reuse [network-min.json](../../../gateway/yang/instances/network-min.json) and [network-freeform.json](../../../gateway/yang/instances/network-freeform.json) unchanged.

| Public boundary | Required observable |
| --- | --- |
| Provider protocol 6 | Known schema and semantic errors fail; null fails; unknown input defers. |
| Shared loader | Valid original and canonical bytes decode equally; gateway errors and rejection behavior remain compatible. |
| Real OpenTofu plan | Route and rule additions, removals, field changes, and order changes update in place. |
| Version 0 state upgrade | Existing routes retain keys; new maps initialize empty. |
| Shared compiler and runtime rendering | Structured projection and rendered firewall use identical rules and order. |
| Published-format package with filesystem mirror | tofu init and plan succeed without compiler access or separately installed native libraries. |
| Failed staged content write | Installed file and typed state retain prior values. |
| Live LXC content write | Both deployed checks succeed before startup; both consumers restart from write_id. |

Clean-host package and live deployment results are required evidence, not existing results. Coordinate deployment windows and locks per environment. Separate QA, production, and PowerEdge deployments do not block each other. Completion requires reviewed shared semantics, signed commits, current ruleset compliance, and evidence for each acceptance boundary.
