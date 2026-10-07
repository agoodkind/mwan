> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Route-aware network planning implementation plan

**Goal:** Make OpenTofu show keyed changes to configured routes from network.json.

**Architecture:** Share portable decoding, configuration projections, and canonicalization in `networkjson`. Use `mwan_network` for plan-time decoding and `mwan_network_config` for configured state. Keep libyang validation in the guest file validation step.

**Tech Stack:** Go, terraform-plugin-framework v1.19.0, protocol 6, OpenTofu 1.12.6, and libyang.

**Spec:** [spec.md](spec.md)

## Global constraints

- Keep network.json as the configuration source. Require plan-time `file()` input for keyed route diffs.
- Keep shared code in `gateway/internal/networkjson`. Do not create another package or module solely for imports.
- Follow [AGENTS.md](../../../AGENTS.md). Use no globals, one type per fact, tight types, and comments that explain non-obvious constraints. Call `slog` before returning wrapped errors.
- Run gateway gates on Darwin through `make -C gateway docker-make TARGETS="check test"`. Run one builder container at a time because worktrees share the lint cache volume.
- Run provider gates with `make -C provider check test`. Preserve `CGO_ENABLED=0` for darwin/arm64, linux/amd64, and linux/arm64.
- Enter tests through public boundaries with real dependencies. Do not use mocks, stubs, spies, or recorded responses.
- Read each file immediately before editing. Preserve changes from other agents.
- Coordinate with `poweredge-mwan-package-integration` before editing `provider.go` outside `Resources()`, including `DataSources()`. Coordinate before editing `provider_test.go` or `.github/workflows/ci.yml`.
- Coordinate Docker use across lanes. All lanes share the local Docker engine.
- Generate all committed prose through the gpt-6.1-sol Codex prose procedure. Include comments, commit messages, and pull request text.
- Create signed commits with `git commit -S`. Fetch before branch comparisons. Verify every branch-local commit before pushing.
- Run no plan or apply against real hosts. Restrict OpenTofu acceptance to temporary directories with local state.
- Keep LAN client traffic dormant in every fixture and acceptance plan. Permit every interface with at least one role from `provider`, `parent`, `internal`, and `management` in accepted configurations. Create new accepted fixtures with only those interfaces and no LAN client route, LAN forwarding, or LAN-facing DHCP, DNS, or router-advertisement service. Reuse existing parity fixtures unchanged. Plan the `lan.json` negative case without applying it.
- Keep provider objects independent of guest writes and kernel state. Defer live reload.
- Keep the provider and mwan binary at one commit. Preserve `mwan_release` commit matching and `mwan_role` embedded schema output.
- Keep Configs wiring with `proxmox-guest-provider-migration`. Do not edit host modules or the pveguest provider in this scope.

## Peer agreements

| Lane | Agreement |
|---|---|
| `poweredge-mwan-package-integration` | The lane owns the whole pending `guest-type` feature on one branch from `main`: the YANG leaf, the `document` field in [networkjson.go](../../../gateway/internal/networkjson/networkjson.go), the validation in [firewall.go](../../../gateway/internal/networkjson/firewall.go), and `firewall.Compile`. For `guest-type lxc`, the feature makes `steering-group/firewall/management-interface` optional. `mwan-network-cutover` reviews the feature. Each lane sends a note before pushing a change to the shared networkjson.go. The second lane to merge rebases its branch. |
| `tofu-wanconfig-mwan-provider` | The lane owns [decode.go](../../../gateway/internal/networkjson/decode.go), [load.go](../../../gateway/internal/networkload/load.go), [plan.go](../../../gateway/internal/networkjson/plan.go), [canonical.go](../../../gateway/internal/networkjson/canonical.go), their tests, and the moves out of [networkjson.go](../../../gateway/internal/networkjson/networkjson.go). `poweredge-mwan-package-integration` owns the pending `guest-type` feature. Each lane sends a note before pushing a change to shared `networkjson.go`. The second lane to merge rebases its branch. |

## 1. Separate portable decoding from native validation

