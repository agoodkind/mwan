# mwan

Multi-WAN gateway daemon. One binary, `mwan`, for linux/amd64, built with cgo
so it links libyang and libsysrepo statically and can serve its configuration
over the management datastore.

## Layout

| Path | What it holds |
|---|---|
| `cmd/mwan` | The binary: every subcommand, and the systemd units it installs |
| `internal` | The daemon: interface management modules, BGP, health, the agent, the wanconfig publisher |
| `pkg/pveapi` | The Proxmox API client |
| `proto`, `gen` | The `mwan.v1` wire contract and its generated code |
| `yang` | The gateway's own steering model and the instance documents the gates validate |
| `third_party/yang` | The pinned IETF and IANA modules the steering model imports |
| `tools` | The wanconfig stack packaging tool and its builder image |

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
