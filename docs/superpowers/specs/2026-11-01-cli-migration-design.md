# MWAN CLI design

MWAN exposes every current command and nested action through one Cobra command tree. Commands provide complete help and examples. Operator output formats source information without adding diagnoses or rewriting reported values.

## Command behavior

Preserve current command names, nesting, accepted arguments and flags, daemon entry points, operational behavior, machine output defaults, and command-specific exit codes. Include every debug view, deployment action, watchdog failover, and reporting action. Preserve the deploy-gate commit alias and deployment verdict files.

Print root help for an invocation without arguments. Support help and --help throughout the tree without loading gateway configuration, requiring root, or starting operations. Show relevant help for invalid input. Print runtime errors without appending usage.

Parse options once into concrete types and pass them to operations explicitly. Load configuration through the existing configuration package. Construct loggers through the existing logger factory. Preserve protective firewall bootstrap ordering during actual WAN interface-manager startup.

## Output behavior

Use aligned tables for repeated records and indented fields for details. Preserve source identifiers, statuses, booleans, timestamps, response bodies, and errors. Keep diagnostic logs separate from result output and require explicit verbose diagnostic output.

Support --output-format text|json on structured inspection commands and active probe views. Preserve existing machine interfaces and their default output. Emit one JSON document for a finite result and newline-delimited records for continuous results. Reject unsupported formats. Keep operational daemon logging behavior.

## Health behavior

Run one configured WAN probe sweep by default. Use --watch for continuous execution at each WAN's configured interval. Obtain provider policy through the existing configuration loader and WANCONFIG decoder. Share the provider-policy construction used by the daemon without requiring optional application observation settings.

Preserve configured targets, disabled policies, empty family target lists, ping counts, URLs, timeouts, and intervals. Run production ping and HTTP probes and report WAN identity, interface, family, target, attempt, returned measurements, response codes, and original errors. Treat completed failing probes as result data. Return command errors for invalid input, invalid configuration, setup failures, and output failures.

Do not start the health daemon, write daemon health state, notify, or push watchdog status from the operator health command.

## Approaches

Adopt ordinary Cobra constructors within MWAN, a shared help template, injected dependencies, and separate result rendering. Clyde already uses these patterns. Its generic operation registry also generates MCP interfaces that this change does not require.

The approved approach migrates the complete existing tree. A help-only change would retain inconsistent parsing. Replacing current commands with new groups would change established interfaces. Extracting Clyde's internal packages would require another library project. Keep MWAN implementation local.

## Acceptance

Exercise complete help and invalid usage through the binary. Preserve machine contracts through existing process and runtime tests. Verify text and JSON against the same source values. Test configured finite and watched health probes with real local servers and network namespaces. Run the repository Linux builder gates and require Linux CI checks for both architectures.

This change adds neither configuration editing nor service controls. It does not change production configuration or deploy the binary.
