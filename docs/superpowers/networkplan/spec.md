> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Network configuration planning contract

OpenTofu must show configured route additions, removals, and field changes by stable key. Equivalent whitespace and object member order must produce no configuration or file change.

## Current state

The repository does not implement this design yet.

| Source evidence | Current behavior |
| --- | --- |
| [gateway/internal/networkjson/networkjson.go](../../../gateway/internal/networkjson/networkjson.go), lines 9 through 10 and 303 through 333 | `Load` reads the file, validates its bytes with libyang, unmarshals the document, and builds the configuration. The package has no build tag. |
| [gateway/internal/yangpub/schema_cgo.go](../../../gateway/internal/yangpub/schema_cgo.go), lines 3 through 8, 30 through 33, 66, and 117 | Schema validation requires cgo and libyang. Parsing uses `LYD_PARSE_STRICT`, `LYD_PARSE_NO_STATE`, and `LYD_VALIDATE_PRESENT`. A `CGO_ENABLED=0` build of `networkjson` fails because the native validation symbols are unavailable. |
| [provider/Makefile](../../../provider/Makefile), lines 12 and 23 | The provider builds with `CGO_ENABLED=0` and uses the gateway module through the workspace. |
| [provider/internal/provider/provider.go](../../../provider/internal/provider/provider.go), lines 88 through 90 | `Resources()` returns nil. The provider has no managed network configuration resource. |
| [gateway/internal/networkjson/routes.go](../../../gateway/internal/networkjson/routes.go), lines 11 through 30 | Route parsing already defaults the main table to 254 and the metric to 0. An absent gateway represents an on-link route. |
| [gateway/internal/interfaceintent/routes.go](../../../gateway/internal/interfaceintent/routes.go), lines 9 through 43 | Route validation rejects host bits, invalid same-family gateways, and conflicting configured routes. IPv6 metrics 0 and 1024 conflict because the kernel stores metric 0 as 1024. |
| [gateway/internal/networkjson/intent.go](../../../gateway/internal/networkjson/intent.go), lines 810 through 845 | `claimFamilyResources` creates a main-table default route claim for an interface gateway and a claim for each route-list entry. `ValidateConfiguredRoutes` rejects a `/0` route-list entry when the same family also sets an interface gateway. |

Configs currently uses Ansible to render and install the network document. OpenTofu does not consume the document.

The interface manager reads its configuration once during startup. File changes require a restart of `mwan-ifmgr@wan.service`.

An OpenTofu 1.12.6 experiment established that a typed route map produces keyed changes under `~ update in-place`. A JSON string produces a positional string diff. A data source with known inputs produces no plan entry.

## Contract

### Configuration input and schema authority

Keep `network.json` as the configuration source. OpenTofu must consume the existing document through `file(...)` or `jsondecode(file(...))`; the design must not require network definitions to be rewritten in HCL.

Accept the existing RFC 7951 JSON encoding rooted at `ietf-interfaces:interfaces`. Interfaces contain their `ietf-ip:ipv4` and `ietf-ip:ipv6` containers. Configured route lists use the `goodkind-mwan-steering` namespace.

The authoritative schema is [goodkind-mwan-steering@2026-10-01.yang](../../../gateway/internal/yangpub/schema/goodkind-mwan-steering@2026-10-01.yang) and its imported `ietf-interfaces`, `ietf-ip`, `ietf-nat`, `ietf-inet-types`, `ietf-yang-types`, `ietf-routing`, and `iana-if-type` modules.

The schema defines configured routes in lines 838 through 848 and 953 through 963. Each route list uses `destination` as its key. Interface gateway and route metric leaves appear in lines 826 through 837 and 941 through 952.

| Configured route field | Required behavior |
| --- | --- |
| `destination` | Require a canonical network prefix in the interface family's address family. Reject host bits. |
| `gateway` | Accept an optional valid same-family address. An absent value means on-link. |
| `table-id` | Require 254 when present. Default to 254 when absent. |
| `metric` | Accept a uint32. Default to 0 when absent. |

Preserve the existing owner default. An absent owner becomes `networkd` when `link-files` is configured; otherwise, the owner becomes `external`.

An omitted route default and its explicit value must produce equal typed outputs. Canonical content must retain the explicit value when the source contains it.

Unknown or misspelled JSON members remain subject to strict libyang validation. `Decode` must retain the existing Go unmarshaling behavior. For example, `Decode` ignores a route member named `via` and interprets the missing `gateway` as on-link; native validation must reject `via` before installation.