Task 1 is complete in commit `ed1da56`. The gateway gate reported "All blocking checks passed." The networkjson tests reported `ok`.

**Owner:** The `tofu-wanconfig-mwan-provider` lane delegates this task to one implementer.

**Dependencies:** This task depends on the spec commit.

### Files

Create:

- [gateway/internal/networkjson/decode.go](../../../gateway/internal/networkjson/decode.go).
- Task 6 moved the file to [gateway/internal/networkload/load.go](../../../gateway/internal/networkload/load.go).
- [gateway/internal/networkjson/decode_parity_test.go](../../../gateway/internal/networkjson/decode_parity_test.go).

Modify:

- [gateway/internal/networkjson/networkjson.go](../../../gateway/internal/networkjson/networkjson.go).

### Required interfaces

Task 2 requires `Decode(data []byte) (*Config, error)` without cgo. Native callers require unchanged `Load`, `ApplyFrom`, and `ApplyDefault` behavior.

`Load` must validate and decode the same bytes. Portable decoding must retain the existing Go semantic checks.

`Decode` treats explicit JSON null like an absent member. Libyang rejects every tested null leaf at apply time.

### Steps

1. Move `Load`, `ApplyFrom`, and `ApplyDefault` into `load.go` with `//go:build cgo`.
2. Implement `Decode` with `json.Unmarshal` into `document`, followed by `build`.
3. Change `Load` to call `Decode` after libyang validation. Preserve existing path-qualified error text and `slog` lines.
4. Update the package comment to distinguish portable decoding and semantic checks from validation requiring cgo and libyang.
5. Add a cgo parity test through `Load` and `Decode`. Task 1 uses [network-min.json](../../../gateway/yang/instances/network-min.json) and [network-freeform.json](../../../gateway/yang/instances/network-freeform.json) unchanged; Task 2 adds network-routes.json to the parity test.
6. Label invalid documents by rejecting layer. Cover schema-only unknown members, enums, ranges, and mandatory leaves. Cover decode failures for host bits and invalid gateways. Record the observed null-leaf results. Libyang rejects steering weight null, forced-dscp null, and probe-timeout null with "Invalid non-number-encoded uintN value". `Decode` rejects steering weight null and probe-timeout null and accepts forced-dscp null. Both paths reject wan fw-mark 0 because the firewall build check reports "zero or duplicated mark".
7. Assert that `Load` rejects every invalid document. Assert that `Decode` rejects exactly the decode-labeled cases. Compare complete `Config` values for valid documents. Assert equal decoding results for explicit null and absent members.
8. Run the existing networkjson tests to detect changed loader behavior or error text.

### Verification

Run:

```bash
make -C gateway docker-make TARGETS="check test"
```

Expect exit 0. The parity test must establish the layer-specific rejections and equal valid configurations. Null-leaf labels must match observed results. Existing tests must retain their loader results and error text.

Task 3 provides the portable provider build proof.

### Acceptance

AC2 and AC3 pass. `Decode` uses the existing semantic rules and treats explicit null like absence. `Load` retains native schema validation.

## 2. Add configured projections and canonical JSON

Task 2 is complete in commit `e4c476f`. The gateway gate reported "All blocking checks passed." The `goodkind.io/mwan/internal/networkjson` and `goodkind.io/mwan/cmd/mwan` tests reported `ok`.

**Owner:** The `tofu-wanconfig-mwan-provider` lane delegates this task to one implementer.

**Dependencies:** This task depends on Task 1.

### Files

Create:

- [gateway/internal/networkjson/plan.go](../../../gateway/internal/networkjson/plan.go).
- [gateway/internal/networkjson/canonical.go](../../../gateway/internal/networkjson/canonical.go).
- [gateway/internal/networkjson/plan_test.go](../../../gateway/internal/networkjson/plan_test.go).
- [gateway/internal/networkjson/canonical_test.go](../../../gateway/internal/networkjson/canonical_test.go).
- [gateway/internal/networkjson/testdata/plan-base.json](../../../gateway/internal/networkjson/testdata/plan-base.json).
- [gateway/yang/instances/network-routes.json](../../../gateway/yang/instances/network-routes.json).

