# MWAN CLI migration implementation plan

Implement the [approved CLI design](../specs/2026-11-01-cli-migration-design.md) in dependency order. Keep each task in its assigned stack layer and commit coherent changes with behavioral verification.

## Port every command

Use branch `codex/mwan-cli-cobra`, based on `main`.

- [ ] Register every current command and nested action in Cobra, including all debug views, both deployment command trees, watchdog failover, and report watch and send.
- [ ] Preserve names, accepted arguments and flag spellings, defaults, command-specific exit codes, machine output, and actual operational paths.
- [ ] Separate parsing from business logic. Pass concrete options and streams explicitly instead of mutating process arguments or parsing them twice.
- [ ] Add complete descriptions, argument documentation, flags, and examples. Make root help and command help work before configuration loading and operational setup.
- [ ] Preserve daemon startup, signals, readiness, WAN firewall bootstrap ordering, legacy NPT transition conditions, observation JSON contracts, deployment verdict files, and the deploy-gate commit alias.
- [ ] Extend process tests for help, invalid input, and existing machine contracts. Run Linux builder checks and review this layer.

## Format source results

Use branch `codex/mwan-cli-output`, based on `codex/mwan-cli-cobra`.

- [ ] Separate typed collection from text and JSON rendering. Reuse existing result types instead of mirroring them.
- [ ] Format raw source values using tables or indented records. Preserve original statuses, identifiers, booleans, timestamps, response bodies, and error messages.
- [ ] Add --output-format text|json to structured inspection commands and active probe views. Keep existing machine defaults and reject unsupported formats.
- [ ] Separate diagnostics from results. Keep daemon logging behavior and make verbose operator diagnostics explicit.
- [ ] Verify text and JSON preserve identical facts, including unknown states and errors. Run relevant Linux checks and review this layer.

## Run configured health probes

Use branch `codex/mwan-health-once`, based on `codex/mwan-cli-output`.

- [ ] Share pure provider-policy construction with the daemon. Keep optional application observation settings out of the operator WAN probe dependency.
- [ ] Read existing WANCONFIG health policy through the configuration loader. Do not introduce public targets, configuration fallbacks, or another configuration schema.
- [ ] Execute production ping and HTTP probes once by default. Preserve configured attempts, family target lists, disabled policies, timeouts, and URLs.
- [ ] Add --watch with independent per-WAN configured intervals and signal cancellation.
- [ ] Render raw per-probe results in text or JSON. Keep failed probes as data and report setup, configuration, input, and output failures as command errors.
- [ ] Keep operator probes independent of daemon lifecycle, state writes, notifications, and watchdog pushes.
- [ ] Verify through real local servers and network namespaces. Run Linux builder checks and review this layer.

## Verify and publish

- [ ] Run `make -C gateway docker-make TARGETS="build-check test check-yang"` on Darwin.
- [ ] Review the complete stack against the design and preserve unrelated checkout changes.
- [ ] Publish three dependent pull requests ready for review. Verify required Linux CI checks for both architectures.
- [ ] Report pull requests and verified checks separately from deployment. Do not deploy or change production configuration.
