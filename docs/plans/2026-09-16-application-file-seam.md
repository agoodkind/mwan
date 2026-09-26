> Read the [repository context](../README.md) before using copied commands or historical plans.

# Plan: one home per application file

Design: [Application files belong to the binary; site files belong to configs](../superpowers/specs/2026-09-16-application-file-seam-design.md). Links point at commit `8693b22f` of `agoodkind/configs` and `fe3724d6` of `agoodkind/configsctl`.

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on this page refer to the tree as it was.

Order follows the repo split goal: `MWAN-490` first, then the `mwan install` verb under `MWAN-305`, then `opnsensectl` under `MWAN-410`. Each slice is one reviewed pull request, deployed to the testbed from `main` and validated live before the next, then to production on explicit approval per command. Every slice keeps routes, rules, and the served wanconfig tree byte-identical to a capture taken before it.

## Slice 1. Playbooks pull and verify the releases (`MWAN-490`)

1. Add `ansible/playbooks/tasks/github-release.yml`, run on the controller, driven by vars: `release_repo`, `release_tag`, `release_assets` (a list of `{name, checksum}`), `release_stage_root`. Per asset: `get_url` from the release download URL with `checksum: sha256:<declared>`, then `gh attestation verify <file> --owner agoodkind`, then `unarchive` into `<stage>/<tag>/<name without platform suffix>/`. Resolve the tag to its full commit with `gh api repos/<repo>/commits/<tag>`. Set the facts the plays read today: `mwan_release_tag`, `mwan_release_commit`, `mwan_release_dir`, `wanconfig_stack_dir`, `opnsensectl_release_tag`, `opnsensectl_release_commit`, `opnsensectl_release_dir`. Drop `wanconfig_stack_manifest`; nothing reads it.
2. Pin in `group_vars`: `mwan_release_tag` and `mwan_release_assets` for the gateway sources, `opnsensectl_release_tag` and `opnsensectl_release_assets` for the router and hypervisor sources, following [`clyde_suburban_servers.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/inventory/group_vars/clyde_suburban_servers.yml#L5-L9).
3. Import the task in [`deploy-mwan.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/deploy-mwan.yml), [`deploy-mwan-failover.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/deploy-mwan-failover.yml), [`deploy-testbed.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/deploy-testbed.yml), [`deploy-proxmox.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/deploy-proxmox.yml), [`deploy-opnsense.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/deploy-opnsense.yml). The existing copy tasks keep their paths (`<dir>/linux_amd64/mwan`, `<stack dir>/debs/`).
4. Land while `configsctl` still accepts `--release`; run deploys without the flag so the play's own facts are used. Prove on the testbed: `deploy-mwan`, `deploy-proxmox`, `deploy-opnsense` from `main` with no release flag; [`verify-mwan-release.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/tasks/verify-mwan-release.yml) and [`verify-opnsensectl-release.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/tasks/verify-opnsensectl-release.yml) assert the pinned commits. Then production.

## Slice 2. Authorized keys back to `deploy-ssh-keys.yml` (`MWAN-490`)