Modify:

- [gateway/internal/networkjson/decode_parity_test.go](../../../gateway/internal/networkjson/decode_parity_test.go).
- [gateway/internal/networkjson/networkjson_test.go](../../../gateway/internal/networkjson/networkjson_test.go).
- [gateway/cmd/mwan/wanconfig_roundtrip_test.go](../../../gateway/cmd/mwan/wanconfig_roundtrip_test.go).

### Required interfaces

Task 3 requires `Canonicalize(data []byte) ([]byte, error)`, configured routes, interface connections, and provider defaults.

Route keys use `<interface>|<family>|<destination>`. Provider default keys use `<connection-id>|<family>|<table-id>`. The loader derives the connection ID from explicit `connection-id`, then provider name, then interface name.

Reuse `interfaceintent.RouteIntent` and `interfaceintent.Connection`. `ConfiguredRoutes` returns `map[RouteKey]ConfiguredRoute`. `ConfiguredRoute` embeds `interfaceintent.RouteIntent` and adds `Source RouteSource`. `RouteSource` is a string type with constants `RouteSourceRoute = "route"` and `RouteSourceGateway = "gateway"`. Export the projection helpers for future live-update code.

`ProviderDefaults` has the signature `func (c *Config) ProviderDefaults() (map[ProviderDefaultKey]ProviderDefault, error)`. `ProviderDefault` has fields `Interface`, `Family`, `TableID`, `InternalDestination` (`netip.Prefix`), and `InternalInterface`.

### Steps

1. Define `Family` with constants `FamilyIPv4 = "ipv4"` and `FamilyIPv6 = "ipv6"`.
2. Define `RouteKey` with interface name, family, and `netip.Prefix`. Make `String()` use `netip.Prefix.String()`.
3. Implement `Config.ConfiguredRoutes()` across all connections and owners. Include configured route lists with `Source = RouteSourceRoute`. Include each interface gateway shorthand with `Source = RouteSourceGateway`, destination `0.0.0.0/0` or `::/0`, gateway equal to the interface gateway, table 254, and metric equal to `route-metric` when present or 0 otherwise. Use the same destination key for shorthand and a route-list default. Preserve the existing rejection when both configure that destination in one family.
4. Define `ProviderDefaultKey` with `ConnectionID`, `Family`, and `TableID`. Implement `ProviderDefaults` for accepted providers. Include IPv4 only when `TranslationV4` is set and IPv6 only when `TranslationV6` is set. An ietf-ip container or DHCP alone does not add an entry. Use the provider's WAN table ID and derived connection ID.
5. Project the IPv4 internal destination from `steering-group/routes/internal-net-v4`. Project the IPv6 internal destination from `steering-group/translation/opnsense-edge-v6` with prefix length 128. Use `steering-group/routes/internal-iface` for `InternalInterface` in both families. The daemon installs the provider-table default with metric 0 and discovers its gateway from the kernel at runtime. Keep interface gateway shorthand and `route-metric` in the main-table `routes` projection.
6. Reject out-of-range WAN table IDs in `ProviderDefaults` with an error containing the provider and table ID. `Decode` does not enforce the uint32 range; libyang enforces that range during native loading. Return errors for invalid internal destinations.
7. Preserve absent route gateways as zero `netip.Addr` values. Preserve absent shorthand metrics as nil pointers in the shared connection type; project their route metric as 0.
8. Implement `Canonicalize` without schema interpretation. Remove insignificant whitespace and sort object members. Preserve array order, string values, and number literals with `json.Number`.
9. Reject invalid JSON and duplicate decoded member names within an object. Treat escaped and unescaped spellings of the same decoded name as duplicates. Treat case-variant names as distinct names.
10. Copy network-min.json to plan-base.json for the plan test base. Create the route fixture with only interfaces that have at least one allowed role. Include no LAN client route, LAN forwarding, or LAN-facing DHCP, DNS, or router-advertisement service.
11. Test projections through `Decode`. Cover route keys, all owners, default table 254, default metric 0, lowercase IPv6, absent gateways, and provider defaults. Cover both gateway shorthand families, present and absent shorthand metrics, both `Source` values, and rejection of shorthand combined with a route-list default. Cover translation-policy family selection, derived connection IDs, both internal destinations, the internal interface, and out-of-range table ID errors.
12. Test `Canonicalize` through its public function. Cover whitespace, object member order, duplicate decoded member names, case-variant names, array order, string values, and number literals.
13. Extend the parity test with the route fixture, duplicate-member cases, and case-variant cases. Assert that `Load` accepts canonical output and returns the same `Config` as the original valid document. Record that `Decode` accepts duplicate members with the last value winning and matches member names case-insensitively. `Canonicalize` and libyang reject duplicate members. Label a case-variant member such as `Destination` as a schema-layer rejection because libyang rejects it as an unknown node.
14. Add `//go:build cgo` to networkjson_test.go because the file calls `Load` and `yangpub.WriteSchema`.
15. Add one `networkListKeys` entry for the route list key in wanconfig_roundtrip_test.go.

