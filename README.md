# mwan

Multi-WAN gateway daemon. One binary, `mwan`, for linux/amd64 and linux/arm64.
The binary links libyang and libsysrepo statically through cgo and serves its
configuration over the management datastore. The standards a change must meet
are in [AGENTS.md](AGENTS.md).

## Documentation

Read the [gateway architecture](docs/mwan.md) for provider selection,
routing, failover, and planned designs.

The [documentation guide](docs/README.md) includes MWAN specifications,
implementation plans, runbooks, and execution records copied from Configs.
It also explains which commands still require the Configs checkout.

The [router VPP reference](docs/vpp.md) covers optional binary API control,
shared memory counters, and native CPU profiling.

## Layout

| Path | What it holds |
|---|---|
| `gateway/cmd/mwan` | The binary: every subcommand |
| `gateway/internal/installspec` | The role table and the units, drop-ins and policy that `mwan install` writes |
| `gateway/internal` | The daemon: interface management modules, BGP, health, the agent, the wanconfig publisher |
| `gateway/internal/yangpub/schema` | The YANG modules the binary embeds |
| `gateway/proto`, `gateway/gen` | The `mwan.v1` wire contract and its generated code |
| `gateway/yang/instances` | The network documents the instance gate validates |
| `gateway/tools` | The wanconfig stack packaging tool and its builder image |
| `provider` | The `terraform-provider-mwan` OpenTofu provider: data sources for the release archives and the files each role installs |

## Installing what the binary owns

The units, drop-ins, YANG schema, and read-only RESTCONF policy are inside the
binary, so a host runs the files the release it pins was built from rather than
whatever a deploying checkout held. `mwan install` puts them on the host:

```
mwan install                             # Print help
mwan install --role wan --apply          # install everything that role owns
mwan install --print-schema /tmp/schema  # write the YANG modules for validation
```

What a role installs:

- Every role writes its systemd units and drop-ins, then re-enables them, so a
  unit whose `WantedBy` moved loses the symlink under its old target.
- The `wan` role installs or updates the YANG modules in sysrepo and
  imports the read-only NACM policy into each of startup and running that does
  not already hold it.

The verb renders no template and reads no site value, so every file it writes
is byte-identical at every site. `configs` renders the files that need one.

A second `--apply` run reports no change and leaves every timestamp alone.
`--root <dir>` writes under another directory, asks systemd for nothing, and
keeps its own sysrepo repository below the root, which is how to inspect a run
without affecting the machine. The verb never restarts the daemon; that
decision stays with whatever called it.

## Reporting

The reporter uses the existing TOML configuration and WANCONFIG document. Its runtime requirements are:

| Requirement | Purpose |
|---|---|
| Linux | Interface, connection, CPU, and memory counters. |
| RRDtool | Graph history and image rendering. |
| Liberation Mono | Graph text. |
| Email configuration | Recipient and SMTP2GO credential or `msmtprc_path`. |
| `CAP_NET_RAW` | WAN probes. |
| `CAP_NET_ADMIN` | Linux connection accounting. |

MWAN writes report history to `state_directory` and runs the executable at `rrdtool_path`. Both paths must be absolute:

```toml
[report]
state_directory = "/var/lib/mwan/reporting"
rrdtool_path = "/usr/local/bin/rrdtool"
```

Disabled kernel byte accounting makes client attribution incomplete. WAN interface measurements remain available independently of client accounting.

Reporting commands:

| Command | Operation |
|---|---|
| `mwan report watch` | Collect measurements and send scheduled reports and incident emails. |
| `mwan report send` | Send a summary from saved observations. |

The `--network` option selects the WANCONFIG document, and `--schema-dir` selects its YANG validation models. Read [reporting behavior](docs/reporting.md) for measurement meanings and incident handling.

## Building and testing