1. Replace the [`go run goodkind.io/configsctl/cmd/deploy-authorized-keys` task](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/deploy-ssh-keys.yml#L94-L115) with the original shape: fetch `https://github.com/{{ github_ssh_keys_user }}.keys` with `uri`, strip, dedupe, and sort the lines in `set_fact`, append the sshpiper restricted key for the second bundle, write both bundle paths with `copy` and `content:`, `assert` the list is not empty. Remove the `configsctl_version` lookup.
2. Prove on the testbed hypervisor and one guest: the deployed `authorized_keys` equals the previous bundle byte for byte.

## Slice 3. `configsctl` loses staging and authorized keys (`MWAN-490`)

1. In `configsctl`, delete [`internal/release`](https://github.com/agoodkind/configsctl/tree/fe3724d65e8f9b59980b6020f0853650f4077edd/internal/release), [`internal/authorizedkeys`](https://github.com/agoodkind/configsctl/tree/fe3724d65e8f9b59980b6020f0853650f4077edd/internal/authorizedkeys), [`cmd/deploy-authorized-keys`](https://github.com/agoodkind/configsctl/tree/fe3724d65e8f9b59980b6020f0853650f4077edd/cmd/deploy-authorized-keys), the `--release` and `--opnsensectl-release` flags in [`cmd/configsctl/main.go`](https://github.com/agoodkind/configsctl/blob/fe3724d65e8f9b59980b6020f0853650f4077edd/cmd/configsctl/main.go), their tests, and the `README` lines. The command list becomes lint, validation, and safe running.
2. Bump the `configsctl` pin in [`configsctl`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/configsctl) to that release; update the [`Rakefile`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/Rakefile) bypass and every doc that shows `--release`.
3. Prove: every deploy playbook runs from `main` on the testbed with the new pin; then production.

## Slice 4. `mwan install` verb, static files (`MWAN-390`, `MWAN-391`, `MWAN-392`, `MWAN-393` to `MWAN-396`)

1. In `mwan/go`, embed the four `mwan` units, the schema modules, `nacm-anonymous.xml`, `rousette.service`, `nghttpx-wanconfig.service`, `99-quiet-console.conf`, and the two service overrides. Move each file from its `configs` path (linked in the design's table) into the module; the schema modules move from `third_party/yang` and `mwan/yang` into the module's embedded tree, and the [Makefile schema gates](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/mwan/go/Makefile#L160-L198) and the Go tests read the embedded copies.
2. Add `mwan install`: help by default, `--apply` required to act; with `--apply`, write each file when content differs, `daemon-reload` and enable units through the systemd D-Bus API, install or update the schema and import the policy through the sysrepo binding, print one line per changed file, exit non-zero on failure, idempotent on the second run. Add `mwan install --print-schema <dir>` for the controller-side validation.
3. [`deploy-mwan.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/deploy-mwan.yml#L264-L284) and [`wanconfig-stack.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/tasks/mwan-vm/wanconfig-stack.yml#L91-L192) stop copying those files and run `mwan install --apply` after installing the binary; the `yanglint` validation reads the printed schema directory. Remove the `third_party/yang` submodule from `configs` once nothing reads it.
4. Prove on the testbed: a deploy from a checkout with no submodule initialized succeeds; installed files hash-equal the embedded copies; a reboot converges; capture comparison unchanged. Then production.

## Slice 5. Cancelled (`MWAN-382` to `MWAN-386`)

`sysctl-mwan.conf`, `rt_tables` and `nghttpx-wanconfig.conf` need site values, so they stay in `configs` under the rule. `deploy-mwan.yml` keeps templating all three.

## Slice 6. `opnsensectl install` verb (`MWAN-410`)

1. In `opnsensectl`, embed the `rc.d` script, run shim, `rc.conf` defaults, loader entry, and the two host units. Add `opnsensectl install` with the same contract (help by default, `--apply` to act) for the router and the hypervisor.
2. [`mwan-opnsense-deploy.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/tasks/mwan-opnsense-deploy.yml#L60-L100) and [`mwan-opnsense-host-deploy.yml`](https://github.com/agoodkind/configs/blob/8693b22f892bc745fa16631e4b1fc874fb9ee53e/ansible/playbooks/tasks/mwan-opnsense-host-deploy.yml#L63-L100) stop copying those files and run the verb. Prove on the testbed router and hypervisor: the router contract holds (install path, symlink, `rc.d` supervision, run shim, `argv0` fast path, `daemon version` over the channel). Then production, one host per session.

## Verification that closes the plan

- `deploy-mwan`, `deploy-proxmox`, `deploy-opnsense`, `deploy-testbed`, and `deploy-mwan-failover` run from `main` on the testbed and production with no `--release` flag, no `configsctl` staging, and no static application file copied from the checkout. The deploy keeps rendering the templated files: `network.json`, `config.toml`, `sysctl-mwan.conf`, `rt_tables`, `nghttpx-wanconfig.conf`, the 802.1X chain, the console drop-ins, and, until `MWAN-341`, `nftables.conf`. The daemon renders the per-provider networkd units at runtime under `MWAN-491`, outside this plan.
- `configsctl`'s command list is lint, validation, and safe running.
- `configs` holds no `third_party/yang` submodule.
- A deploy from a fresh shallow clone with no submodule produces a gateway byte-identical, in routes, rules, and served tree, to one deployed from a full checkout.