### Verification

Run:

```bash
make -C gateway docker-make TARGETS="check test"
```

Expect exit 0. Projection tests must establish stable keyed values for route-list entries, gateway shorthand, and provider-table internal routes. Canonicalization tests must establish exact preservation of meaningful array order and scalar values. Native parity tests must accept the canonical valid documents and establish duplicate-member and case-variant rejection layers.

### Acceptance

AC3 passes for the route fixture and canonical output after the gateway gate succeeds. Configured routes include gateway shorthand in table 254 with the required `source`. Provider defaults use the provider table and translation-policy family set. Provider defaults include the configured internal destination and interface. The projection excludes observed, learned, leased, delegated, router-advertisement, and BGP routes.

## 3. Implement the provider objects

Task 3 is complete in commit `3a2198e`. The provider gate reported "All checks passed." The `goodkind.io/mwan/provider/internal/provider` tests reported `ok`.

**Owner:** The `tofu-wanconfig-mwan-provider` lane delegates this task to one implementer.

**Dependencies:** This task depends on Task 2.

### Files

Create:

- [provider/internal/provider/network_model.go](../../../provider/internal/provider/network_model.go).
- [provider/internal/provider/network_data_source.go](../../../provider/internal/provider/network_data_source.go).
- [provider/internal/provider/network_config_resource.go](../../../provider/internal/provider/network_config_resource.go).
- [provider/internal/provider/network_test.go](../../../provider/internal/provider/network_test.go).

Modify:

- [provider/internal/provider/provider.go](../../../provider/internal/provider/provider.go).

### Required interfaces

Task 4 requires the registered `mwan_network` data source and `mwan_network_config` resource over protocol 6.

Configs requires `canonical_content`, `interfaces`, `routes`, and `provider_defaults`. The interface output must expose the fields needed for the dormant-LAN check.

The `mwan_network` interfaces output applies no `guest-type` rule of its own. `Decode` returns the values that the shared rules accept. The provider output reflects the `guest-type` rule once the feature code is on `main`.

The pending `guest-type` branch adds the top-level network.json leaf with a `qemu` default and an `lxc` value. `networkjson.Config.GuestType` uses `config.GuestType` with `config.GuestTypeQEMU`, `config.GuestTypeLXC`, `config.GuestTypes()`, and `config.ParseGuestType(name)`. The `lxc-target-fixes` branch adds optional `mwan_role.guest_type` with the same values and default.

Task 3 adds the string output `mwan_network.guest_type` only when the guest-type code is on `main`. Otherwise, a follow-up task adds the output after that merge. The output uses the decoded leaf value with the `qemu` default applied by `Decode`. The Configs guest module passes `data.mwan_network.gateway.guest_type` to `mwan_role.guest_type`.

A future live path reads route `source` to select the YANG node for a key.

### Steps