The build and lint pipeline is [go-makefile](https://github.com/agoodkind/go-makefile),
fetched at parse time. Run `make -C gateway help` for the shared targets.

```
make -C gateway build-check             # vet, lint, govulncheck, and the Go version gate
make -C gateway check-yang              # the YANG model and instance gates
make -C gateway test                    # the suite
make -C gateway build                   # build on a Linux host
make -C gateway docker-make TARGETS=... # any make targets in the builder container
make -C gateway docker-make-amd64 TARGETS=... # the same in the amd64 container, emulated on arm64
make -C gateway build-wanconfig-all     # the linux binary for amd64 and for arm64
make -C gateway test-docker-all         # the suite in the amd64 and the arm64 container
```

Darwin cannot build the sysrepo binding. Run `make -C gateway docker-make` on
macOS to run the quality gate, test suite, and YANG gates in the builder
container. Run `build-docker`, `build-check-docker`, `lint-docker`,
`vet-docker`, or `test-docker` separately with `make -C gateway` to select one
Docker target. `check-yang` also uses the builder container on macOS.
The container is a Debian image with the pinned libyang and sysrepo and `yanglint`
installed, and it builds the cgo dependencies for its own architecture. The
Docker targets need Docker. They use the host architecture unless
`WANCONFIG_DOCKER_ARCH` sets `amd64` or `arm64`, and the other architecture
runs under emulation.

The builder and firewall tests use Docker Compose. Each invocation creates
its own project and temporary cache volumes. The runner removes its test
containers, network, volumes, and temporary directory after success,
failure, or interruption. Host source files and test results remain
available. Docker retains its images and build layers.

The container lints its own architecture, which is linux/arm64 on Apple
Silicon.

CI lints linux/amd64. CI also compiles the binary and the stack packages on a
native arm64 runner and runs the suite there. A change that breaks arm64 fails
those checks.

On a Linux host, the YANG gates need `yanglint` from the `libyang2-tools`
package.

### Build and test the Rust native worker

MWAN keeps WAN policy and connection state in Go and calls the Rust library
through ABI2, its C-compatible native interface. Rust implements DPDK access,
packet parsing, buffer ownership, forwarding, kernel return, and resource
cleanup. It receives and transmits packet batches inside the library. DPDK
and its Broadcom driver remain C dependencies; small C helpers expose DPDK
macros, inline functions, and field access.

Install Rust through rustup, a C compiler, and Clang with libclang. Run these
commands from the repository root. Rustup selects the toolchain in
[rust-toolchain.toml](rust-toolchain.toml).

1. Check Rust formatting, Clippy, parser behavior, and native ABI layouts.

   ```sh
   make -C gateway native-rust-check
   ```

2. Install the pinned Miri and fuzzing tools, then run each check separately.

   ```sh
   make -C gateway native-rust-test-toolchain
   make -C gateway native-rust-miri
   make -C gateway native-rust-fuzz
   ```

The Native Rust workflow runs these checks on Linux amd64 and arm64 without
the DPDK SDK. Miri checks the parser's executed Rust operations for undefined
behavior. Fuzzing uses varied packet inputs and AddressSanitizer to check
decoding, restoration, and forwarding. These checks do not execute the DPDK
interface or instrument the C helpers, DPDK, or Broadcom driver. Go's race
detector does not check Rust or C memory safety.

Run SDK-dependent checks manually on a Linux lab machine for each
architecture. Install Meson, Ninja, and pkg-config, and prepare the SDK using
the [required manual patches](native/patches/README.md). Set
`MWAN_NATIVE_DPDK_PREFIX` to its absolute installation directory before
running this command:

```sh
make -C gateway native-sdk-check MWAN_NATIVE_DPDK_PREFIX="$MWAN_NATIVE_DPDK_PREFIX"
```

This target checks Rust with the DPDK feature, builds the hardware regression
driver, and builds and tests Go with the `nativeoffload` tag. Use
`native-library` with the same SDK setting to build only the library.

To run the SDK checks through Docker Compose, use `docker-make` with the
same host SDK path. The runner mounts the SDK read-only. If the SDK's
pkg-config files specify a different installation path, set
`MWAN_NATIVE_DPDK_CONTAINER_PREFIX` to that path inside the container.

The SDK build does not run hardware traffic tests. Follow the
[malformed-packet procedure](native/tests/malformed/README.md) with external
clients on different physical ports. Compare exact packet bytes and
checksums, kernel return, forwarding after the malformed packet, rule
removal, reopen, and final shutdown. Run one workload at a time. Compare
throughput and CPU against the C worker under matching conditions, with
100% CPU meaning one logical CPU.

Use the [TCP shutdown procedure](native/tests/shutdown.md) to verify one
connection across Disable, Enable, and final close with the Rust fixture.

## Releasing

Pushing to `main` builds, signs, attests, and publishes the release the deploy
installs. Nothing else produces a shipped binary.