`Decode` must treat an explicit JSON null like an absent member. Go `encoding/json` sets pointers, maps, slices, and interfaces to nil for null and leaves other Go types unchanged. The document uses pointers for scalars that must distinguish absence from zero. Libyang schema validation at apply time must decide whether a null leaf is valid.

### Canonical content and collection order

`Canonicalize(data []byte) ([]byte, error)` must remove insignificant whitespace and sort object members by member name. It must reject invalid JSON and duplicate member names within an object.

`Canonicalize` must preserve every array order, string value, and number literal. Number handling must use `json.Number`. Canonicalization must not interpret the schema.

| Collection | Meaning of order |
| --- | --- |
| Interface list and configured route lists | The typed plan maps must ignore list order because the lists are keyed and system-ordered. |
| Networkd file sections and section entries | Preserve order because the schema uses `ordered-by user`. |
| Translation static mappings | Preserve order because the schema uses `ordered-by user`. |
| Resolver DNS servers, health targets, and reserved tables | Preserve operational order. DNS preference and probe order depend on the configured sequence. |
| Every JSON array in `canonical_content` | Preserve source order, including system-ordered lists. |

Reordering interfaces or routes must leave `mwan_network_config` unchanged. The reordered array must change `canonical_content`, update `pveguest_file`, change `write_id`, and restart `mwan-ifmgr@wan.service`.

Whitespace and object member order changes must leave both `mwan_network_config` and `pveguest_file` unchanged.

### Configured identities and projections

Reuse `interfaceintent.Connection` and `interfaceintent.RouteIntent`. Do not introduce types that mirror their fields.

| Projection | Identity and required values |
| --- | --- |
| `interfaces` | Key by interface name, matching the YANG interface key. |
| `ConfiguredRoutes` | Return configured route-list entries and interface gateway shorthand from every connection and every owner as `map[RouteKey]interfaceintent.RouteIntent`. |
| `RouteKey` | Use interface name, family, and destination prefix. `String()` must return `<interface>|<family>|<destination>` using `netip.Prefix.String()`. |
| `ProviderDefaultKey` | Use connection ID, family, and provider table ID. `String()` must return `<connection-id>|<family>|<table-id>`. |
| `ProviderDefault` | Include interface, family, table ID, configured gateway, DHCP, and optional route metric for each accepted provider connection family. Use a zero gateway address when runtime learns the gateway. |

Families must use `ipv4` and `ipv6`. An existing suitable family type may be reused.

`ConfiguredRoutes` and the provider `routes` map must include interface gateway shorthand as the family default destination, `0.0.0.0/0` or `::/0`. The key must be `<interface>|<family>|<default prefix>`. The gateway must equal the interface gateway. The metric must equal `route-metric` when present and 0 otherwise. The table ID must be 254.

A route-list `/0` entry and interface gateway shorthand share one destination identity. `source` must be `"route"` for a route-list entry and `"gateway"` for interface gateway shorthand.

`provider_defaults` must separately record the default route in the WAN provider table for each accepted provider family.

Exclude DHCP leases and learned gateways, delegated prefixes, NPT external prefixes, router-advertisement defaults, BGP-learned routes, interface indexes, owned addresses, observed routes, assignments, steering state, and BGP peer state.

### Provider objects

The data source `mwan_network` must decode the document at plan time when `content` is known.

| Data source attribute | Type | Requirement |
| --- | --- | --- |
| `content` | String | Required input containing the network document. Null fails validation. |
| `canonical_content` | String | Output from `Canonicalize`. |
| `interfaces` | Map of interface objects | Output keyed by interface name. |
| `routes` | Map of route objects | Output keyed by `RouteKey.String()`. |
| `provider_defaults` | Map of provider default objects | Output keyed by `ProviderDefaultKey.String()`. |

The `mwan_network` interfaces output must apply no `guest-type` rule of its own. `Decode` must return the values accepted by the shared rules. The provider output must reflect the `guest-type` rule once the implementation is on main.

Both provider objects must use the following object types and one shared conversion implementation.

| Object | Attribute types and null values |
| --- | --- |
| Interface | `name`, `type`, `owner`, and `connection_id` are strings. `enabled` is a bool and is null when absent. `roles` is a set containing applicable values from `provider`, `parent`, `internal`, and `management`. `provider_name` is a string and is null for non-provider interfaces. |
| Route | `interface`, `family`, and `destination` are strings. `source` is a string containing `"route"` for a route-list entry or `"gateway"` for interface gateway shorthand. `gateway` is a string and is null for on-link routes. `table_id` and `metric` are numeric configured values after decoding defaults. |
| Provider default | `connection_id`, `interface`, and `family` are strings. `table_id` is numeric. `gateway` is a string and is null when runtime learns the gateway. `dhcp` is a bool. `route_metric` is numeric and is null when absent. |

