# mwan

Multi-WAN gateway daemon. One binary, `mwan`, for linux/amd64, built with cgo
so it links libyang and libsysrepo statically and can serve its configuration
over the management datastore. The standards a change to this code has to meet
are in [AGENTS.md](AGENTS.md).

## Layout

| Path | What it holds |
|---|---|
| `cmd/mwan` | The binary: every subcommand, and the units, drop-ins, policy and templates it embeds |
| `internal` | The daemon: interface management modules, BGP, health, the agent, the wanconfig publisher |
| `internal/yangpub/schema` | The eight YANG modules the binary embeds |
| `pkg/pveapi` | The Proxmox API client |
| `proto`, `gen` | The `mwan.v1` wire contract and its generated code |
| `yang/instances` | The network documents the instance gate validates |
| `tools` | The wanconfig stack packaging tool and its builder image |

## Installing what the binary owns

The units, the YANG schema, the read-only RESTCONF policy, and the templates
for the files that carry a few site values are inside the binary, so a host
runs what the release it pins was built from rather than whatever a deploying
checkout held. `mwan install` puts them on the host:

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
- The `wan` role renders three files that are the program's but carry a few
  site values, from `/etc/mwan/config.toml` and `/etc/mwan/network.json`: the
  kernel tunables at `/etc/sysctl.d/99-mwan.conf`, the routing table names at
  `/etc/iproute2/rt_tables`, and the RESTCONF proxy's configuration at
  `/etc/nghttpx/wanconfig.conf`. It then writes each kernel tunable whose live
  value differs from the file's.

A run writes each file whose content differs, prints one line per file it
changed, and exits non-zero on any failure, so a second run reports no change
and leaves every timestamp alone. A value the rendered configuration does not
carry yet is reported and the file that needs it is left alone, rather than
written emptier than the deploy wrote it.

`--root <dir>` writes under another directory, asks systemd for nothing, keeps
its own sysrepo repository below the root, and applies no kernel tunable,
which is how to inspect a run without affecting the machine. The verb never
restarts the daemon; that decision stays with whatever called it.

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
