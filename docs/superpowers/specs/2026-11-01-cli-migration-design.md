# MWAN CLI design

The MWAN binary passes process arguments and streams to one internal entry
point. The root package assembles the Cobra command tree. Each command package
provides a constructor that returns a command with its arguments, flags, help,
and examples.

## Command behavior

The root and command groups display help when a caller omits a command.
`help` and `--help` do not load gateway configuration, require root, or start
network operations. Invalid input prints relevant usage. Runtime errors print
diagnostics without appending usage.

Commands pass concrete options and streams to operations that can run without
Cobra. They use the shared configuration loader and logger factory. The WAN
interface manager installs its protective firewall during startup before it
applies network configuration.

## Output behavior

Structured inspection and probe views accept `--output-format text` and
`--output-format json`. Text uses tables or indented fields. Both formats
preserve source identifiers, statuses, timestamps, response bodies, and
errors. Finite results use one JSON document. Continuous JSON results use one
record per line. Operator commands put diagnostic logs on standard error when
the caller enables verbose logging.

## Health behavior

`mwan health` runs one sweep using the configured WAN policy and the same
production probe implementation as the daemon. Each sweep uses configured ping
targets and attempt counts, HTTP URLs, and timeouts. Disabled policies and
families without ping targets do not trigger probes.

`--watch` repeats probes at each WAN's configured interval. Interrupting the
command cancels the workers. Completed probe failures remain result data.
Invalid input, configuration, setup, and output errors return a nonzero exit.
The operator command does not load optional application observation settings
or write daemon health state.
