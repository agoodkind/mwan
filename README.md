# mwan

Multi-WAN gateway daemon. One binary, `mwan`, for linux/amd64 and linux/arm64.
The binary links libyang and libsysrepo statically through cgo and serves its
configuration over the management datastore. The standards a change must meet
are in [AGENTS.md](AGENTS.md).

## Documentation

Read the [gateway overview](docs/mwan.md) for provider selection, routing, and
gateway operations.

The [documentation guide](docs/README.md) links the gateway specifications,
implementation plans, and operator guides.

## Command help

Run `mwan` to list commands. Each command provides its arguments, flags,
defaults, and examples through `--help`:

```sh
mwan ifmgr --help
mwan help health
```

Help does not read configuration or start gateway operations. Commands that
use TOML accept `--config`; the first supplied path overrides `MWAN_CONFIG`.

`gateway-status` and the debug inspection and probe views
accept `--output-format text` or `--output-format json`. `gateway-status`
defaults to JSON, and debug views default to text. Finite JSON output contains
one object or array. Use `--verbose` to print diagnostic logs to standard error.

## Check WAN health

Run `mwan health` to probe each enabled WAN with its configured ping targets,
attempt count, HTTP URLs, and timeout from WANCONFIG. Linux requires raw socket
permission for ICMP probes. TOML supplies application settings through the
existing `--config` option. Health probes do not load optional application
observation files or write daemon health state.

```sh
mwan health --config /etc/mwan/config.toml
mwan health --watch --output-format json
```

The command checks every configured target in a complete sweep and sends the
configured number of ping attempts per target. Each address family with ping
targets also receives one HTTP request per configured URL. Empty families and
disabled policies do not trigger probes.

The command defaults to text and exits after a complete sweep. Finite JSON
contains one array of raw probe records. `--watch` starts each WAN independently
and waits for that WAN's configured interval after its previous sweep finishes.
Watched JSON contains one record per line as attempts complete. Interrupting
the command cancels probes and waits for workers to finish.

Records include WAN identity, interface, family, target, attempt, timestamp,
returned measurements, and probe errors. Completed failed probes remain result
data. Argument, configuration, and output errors cause a nonzero exit. Use
`--network` and `--schema-dir` to select the existing WANCONFIG document and
validation models, and `--verbose` for diagnostic logs on standard error.

## Installing what the binary owns

The binary embeds its systemd units, drop-ins, YANG schema, and read-only
RESTCONF policy. `mwan install` writes those files to the host:

```
mwan install                             # Print help
mwan install --role wan --apply          # install everything that role owns
mwan install --print-schema /tmp/schema  # write the YANG modules for validation
```

The installer performs these actions for the `wan` role:

- It writes the systemd units and drop-ins and re-enables each unit to update
  its `WantedBy` links.
- It installs or updates the YANG modules in sysrepo and imports the read-only
  NACM policy into startup and running when those datastores do not store it.

The installer does not render templates or read site values. Every file it
writes has the same bytes at every site. `configs` renders the files that need
site-specific values.

A second `--apply` run reports no change and leaves every timestamp alone.
`--root <dir>` writes under another directory and stores a separate sysrepo
repository below that root. It does not enable the host's systemd units. The
installer does not restart the daemon. The caller controls when to restart it.

## Building and testing

Gateway Make targets run through Docker Compose. Run `make -C gateway help`
for the current target list.

```
make -C gateway build-check
make -C gateway test
make -C gateway build
```