Task 3 of the plan must add `source` to the shared route object type and conversion for both provider objects. Provider tests must verify both source values and the default route projection from interface gateway shorthand.

The resource `mwan_network_config` must record configured values without a computed copy of those values.

| Resource attribute | Type | Requirement |
| --- | --- | --- |
| `interfaces` | Map of interface objects | Required; not Computed. |
| `routes` | Map of route objects | Required; not Computed. |
| `provider_defaults` | Map of provider default objects | Required; not Computed. |

`ValidateConfig` must verify that each map key equals the identity derived from the object's fields.

`Create` and `Update` must store planned values in state. `Read` must return prior state. `Delete` must remove the resource from state. The resource must have no import operation and no `RequiresReplace` modifier.

Unknown `content` must defer the data source read until apply. Dependent resource attributes must show `(known after apply)`. Configs must supply `content` through `file()` to obtain keyed plan diffs.

Every `Decode` error and every `Config.Rejected` entry must become an error diagnostic. Diagnostics must include the interface and rejection reason when available. Preserve existing semantic error text, including:

```text
interface %s: %s route destination %q must be a canonical same-family network prefix
%s configured routes require table-id 254
%s route destination %s is configured twice
%s route %s has invalid same-family gateway %q
configured route %s on %s conflicts with gateway on %s
resource %s has writers %s and %s
```

### Validation and compatibility

Use one authoritative rule set comprising the existing libyang schema and gateway Go semantic rules. The provider must not implement a second YANG validator.

| Validation layer | Required checks | Failure behavior |
| --- | --- | --- |
| Provider plan | Run `Canonicalize`, then cgo-free `Decode`. Convert load errors and rejected provider entries into diagnostics. | Fail the plan on JSON or Go semantic errors. |
| Guest `check-network` | Run `Load` with cgo and libyang against the staged canonical bytes. Reject any load error or provider rejection. | Fail staged-file validation before rename. |
| Guest `check-firewall` | Load the document, compile the firewall, and apply and inspect it in an isolated network namespace. | Fail staged-file validation before rename. |
| Linux parity test | Label invalid documents by rejecting layer. Assert that `Load` rejects all cases and `Decode` rejects exactly the decode-labeled cases. Compare configurations for valid original and canonical bytes. | Fail verification on any validation or canonicalization mismatch. |

The parity cases must include schema-only errors such as unknown members, invalid enums, invalid ranges, and missing mandatory leaves. Decode cases must include route host bits and invalid special gateways.

Add null-leaf cases to the labeled parity table. Label each case with the rejecting layer observed in the builder container. The test result must determine the label.

The provider and `mwan` binary must use the same build commit. `mwan_release` must reject a release tag with a different commit suffix. `mwan_role` must return the YANG files embedded at that commit. The provider must embed no separate validation schema.

The provider must build with `CGO_ENABLED=0` for `darwin/arm64`, `linux/amd64`, and `linux/arm64`. Provider checks and tests must run on Darwin without libyang. Native guest validation must retain the existing schema checks.

### Plan examples

Task 4 of the plan must assert the following D10 renderings and their before-and-after values.

Adding the route creates a keyed entry.

```text
  # mwan_network_config.gateway will be updated in-place
  ~ resource "mwan_network_config" "gateway" {
      ~ routes            = {
          + "bgp0|ipv4|198.51.100.0/24" = {
              + destination = "198.51.100.0/24"
              + family      = "ipv4"
              + gateway     = "192.0.2.2"
              + interface   = "bgp0"
              + metric      = 0
              + source      = "route"
              + table_id    = 254
            },
            # (1 unchanged element hidden)
        }
        # (2 unchanged attributes hidden)
    }
```

Deleting the route removes that keyed entry.

```text
  ~ resource "mwan_network_config" "gateway" {
      ~ routes            = {
          - "bgp0|ipv4|198.51.100.0/24" = {
              - destination = "198.51.100.0/24" -> null
              - family      = "ipv4" -> null
              - gateway     = "192.0.2.2" -> null
              - interface   = "bgp0" -> null
              - metric      = 0 -> null
              - source      = "route" -> null
              - table_id    = 254 -> null
            },
            # (1 unchanged element hidden)
        }
        # (2 unchanged attributes hidden)
    }
```

