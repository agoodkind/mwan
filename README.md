# mwan

Multi-WAN gateway daemon. One binary, `mwan`, for linux/amd64 and linux/arm64.
The binary links libyang and libsysrepo statically through cgo and serves its
configuration over the management datastore. The standards a change to this code has to meet
are in [AGENTS.md](AGENTS.md).

## Layout

| Path | What it holds |
|---|---|
| `cmd/mwan` | The binary: every subcommand, and the units, drop-ins and policy it embeds |
| `internal` | The daemon: interface management modules, BGP, health, the agent, the wanconfig publisher |
| `internal/yangpub/schema` | The eight YANG modules the binary embeds |
| `pkg/pveapi` | The Proxmox API client |
| `proto`, `gen` | The `mwan.v1` wire contract and its generated code |
| `yang/instances` | The network documents the instance gate validates |
| `tools` | The wanconfig stack packaging tool and its builder image |

## Installing what the binary owns

The units, drop-ins, YANG schema, and read-only RESTCONF policy are inside the
binary, so a host runs the files the release it pins was built from rather than
whatever a deploying checkout held. `mwan install` puts them on the host:

```
mwan install                             # says what it would do, touches nothing
mwan install --role wan --apply          # install everything that role owns
mwan install --print-schema /tmp/schema  # write the YANG modules for validation
```

What a role installs:

- Every role writes its systemd units and drop-ins, then re-enables them, so a
  unit whose `WantedBy` moved loses the symlink under its old target.
- The `wan` role installs or updates the eight YANG modules in sysrepo and
  imports the read-only NACM policy into each of startup and running that does
  not already hold it.

The verb renders no template and reads no site value, so every file it writes
is byte-identical at every site. `configs` renders the files that need one.

A second `--apply` run reports no change and leaves every timestamp alone.
`--root <dir>` writes under another directory, asks systemd for nothing, and
keeps its own sysrepo repository below the root, which is how to inspect a run
without affecting the machine. The verb never restarts the daemon; that
decision stays with whatever called it.

## Building and testing

The build and lint pipeline is [go-makefile](https://github.com/agoodkind/go-makefile),
fetched at parse time by `bootstrap.mk`. Run `make help` for the full target
list.

```
make check               # lint, vet, staticcheck, deadcode, and the YANG model gates
make test                # the suite
make test-docker         # the suite in the builder container, on any host
make build-wanconfig     # the linux binary, built in the builder container
make test-docker-all     # the suite in the amd64 and the arm64 container
make build-wanconfig-all # the linux binary for amd64 and for arm64
```

Both gates need Linux. Darwin cannot build the sysrepo binding, and a host run
would compile out every package that uses it. On macOS, `make test` runs the
suite in the builder container, a Debian image with the pinned libyang and
sysrepo installed. The Docker targets need Docker. They use the host
architecture unless `WANCONFIG_DOCKER_ARCH` sets `amd64` or `arm64`, and the
other architecture runs under emulation. `make build-wanconfig` writes
`bin/mwan-wanconfig-linux-<arch>`.

The YANG gates need `yanglint`, from `brew install libyang` locally or the
`libyang2-tools` package on Linux.

## Releasing

Pushing to `main` builds, signs, attests, and publishes the release the deploy
installs. Nothing else produces a shipped binary.
