> Read the [repository context](../README.md) before using copied commands or historical plans.

# Operations

These pages are for running and recovering the homelab.

Production hosts serve live traffic for people who cannot recover from a bad
change on their own. Read the live host before you trust repo templates. State a
hypothesis, test the smallest reversible change, verify no regression, and only
then codify the change in git. Do not bulk-change MWAN, OPNsense, or the vault
hypervisor, and do not restart networking services without a rollback path.

Run OpenTofu and Ansible through configsctl from the repository root. The
[configsctl](https://github.com/agoodkind/configs/blob/main/configsctl) script there runs the configsctl release this
checkout pins, so continuous integration, the pre-commit hook, and every operator
run the same version. To move to a newer release, change the version in that
script.

```bash
./configsctl tofu apply
./configsctl deploy <name> [--limit <host>] [--check] [--diff]
```

A play that installs the MWAN binary or opnsensectl pulls the release its
environment pins onto the controller first. It downloads each archive against
the pinned checksum, verifies the GitHub attestation, and unpacks it under the
ignored `.make/releases` directory. No release is named on the command line,
and a check run stages the release without touching a host. After it copies
the binary onto a Linux host, the play runs `mwan version` on that copy and
fails unless the reported commit is a prefix of the release's commit.
`deploy-proxmox` and `deploy-opnsense` check opnsensectl the same way. To move
an environment to a newer release, set its tag, the archive checksums from
that release's `checksums.txt`, and for MWAN the repository that publishes the
release in
[mwan_prod_all.yml](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_prod_all.yml) or
[mwan_testbed_all.yml](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/mwan_testbed_all.yml).
Use `--limit` on production so one command does not touch both hypervisors.
Dry-run with `--check --diff` before a mutating run.

## Run logs

Both commands write their output to a file in the host temp directory, under a
`configs-runs` directory, and print that path before the run starts. The output
arrives over minutes and is the record you need when a run goes wrong, so it
goes somewhere nothing can truncate, buffer, or pipe it away. Follow a run with
`tail -f <path>`. Secret values are redacted in the file exactly as they are on
the terminal, and each run gets its own file, so an earlier run's record
survives.

The logs sit outside the repository, so a run leaves the working tree clean and
the host reclaims old logs on its own schedule. Copy a log you need to keep
before the host clears it.

A run refuses to start when something other than its own private directory sits
at that path, and the error names both the path and the reason. On a host where
the temp directory is shared, another user can create that name first. Remove
the path it names, then run the command again.

A `tofu apply` or `tofu destroy` without `-auto-approve` keeps the terminal
instead, because you cannot answer an approval prompt you cannot see. The same
holds for every other tofu subcommand; only `plan`, `refresh`, and an
auto-approved `apply` or `destroy` write to a file.

- [MWAN](mwan/layout.md)
- [OPNsense](opnsense/operations.md)
- [Ansible](https://github.com/agoodkind/configs/blob/main/docs/ops/ansible/quality.md)
- [Infrastructure](https://github.com/agoodkind/configs/blob/main/docs/ops/infra/access.md)
