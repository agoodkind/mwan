# Verify the MWAN command line

Use the [CLI design](../specs/2026-11-01-cli-migration-design.md) to review
gateway command changes.

1. Run the built binary without arguments and with `--help` for commands
   and groups. Check arguments, flags, and examples. Run help as an
   unprivileged user with an unusable configuration path.
2. Exercise invalid arguments and runtime failures separately. Check the exit
   codes, usage text, and diagnostic stream.
3. Compare text and JSON against the same source data. Check identifiers,
   timestamps, statuses, response bodies, and errors. Check that diagnostic
   logs do not enter structured result output.
4. Exercise finite and watched WAN health probes using local servers and
   isolated network namespaces. Check configured targets, per-WAN intervals,
   failed probe records, and cancellation.
5. Run the gateway build and test gates from the
   [repository README](../../../README.md#building-and-testing).