1. Define shared object types and conversion in `network_model.go`. Use identical object types for both provider objects.
2. Implement required string input `content` for `mwan_network`. Call `Canonicalize`, followed by `Decode`.
3. Convert canonicalization errors, decode errors, projection errors, and each `Config.Rejected` entry into error diagnostics. Include the interface and reason for rejected entries. Include the provider and out-of-range table ID for `ProviderDefaults` range errors.
4. Return `canonical_content` and the configured maps.

   | Output | Key | Object attributes |
   |---|---|---|
   | `interfaces` | Interface name | `name`, `type`, `enabled`, `owner`, `connection_id`, `roles`, `provider_name` |
   | `routes` | Route key string | `interface`, `family`, `destination`, `gateway`, `table_id`, `metric`, `source` |
   | `provider_defaults` | Provider default key string | `connection_id`, `interface`, `family`, `table_id`, `internal_destination`, `internal_interface` |

5. Encode absent `enabled`, non-provider `provider_name`, and absent route gateways as null. Encode roles as a set of `provider`, `parent`, `internal`, and `management`. Encode route `source` as `"route"` or `"gateway"` from the shared projection. Encode `internal_destination` with `netip.Prefix.String()` and `internal_interface` from the shared projection.
6. Add the `guest_type` string output when the guest-type code is on `main`. Otherwise, record the follow-up task after that merge.
7. Implement the resource maps as Required attributes with no Computed attributes. Validate every map key against the object's identity fields. Validate `source` against its allowed values.
8. Make Create and Update store planned values. Make Read return prior state. Make Delete remove state.
9. Add no import support or `RequiresReplace` modifier. Make no network call or guest write.
10. Register the resource in `Resources()`. Coordinate with `poweredge-mwan-package-integration` before registering the data source in `DataSources()`.
11. Test both objects through the protocol 6 public boundary using the existing harness style. Exercise diagnostics, key validation, null values, defaults, both route sources, gateway shorthand, provider internal destinations and interfaces, projection errors, and resource state operations. Test decoded `qemu` and `lxc` output values when `guest_type` is added.
12. Test null content rejection and unknown content deferral. Keep every attribute non-sensitive.

### Verification

Run on Darwin:

```bash
make -C provider check test
```

Expect exit 0 with `CGO_ENABLED=0`. The provider must compile with `networkjson.Decode` without libyang.

Run the Linux gateway gates separately:

```bash
make -C gateway docker-make TARGETS="check test"
```

Expect exit 0. Protocol tests must return the specified maps and diagnostics.

Require provider build results for darwin/arm64, linux/amd64, and linux/arm64 before accepting AC1.

### Acceptance

AC1 passes after all platform builds succeed. Both objects expose configured values through the shared model. Resource updates require no replacement. `mwan_network` rejects duplicate members at plan time because it calls `Canonicalize` before `Decode`.

## 4. Test OpenTofu plans through the provider binary

Task 4 is complete in commit `3910073`. The provider gate reported "All checks passed." The OpenTofu 1.12.6 plan test reported `ok goodkind.io/mwan/provider/internal/provider` (9.539s). The `tofu-plan` CI job has not run on GitHub yet.

**Owner:** The `tofu-wanconfig-mwan-provider` lane delegates this task to one implementer.

**Dependencies:** This task depends on Task 3.

### Files

Create:

- [provider/internal/provider/tofu_plan_test.go](../../../provider/internal/provider/tofu_plan_test.go).
- [provider/internal/provider/testdata/networkplan/main.tf](../../../provider/internal/provider/testdata/networkplan/main.tf).
- [provider/internal/provider/testdata/networkplan/base.json](../../../provider/internal/provider/testdata/networkplan/base.json).
- [provider/internal/provider/testdata/networkplan/added.json](../../../provider/internal/provider/testdata/networkplan/added.json).
- [provider/internal/provider/testdata/networkplan/removed.json](../../../provider/internal/provider/testdata/networkplan/removed.json).
- [provider/internal/provider/testdata/networkplan/changed.json](../../../provider/internal/provider/testdata/networkplan/changed.json).
- [provider/internal/provider/testdata/networkplan/reformatted.json](../../../provider/internal/provider/testdata/networkplan/reformatted.json).
- [provider/internal/provider/testdata/networkplan/invalid.json](../../../provider/internal/provider/testdata/networkplan/invalid.json).
- [provider/internal/provider/testdata/networkplan/lan.json](../../../provider/internal/provider/testdata/networkplan/lan.json).