Changing the gateway from `192.0.2.2` to `192.0.2.3` and the metric from 0 to 50 updates fields under the existing key.

```text
  ~ resource "mwan_network_config" "gateway" {
      ~ routes            = {
          ~ "bgp0|ipv4|198.51.100.0/24" = {
              ~ gateway     = "192.0.2.2" -> "192.0.2.3"
              ~ metric      = 0 -> 50
                # (5 unchanged attributes hidden)
            },
        }
        # (2 unchanged attributes hidden)
    }
```

Removing the gateway changes the route to on-link.

```text
~ gateway = "192.0.2.2" -> null
```

Changing the configured provider route metric from 10 to 20 updates the provider default. The connection ID `att` and table 101 are example values.

```text
~ provider_defaults = { ~ "att|ipv4|101" = { ~ route_metric = 10 -> 20 } }
```

Equivalent whitespace and object member order produce the unchanged plan for both `mwan_network_config` and `pveguest_file`.

```text
No changes.
```

Uppercase IPv6 destination text must decode to the same lowercase `RouteKey`. The typed resource must show no change; `canonical_content` must differ and cause a file content update.

Adding explicit `table-id` 254 and `metric` 0 must likewise change only canonical content and the file.

### Configs integration and apply

Configs must provide one source document per gateway for `file()` to read at plan time. The Configs wiring lane decides whether to move the source out of the existing template or render a host-specific file.

The network configuration wiring must use the following interface.

```hcl
data "mwan_network" "gateway" {
  content = file("${path.module}/network.json")
}

resource "pveguest_file" "network" {
  path     = "/etc/mwan/network.json"
  content  = data.mwan_network.gateway.canonical_content
  validate = "/usr/local/bin/mwan deploy-gate check-network %s /usr/local/share/wanconfig/yang && /usr/local/bin/mwan deploy-gate check-firewall %s /usr/local/share/wanconfig/yang"
  depends_on = [pveguest_download.mwan, pveguest_file.yang, pveguest_sysrepo_data.role]
}

resource "mwan_network_config" "gateway" {
  interfaces        = data.mwan_network.gateway.interfaces
  routes            = data.mwan_network.gateway.routes
  provider_defaults = data.mwan_network.gateway.provider_defaults
  depends_on        = [pveguest_file.network]
}
```

The snippet specifies network document wiring only. Other pveguest attributes must follow that provider's contract.

Gateway unit startup must depend on the following prerequisite order:

1. `pveguest_host_kernel_modules` installs the 18 LAB-88 modules on PowerEdge.
2. The gateway container exists with `bpfdelegate` and nesting.
3. `pveguest_download` installs `mwan` at `/usr/local/bin/mwan` with mode 0755. The wanconfig-stack debs and `pveguest_deb_packages` are installed.
4. `pveguest_file` installs YANG files under `/usr/local/share/wanconfig/yang`. `pveguest_sysrepo_module` and `pveguest_sysrepo_data` install the role from `mwan_role`.
5. Network and firewall files pass validation using the installed binary and schema.
6. `pveguest_systemd_unit` starts `mwan-ifmgr@wan.service`.

The unit's `restart_on` map must include the binary write ID, `pveguest_file.network.write_id`, firewall file write IDs, and deb write IDs.

The container resource must not reference network document content. Mutable route settings must not force replacement of the guest or `mwan_network_config`.

`pveguest_file` must validate the staged canonical file before rename. Validation failure must delete the staged file and preserve the installed file, `sha256`, and `write_id`.

`mwan_network_config` must depend on the successful file write. A failed validation must preserve its prior state. The next plan must show the same configured changes.

Apply must install the file through `pveguest_file` and restart `mwan-ifmgr@wan.service` through `restart_on`. `mwan_network_config` must record the last successfully written configured values.

### Ownership, drift, and future updates

`mwan_network_config` must own only its OpenTofu state. Its `Read` operation must not inspect the guest file or kernel routes.

`pveguest_file` must own the installed document and detect file drift through `sha256` readback. The daemon must reconcile kernel route drift from the document loaded at startup.

The existing MWAN route reconciler installs main-table routes with protocol 186, journals owned routes, and deletes journaled routes absent from the loaded configuration. Networkd renders networkd-owned routes into `[Route]` sections. BGP-learned routes remain runtime state.

