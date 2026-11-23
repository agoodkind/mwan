# AGENTS

Please read [README.md](README.md) first.

## Gateway controlplane command structure

Each subcommand composes the packages under `internal` rather than
reimplementing them: the configuration loader, the notifier, the logger
factory, the command operations, the BGP speaker, tracing, and WAN state.
The OPNsense tooling, including the daemon inside the OPNsense guest, is the
separate `opnsensectl` binary in
[agoodkind/opnsensectl](https://github.com/agoodkind/opnsensectl).

## Code standards

A subcommand reads its settings through `gateway/internal/config`, which loads
one TOML file and overlays exactly two secrets from the environment. No
subcommand reads its own environment variables for configuration, and a missing
required field fails at load rather than defaulting to a value that is wrong on
every host.

Two callers that need the same data share one type. No bridge or adapter type
mirrors another struct field by field.

Configuration loading, business logic, input and output, and flag parsing live
in separate files. No function parses flags and runs business logic at once.

Addresses, paths, timeouts, mail addresses, and hostnames come from the
configuration.

A comment that restates the code does not belong, and neither does
`// Foo does X` above a function named `Foo`.

## Working rules

Read the source before changing it.

A generic layer stays generic, and provider-specific or platform-specific
behavior sits behind the provider boundary. Preserve exact user-visible values
unless an external boundary requires escaping or translation.

Wire a feature into the runtime path rather than into tests or fallback code,
prefer one source of truth to a compatibility shim, and reconcile related state
as soon as the contract says two values stay in sync.

No baseline edit hides a lint finding, no `//nolint` appears without the
operator's authorization, no synthetic reference or marker-method call
satisfies a reachability tool, no closer is a no-op, and no compile-only or
log-only test counts as behavioral coverage.

A test covers the real contract and the failure mode that motivated the change.
A test that only proves compilation, only reads log output, or asserts
implementation trivia does not.

State what changed, which gates ran, and what residual risk remains. Claim no
file, symbol, commit, or behavior that was not verified, and say why when a
gate could not run.

## Darwin verification

Darwin cannot compile the Linux-only libyang and sysrepo bindings. On Darwin,
run the public Make entry points, which start a Linux builder container:

```bash
make -C gateway build-check
make -C gateway test
```