Modify:

- [provider/Makefile](../../../provider/Makefile).
- [.github/workflows/ci.yml](../../../.github/workflows/ci.yml).

### Required interfaces

The test requires the provider binary, OpenTofu 1.12.6, and local state. It must exercise `mwan_network` and `mwan_network_config` through OpenTofu.

Pass `canonical_content` to `terraform_data` to observe document changes without pveguest. The test cannot prove pveguest validation, write IDs, or service restarts.

### Steps

1. Add `//go:build tofu` to the test.
2. Add `test-tofu-plan`. Build the provider and create a temporary CLI configuration with `dev_overrides`. Run `go test -tags tofu -run TestTofuPlan ./internal/provider` with `TOFU` set to the OpenTofu binary path.
3. Make `main.tf` read a local JSON file through `mwan_network`. Assign the three configured maps to `mwan_network_config`. Give `mwan_network_config` a `depends_on` entry for the `terraform_data` resource that receives `canonical_content`.
4. Use temporary directories and local state. Run `tofu init` and `tofu apply -auto-approve` for the base document. Use only interfaces with allowed roles in accepted fixtures.
5. Run `tofu plan -out` and `tofu show -json` for each variant. Read the rendered plan text. Include a plan with a pending change to the `terraform_data` resource and assert that the keyed route diff for `mwan_network_config` renders in that plan.
6. Assert actions and before and after values per route key. Assert the expected text for additions, deletions, gateway changes, metric changes, and on-link changes. Include `source = "route"` in route-list addition and deletion examples. Assert five unchanged attributes in the combined gateway and metric change example. Cover gateway shorthand with `source = "gateway"` under the family default key.
7. Assert `~ gateway = "192.0.2.2" -> null` for gateway removal from a route-list entry. Change `steering-group/routes/internal-net-v4` from `192.168.10.0/24` to `192.168.20.0/24` and assert `~ internal_destination = "192.168.10.0/24" -> "192.168.20.0/24"` under each provider's IPv4 key, including `att|ipv4|101`. Assert that shorthand gateway and `route-metric` changes update only the corresponding main-table default route in `routes` with `source = "gateway"`.
8. Assert no changes for whitespace and object member reformatting.
9. Derive temporary variants for route and interface list reordering, uppercase IPv6, and explicit defaults. Assert unchanged configured state. Assert changed canonical content where the document's arrays or scalar representations differ.
10. Assert an error containing the rejecting helper's message for invalid input. Cover JSON syntax errors, duplicate decoded member names, rejected provider entries, and out-of-range provider table IDs through the data source boundary.
11. Assert that route changes update `mwan_network_config` in place. Assert that no variant replaces a resource.
12. Plan the `lan.json` negative case without applying it. Add `lan0` with no allowed role and no LAN client route, LAN forwarding, or LAN-facing DHCP, DNS, or router-advertisement service. Assert that the interface map exposes `lan0` with no allowed role. Keep the Configs dormant-LAN preconditions with their owning lane.
13. Coordinate the workflow edit with `poweredge-mwan-package-integration`. Install OpenTofu 1.12.6 in CI and run the new target.

### Verification

Run on Darwin with OpenTofu 1.12.6:

```bash
make -C provider test-tofu-plan
```

Expect exit 0. OpenTofu must produce keyed route changes, both route sources, provider internal destination changes, no formatting-only changes, diagnostics, and no replacement actions. The keyed route diff must render when the `terraform_data` dependency has a pending change in the same plan.

### Acceptance

AC4 and AC5 pass. AC7 passes because every apply uses temporary local state.

AC6 requires every new accepted fixture to contain only interfaces with at least one allowed role and no LAN client route, LAN forwarding, or LAN-facing DHCP, DNS, or router-advertisement service. Existing parity fixtures remain unchanged. The `lan.json` negative case exposes `lan0` without an allowed role and is planned without an apply.