Live reload is deferred. A future live update implementation must be able to identify a changed route by `RouteKey` and read its old configured value from prior resource state without a schema or state migration. The future live path must read `source` to select the route-list entry or interface gateway YANG node for the key.

### Sensitive values

No provider attribute must be marked sensitive. Typed outputs must expose no free-form networkd values.

`canonical_content` must contain the whole document. A secret placed in a free-form networkd entry would appear in the plan for `pveguest_file.content`.

The steering model defines no secret leaf. The current Ansible template renders no secret.

### Dormant LAN

Keep LAN client traffic disabled. The allowed interface set must include every interface with at least one role from `provider`, `parent`, `internal`, and `management`.

The PowerEdge network document contains only interfaces with roles `provider`, `parent`, and `internal`. The internal interface is `mwanbr` on `nic1v0` on the inter-LXC link to the single LAN LXC peer.

The provider must expose interface attributes for the Configs dormant-LAN `check` block. The check must fail on an interface with no allowed role or on a route that references such an interface.

The container check must fail on any gateway interface bound to LAN port `nic0`, `nic3`, or `ens1f1`, or to a bridge containing one of those ports. `proxmox-guest-provider-migration` owns the container check. The check must read interface bindings from the guest container configuration.

The PowerEdge document depends on the pending `guest-type` feature. The feature makes `steering-group/firewall/management-interface` optional for `guest-type` `lxc`. `poweredge-mwan-package-integration` owns the feature. The Configs dormant-LAN check sees no management interface on PowerEdge.

New fixtures and acceptance plans must contain only interfaces with allowed roles. New fixtures, plans, and acceptance steps must configure no LAN client route, LAN forwarding, or LAN-facing DHCP, DNS, or router-advertisement service.

Reuse [network-min.json](../../../gateway/yang/instances/network-min.json) and [network-freeform.json](../../../gateway/yang/instances/network-freeform.json) unchanged for parity tests.

The deliberate `lan.json` rejection case must add `lan0` with no allowed role. The case must expose `lan0` in the plan without applying it.

## Boundaries

### Shared code and native dependencies

Keep the shared implementation in [gateway/internal/networkjson](../../../gateway/internal/networkjson). Create no new module or package.

The provider must import `gateway/internal/networkjson`. Gateway code must never import the provider.

`Decode(data []byte) (*Config, error)` must run the existing unmarshaling and build path without cgo. Identity, projection, and canonicalization helpers must also compile without cgo.

`Load`, `ApplyFrom`, and `ApplyDefault` must remain behind a `cgo` build tag. `Load` must validate the file bytes with libyang and call `Decode` on those same bytes. Preserve existing load behavior, error text, and slog output.

The package documentation must distinguish portable decoding and semantic checks from native schema validation.

The provider objects must make no network call or guest write. OpenTofu host and guest configuration remains the responsibility of the coordinated Configs and pveguest work.

### Peer agreements and file ownership

| Lane | Owns | Agreed interface |
| --- | --- | --- |
| `tofu-wanconfig-mwan-provider` | [decode.go](../../../gateway/internal/networkjson/decode.go), [load.go](../../../gateway/internal/networkjson/load.go), [plan.go](../../../gateway/internal/networkjson/plan.go), [canonical.go](../../../gateway/internal/networkjson/canonical.go), their tests, and moves out of `networkjson.go`; network objects and tests under [provider/internal/provider](../../../provider/internal/provider); network registrations; this specification and its plan | Implement `mwan_network`, `mwan_network_config`, shared identities, and the `Decode`/`Load` split. `poweredge-mwan-package-integration` owns the whole `guest-type` feature. Notify PowerEdge before editing provider registrations beyond `Resources()`, including `DataSources()`, or existing provider tests. Each lane must send a note to the peer lane before pushing a change to shared `networkjson.go`. The second lane to merge must rebase its branch. |
| `mwan-network-cutover` | Watchdog and the cgo-free failover precondition package; no specification or provider edits here | Review schema, identity, ordering, validation, and future update compatibility. Use configured values only and the agreed route and provider default keys. |
| `poweredge-mwan-package-integration` | PR 191 release and role foundation; packaging, BPF, PowerEdge work, PR 194 stack-offline, `npt-pinned-verify`, and `lxc-target-fixes`; the whole `guest-type` feature | Preserve cgo-free provider builds and use a configured-only resource. Coordinate shared provider and CI edits. Measure `check-firewall` in the PowerEdge gateway LXC. Implement the whole `guest-type` feature on one branch from main, including the YANG leaf, the `document` field in `networkjson.go`, firewall validation in [gateway/internal/networkjson/firewall.go](../../../gateway/internal/networkjson/firewall.go), and `firewall.Compile`. `mwan-network-cutover` reviews the feature. Each lane must send a note to the peer lane before pushing a change to shared `networkjson.go`. The second lane to merge must rebase its branch. |
| `proxmox-guest-provider-migration` | pveguest provider, proxmox overlays, Configs guest and host wiring, and the Configs gueststate specification | Install canonical content through validated `pveguest_file`; restart from `write_id`; implement the role-based dormant-LAN check, container binding check, and prerequisite dependencies. |
| `tack-deployment-hardening` | `configsctl`, locks, and the controller | Gate real-host operations through coordinated release and Configs locks. |

