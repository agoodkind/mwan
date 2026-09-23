# AGENTS

Standards for changing the Go code here. A violation blocks merge. What the
binary installs, how to build it, and which gates to run are in
[README.md](README.md); run those gates before reporting a change as done.

## One binary

A new gateway tool becomes a subcommand of `mwan`, never a second binary. Run
`mwan` with no arguments for the current set. A subcommand is either a
long-running daemon, as the agent, the watchdog, and the interface manager are,
or a one-shot operator tool, as the health probes, the delegated-prefix and
firewall-state inspection, and the alert self-test are. The interface manager's
behavior comes from the role it starts in.

Each subcommand composes the packages under `internal` rather than
reimplementing them: the configuration loader, the notifier, the logger
factory, the operations layer, the BGP speaker, tracing, and rollback state.
The OPNsense tooling, including the daemon inside the OPNsense guest, is the
separate `opnsensectl` binary in
[agoodkind/opnsensectl](https://github.com/agoodkind/opnsensectl).

## Code standards

- **One configuration path.** A subcommand reads its settings through
  `internal/config`, which loads one TOML file and overlays exactly two secrets
  from the environment. No subcommand reads its own environment variables for
  configuration, and a missing required field fails at load rather than
  defaulting to a value that is wrong on every host.
- **One notifier and one logger factory.** Alert mail goes through
  `internal/notify`, and every logger comes from `logging.New`. No subcommand
  builds a second mail path or a second handler stack.
- **No globals.** Configuration and state pass through function arguments. No
  package-level `var` holds configuration, shared state, or a singleton.
- **One type per fact.** Two callers that need the same data share one type. No
  bridge or adapter type mirrors another struct field by field.
- **Separated concerns.** Configuration loading, business logic, input and
  output, and flag parsing live in separate files. No function parses flags and
  runs business logic at once.
- **No hardcoded values.** Addresses, paths, timeouts, mail addresses, and
  hostnames come from the configuration.
- **Tight types.** `any`, `interface{}`, and loose maps appear only at a real
  external boundary. Convert untyped input to a concrete type as early as it
  can be converted.
- **Comments say why.** A comment that restates the code does not belong, and
  neither does `// Foo does X` above a function named `Foo`.

## Working rules

- **Start from evidence.** Read the source before changing it.
- **Respect the boundary.** A generic layer stays generic, and
  provider-specific or platform-specific behavior sits behind the provider
  boundary. Preserve exact user-visible values unless an external boundary
  requires escaping or translation.
- **Implement the real path.** Wire a feature into the runtime path rather than
  into tests or fallback code, prefer one source of truth to a compatibility
  shim, and reconcile related state as soon as the contract says two values
  stay in sync.
- **No shortcuts.** No baseline edit hides a lint finding, no `//nolint` lands
  without the operator's authorization, no synthetic reference or marker-method
  call satisfies a reachability tool, no closer is a no-op, and no compile-only
  or log-only test counts as behavioral coverage.
- **Useful tests.** A test covers the real contract and the failure mode that
  motivated the change. A test that only proves compilation, only reads log
  output, or asserts implementation trivia does not.
- **Report honestly.** State what changed, which gates ran, and what residual
  risk remains. Claim no file, symbol, commit, or behavior that was not
  verified, and say why when a gate could not run.

## Darwin verification

Darwin cannot compile the Linux-only libyang and sysrepo bindings. On Darwin,
run every gate in the repository's Linux builder container, which is native
arm64 on Apple Silicon. The container builds the cgo dependencies for
linux/arm64 and runs the named make targets:

```bash
make docker-make TARGETS="check test"
```

`TARGETS` accepts any make target. The first run builds the image and the cgo
dependencies, and later runs reuse both.

The container lints linux/arm64. The required Linux GitHub CI checks lint
linux/amd64 and build and test both architectures. A change is done only when
those checks pass.