A `lifecycle { precondition }` block on `pveguest_file.network` must fail on an interface with no allowed role or a route that references such an interface. A separate `lifecycle { precondition }` block on the gateway container resource must fail on a gateway interface bound to `nic0`, `nic3`, `ens1f1`, or a bridge containing one of those LAN ports. `proxmox-guest-provider-migration` owns both preconditions. The binding precondition reads container interface bindings and host bridge member ports from the host node network configuration. The local negative plan does not prove those preconditions.

## 5. Review prose, signed commits, and CI

**Owner:** The `tofu-wanconfig-mwan-provider` lane delegates this task to one implementer.

**Dependencies:** This task depends on Task 4.

### Files

Create no additional files.

Modify no additional files. Review prose in the files assigned by Tasks 1 through 4.

### Required interfaces

The implementation review requires Task 4's plan evidence and the gateway and provider gate results.

`mwan-network-cutover` reviews route identity, ordering, validation, and future update compatibility. `proxmox-guest-provider-migration` consumes the provider outputs for Configs wiring.

### Steps

1. Generate committed comments, commit messages, and pull request text through the gpt-6.1-sol Codex prose procedure.

   Run each prose invocation with:

   ```bash
   gtimeout --signal=TERM --kill-after=10s 300s codex exec --ephemeral --model gpt-6.1-sol --sandbox read-only
   ```

   Copy both the responses and writing rule blocks verbatim into every input. Accept a result only when the command exits 0 and the result is nonempty. Apply accepted prose verbatim. Request corrections only through another bounded invocation of the same model.

2. Create signed commits in logical groups.
3. Fetch before inspecting `origin/main..HEAD`. Verify each commit with `git verify-commit <sha>` and inspect the raw `gpgsig` header with `git cat-file commit <sha>`.
4. Request review of the shared runtime contract from `mwan-network-cutover`. Confirm the provider output contract with the Configs integration lane.
5. Open a pull request to `main` with verified gate results and remaining acceptance dependencies.
6. Read the active GitHub ruleset. The supplied 2026-10-06 ruleset requires a pull request, signed commits, resolved review threads, and zero required approvals. It permits merge and squash. Merge only after every required check passes and every review thread is resolved.

   Require these status checks to pass:

   - `GitGuardian Security Checks`
   - `go / Build`
   - `go / Prepare`
   - `go / Quality / Format`
   - `go / Quality / Go Version`
   - `go / Quality / Gocyclo`
   - `go / Quality / Golangci Lint`
   - `go / Quality / Staticcheck Extra`
   - `go / Quality / Test`
   - `go / Quality / Vet`

### Verification

Run the gates separately:

```bash
make -C provider check test
make -C provider test-tofu-plan
make -C gateway docker-make TARGETS="check test"
make -C gateway test-provider-drift
```

Expect exit 0 from each command. The drift gate must retain equality between installed role data and provider role data.

For each branch-local commit, run:

```bash
git verify-commit <sha>
git cat-file commit <sha>
```

Expect successful signature verification and a raw `gpgsig` header.

Verify that every active required status check passes and every review thread is resolved before merging.

### Acceptance

The pull request contains reviewed prose, signed commits, passing local gates, passing required CI checks, and resolved review threads. Record any unresolved acceptance evidence before merge.

## 6. Move native loading into networkload and apply review fixes

The working tree contains the implementation. The commit and the CI run that compiles the cgo gateway packages remain pending.

### Files

- Create [gateway/internal/networkload/load.go](../../../gateway/internal/networkload/load.go).
- Update decoding, canonicalization, and tests under [gateway/internal/networkjson](../../../gateway/internal/networkjson).
- Update native callers and tests under [gateway/cmd/mwan](../../../gateway/cmd/mwan).
- Update [gateway/internal/agent/bgp_fib.go](../../../gateway/internal/agent/bgp_fib.go) and [gateway/internal/ifmgr/modules/health/health_test.go](../../../gateway/internal/ifmgr/modules/health/health_test.go).
- Update [provider/internal/provider/network_test.go](../../../provider/internal/provider/network_test.go), this specification, and this plan.

