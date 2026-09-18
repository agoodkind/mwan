# mwan

Multi-WAN gateway daemon. One binary, `mwan`, for linux/amd64, built with cgo
so it links libyang and libsysrepo statically and can serve its configuration
over the management datastore.

## Layout

| Path | What it holds |
|---|---|
| `cmd/mwan` | The binary: every subcommand, and the four systemd units it embeds |
| `internal` | The daemon: interface management modules, BGP, health, the agent, the wanconfig publisher |
| `internal/yangpub/schema` | The eight YANG modules the binary embeds |
| `pkg/pveapi` | The Proxmox API client |
| `proto`, `gen` | The `mwan.v1` wire contract and its generated code |
| `yang/instances` | The network documents the instance gate validates |
| `tools` | The wanconfig stack packaging tool and its builder image |

## Installing what the binary owns

The units and the schema are inside the binary, so a host runs the files the
release it pins was built from rather than whatever a deploying checkout held.
`mwan install` puts them on the host:

```
mwan install                             # says what it would do, touches nothing
mwan install --role wan --apply          # write that role's units and enable them
mwan install --print-schema /tmp/schema  # write the YANG modules for validation
```

A second `--apply` run reports no change and leaves every timestamp alone.
`--root <dir>` writes under another directory and asks systemd for nothing,
which is how to inspect a run without affecting the machine. The verb never
restarts the daemon; that decision stays with whatever called it.

Installing the schema into sysrepo is still the deploy's job. The binary
carries the modules and writes them out; it does not touch the datastore.

## Building and testing

The build and lint pipeline is [go-makefile](https://github.com/agoodkind/go-makefile),
fetched at parse time by `bootstrap.mk`. Run `make help` for the full target
list.

```
make check   # lint, vet, staticcheck, deadcode, and the YANG model gates
make test    # the suite
```

Both gates want Linux. On macOS `make test` routes the whole suite through a
Debian container carrying the pinned libyang and sysrepo, because darwin
cannot build the sysrepo binding and a host run would compile out every
package that exercises it. `make build-wanconfig` builds the linux binary the
same way. Both need Docker.

The YANG gates need `yanglint`, from `brew install libyang` locally or the
`libyang2-tools` package on Linux.

## Releasing

Pushing to `main` builds, signs, attests, and publishes the release the deploy
installs. Nothing else produces a shipped binary.
