> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Application files belong to the binary; site files belong to configs

Every file a gateway or router runs has one home, so a deploy's result depends on the pinned release rather than on the checkout that runs the play.

## The rule

Would the file change if the same binary were deployed to a different site, meaning another network with its own ISPs, interface names, addresses, and keys?

- **Never.** The file is static. It lives in the binary's repository, the binary embeds it with `go:embed`, and the install verb writes it onto the host and applies it.
- **Otherwise.** The file is templated. It lives in `configs`, which renders it from inventory. One site value is enough.

The testbed and production are two such sites already. A file identical on both belongs to the program.

There is no third category. A templated file stays in `configs` whether its values come from inventory, `config.toml`, or `network.json`.

One exception: the daemon renders the per-provider networkd units at runtime under `MWAN-491`, because their values change while the daemon runs.

The 802.1X chain and the console getty drop-ins stay in `configs` by ruling, whatever the rule would say.

## The install verb

`mwan install` with no flags prints its help and exits without touching the host. `mwan install --apply` runs on the host as root after the binary is in place. It writes each embedded file to its path only when the content differs, then applies what it wrote through library calls:

- systemd units and drop-ins: `daemon-reload`, then enable, through the systemd D-Bus API.
- The quiet-console sysctl file: each key written through the sysctl runner.
- The schema and the RESTCONF policy: install or update each module into sysrepo and import the policy, through the sysrepo binding.

The verb renders no template and reads no site value. Every file it writes is byte-identical at every site.

The verb prints one line per file that changed and exits non-zero on any failure. Running it twice changes nothing the second time. It never restarts the daemon that runs it; the playbook keeps the restart decision.

`opnsensectl install` has the same contract on the router and on the hypervisor.

The embedded files are files in each repository, embedded with `go:embed`, not Go string literals.

## What the release contains

- `mwan` release: the `mwan` binary archive and the wanconfig stack archive. No schema archive; the schema is inside the binary.
- `opnsensectl` release: the two binary archives.

## What the playbooks do

The playbook installs the binary from the staged release, renders every templated file from inventory, runs `mwan install --apply`, and decides restarts. It copies no static application file from the checkout. The `yanglint` validation on the controller reads the schema the binary carries: `mwan install --print-schema <dir>` writes the embedded modules to a directory on the controller, so the validation and the gateway install read the same bytes.

Release staging lives in the playbooks under `MWAN-490`: each deploy pulls the pinned release with `get_url` against a sha256 pinned in `group_vars`, then `unarchive`, then `gh attestation verify --owner agoodkind` on each archive. `configsctl` keeps only lint, validation, and safe running.