This scope includes no apply against PowerEdge or any real host.

Any later plan or apply against the real Proxmox API must run through `./configsctl deploy` or `./configsctl tofu` from a Configs main checkout. `configsctl tofu` locks every `proxmox_servers` host.

Real-host operations must announce the workspace and targets to `tack-deployment-hardening` and `proxmox-guest-provider-migration`, wait for release, and send an end note. All lanes share the local Docker engine.

The [implementation plan](plan.md) must specify task order, file ownership, and verification commands.

## Acceptance criteria

- AC1: Provider builds for `darwin/arm64`, `linux/amd64`, and `linux/arm64` must include `networkjson.Decode` with `CGO_ENABLED=0`.
- AC2: Existing networkjson tests must retain `Load` behavior and error text.
- AC3: The Linux builder parity test must verify rejection layers, equal valid configurations, and equal configurations after canonicalization. Null-leaf cases must use labels determined by the observed rejecting layer.
- AC4: Task 4 must use OpenTofu 1.12.6 to assert keyed route addition, deletion, gateway change, metric change, on-link change, and provider default change. Equivalent whitespace and member order must produce no changes. Invalid semantic input must fail with the `Decode` error text.
- AC5: The plan test must replace no resource. `mwan_network_config` changes must update in place.
- AC6: New fixtures and acceptance plans must contain only interfaces with at least one role from `provider`, `parent`, `internal`, and `management`. New fixtures, plans, and acceptance steps must configure no LAN client route, LAN forwarding, or LAN-facing DHCP, DNS, or router-advertisement service. Existing parity fixtures must remain unchanged. The `lan.json` rejection case must expose `lan0` with no allowed role without applying it. Configs must reject interfaces with no allowed role and routes referencing those interfaces. The container check must reject gateway bindings to `nic0`, `nic3`, `ens1f1`, or bridges containing those ports.
- AC7: Acceptance for this scope must include no apply against a real host.
- AC8: Provider tests must verify required inputs, identity-derived map keys, configured defaults, nullable outputs, both route `source` values, interface gateway shorthand, and state operations through the existing protocol 6 boundary.
- AC9: Canonicalization tests must verify duplicate-member rejection, preserved array order, unchanged string values, and exact number literals.
- AC10: Configs acceptance must establish successful staged `check-network` and `check-firewall` validation before gateway unit startup.

## Failure modes

| Failure | Required outcome and recovery |
| --- | --- |
| JSON syntax error or duplicate member | `mwan_network` must fail the plan. `pveguest_file` must write no guest file. |
| `Decode` error or rejected provider entry | `mwan_network` must fail the plan with the interface and reason when available. `pveguest_file` must write no guest file. |
| Schema-only error | The plan may succeed. Apply must fail at `pveguest_file.validate` with `check-network` output. The staged file must be deleted. The installed document, running service, and prior `mwan_network_config` state must remain unchanged. Recovery requires correcting the Configs document, planning, and applying again. |
| `check-firewall` cannot use `unshare` in the unprivileged LXC | File validation must fail every apply. The Configs acceptance owner must decide recovery after the PowerEdge lane supplies the measurement. |
| Provider and `mwan` release use different commits | `mwan_release` must fail its read during planning. |
| `mwan-ifmgr@wan.service` fails after restart | The unit restart must fail. Recovery must use the existing rollback and watchdog path. The network resource already records the new configured values after the successful file write. |

The `check-firewall` measurement inside the unprivileged PowerEdge gateway LXC and the `guest-type` feature are pending. `poweredge-mwan-package-integration` owns both dependencies. Configs acceptance on PowerEdge under AC10 depends on both.