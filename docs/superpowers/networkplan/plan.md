# Verify native network validation and typed rule planning

Use the [network planning contract](spec.md) to verify that the gateway and
OpenTofu provider accept the same network document and that OpenTofu shows
configured changes under stable identities.

## Verify the shared loader

1. Exercise native validation with embedded models and models loaded from a
   directory. Check enabled features, rejection of state data, and diagnostics
   for invalid JSON, invalid YANG data, and invalid network intent.
2. Validate and decode the same input bytes. Compare the decoded results from
   an accepted original document and its canonical form.
3. Exercise failed schema initialization and closed handles. Check that
   gateway diagnostics identify the source path and provider diagnostics
   identify any rejected entries.
4. Run the [gateway build and test gates](../../../README.md#building-and-testing).

## Verify configured policy and firewall projections

1. Compare provider projections and the shared firewall compiler result.
   Check rule identity, order, and configured set membership. Confirm the
   runtime renderer applies the same rules in an isolated network namespace.
2. Compare configured policy rules with the shared gateway derivation. Check
   that runtime selectors remain null when their values depend on leases,
   translation state, or WAN health.
3. Run `make -C provider test-tofu-plan`. Exercise route and rule additions,
   removals, and field changes. Check that mutable fields update existing
   keys, and that reordering rules changes only the chain order when the
   rule identities stay the same.
4. Check typed equality for reordered system lists, explicit defaults, and
   equivalent IPv6 spelling. Check that OpenTofu still displays canonical
   file changes when the source array order or scalar text changes.
5. Exercise provider protocol version 6. Check known invalid input, null
   input, and unknown input that defers evaluation. Verify that upgrading
   version 0 state preserves route keys and initializes rule maps.

## Verify provider packages and installer output

1. Run the provider drift check from
   [CI](../../../.github/workflows/ci.yml). Compare the provider's files with
   the WAN installer output and check that both use the same YANG models.
2. Build provider packages on each native release platform. Inspect the
   library dependencies of each artifact.
3. Test the packages in native runtime environments without a C compiler or
   separately installed libyang and PCRE2. Place a `checksums.txt` file beside
   each package archive. Provide the absolute path of each package in
   `PROVIDER_PACKAGE` and the absolute path of the OpenTofu executable in
   `TOFU`.

   ```sh
   make -C provider test-release-package PROVIDER_PACKAGE="$PROVIDER_PACKAGE" TOFU="$TOFU"
   ```

4. Check that the package works after relocation and that its archive
   metadata matches the artifact under test. Verify `tofu init` and
   `tofu plan` through a filesystem mirror. Require a schema diagnostic
   when the plan uses a document that fails native validation.