### Steps

1. Move `Load`, `ApplyFrom`, and `ApplyDefault` into `networkload` without a build tag. Delete the former loading file from `networkjson`. Preserve behavior, error texts, and slog output.
2. Export `SyntaxError` with an unexported field and `Error` and `Unwrap` methods. Match `SyntaxError` with `errors.As` in `networkload.Load`. Return `decode <path>: <err>` and log once with the path. Remove logging from `Decode`.
3. Update native callers and tests to use `networkload`. Remove `yangpub` imports from non-test `networkjson` files while retaining `yangpub/schema` imports.
4. Make `Canonicalize` reject lone UTF-16 surrogate escapes and member names equal under `strings.EqualFold`. Add regression cases for both conditions.
5. Add duplicate-member and case-variant schema-layer parity cases. Add `TestDecodeKeepsTheLastDuplicateMember`.
6. Add `//go:build cgo` to the DHCPv6 tests. Add a `ProviderDefaults` table-id diagnostic case.
7. Update the specification and plan for the package move and canonicalization rules.

### Verification

`make -C provider check test` and `make -C provider build` pass locally, including the go-mk cgo stub check.

CI run `37551702997` on PR #202 failed `provider / Compile` on darwin/arm64, linux/amd64, and linux/arm64 because go-mk included the cgo-tagged loading file's `yangpub` dependency in the provider build graph.

The next CI run must compile the cgo gateway packages before this section is complete.

## Self-review

The implementation order is Task 1, Task 2, Task 3, Task 4, and Task 5. Each task requires the preceding task's public interface.

Portable decoding preserves Go semantic validation and treats explicit null like absence. Native validation remains necessary for schema-only failures. The Task 1 parity results establish schema-layer rejection for every tested null leaf.

`Decode` accepts duplicate object members with the last value winning and matches member names case-insensitively. `Canonicalize` rejects duplicate decoded member names before provider decoding. Libyang rejects case-variant members as unknown nodes.

Gateway shorthand and route-list defaults share destination identity. The `source` attribute selects the YANG node for a future live update. Provider defaults project provider-table default and internal route configuration for families with translation policies. The internal destination and interface are configured values. The daemon discovers the provider-table default gateway from the kernel at runtime.

The allowed roles include management, so `enmgmt0` does not conflict with the fixture requirements. Existing parity fixtures remain unchanged. The `lan.json` negative case is planned without an apply.

On PowerEdge, `mwanbr` uses gateway-side VF `nic1v0` on the inter-LXC link to the single LAN LXC peer. The PowerEdge network document contains only interfaces with roles `provider`, `parent`, and `internal`. The Configs dormant-LAN precondition sees no management interface on PowerEdge.

The PowerEdge document depends on the pending `guest-type` feature, which makes `steering-group/firewall/management-interface` optional for `guest-type lxc`. `poweredge-mwan-package-integration` owns the feature. PowerEdge acceptance requires decision D2's guest-type feature in the mwan release that `pveguest_download` installs.

Configs acceptance depends on the pending `check-firewall` measurement inside the unprivileged PowerEdge gateway LXC. `poweredge-mwan-package-integration` owns the measurement. `TestCheckFirewallIsolatedKernelLXC` passed in the builder container. The container test does not establish the live LXC measurement.

The local OpenTofu test proves configured state and canonical document differences. Guest validation, failed-write recovery, write IDs, service restarts, and Configs dormant-LAN preconditions require separate Configs acceptance.

Configs acceptance requires a network.json content write that passes both `check-network` and `check-firewall`. A validate-only change does not write or revalidate the installed file. The `restart_on` values for both `mwan-ifmgr@wan.service` and `mwan-agent.service` must include `pveguest_file.network.write_id` because the agent reads network.json at startup when `[bgp] use_wanconfig = true`.

Live reload and real-host operations remain outside this implementation plan.

