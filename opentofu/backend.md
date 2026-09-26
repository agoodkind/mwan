> Read the [repository context](../docs/README.md) before using copied commands or historical plans.

# OpenTofu state operations

## Backend

OpenTofu state lives in the Cloudflare R2 bucket named tofu-state, outside the
homelab, so losing a hypervisor or a workstation never loses the state. The
backend speaks the S3 protocol with native lockfile locking, so concurrent runs
block each other instead of corrupting state.

Run every tofu command through the repo control tool so credentials flow from
the Ansible vault automatically:

```bash
./configsctl tofu plan
./configsctl tofu apply
```

configsctl reads five vault secrets and exports them for the child process:
the R2 access pair (`vault_r2_tofu_access_key_id`,
`vault_r2_tofu_secret_access_key`) authenticates the backend, and the two
Proxmox token secrets authenticate ordinary resources. The
`vault_suburban_proxmox_root_password` secret authenticates `root@pam` for
privileged testbed ISP containers because Proxmox rejects feature changes
from API tokens. Add that secret to the encrypted vault before running OpenTofu.
Nothing needs a `terraform.tfvars` file, and no secret is exported by hand. A
fresh checkout needs one `configsctl tofu init` before its first plan.

To rotate the backend credential, mint a new Cloudflare API token with the
Workers R2 Storage Write permission, derive the S3 pair (the access key id is
the token id, the secret is the SHA-256 hex of the token value), and feed both
names to `configsctl set-secrets`. The old token can then be revoked in the
Cloudflare dashboard.

The backend declaration itself lives in [backend.tf](https://github.com/agoodkind/configs/blob/main/opentofu/backend.tf); its endpoint
embeds the Cloudflare account id, which is stable and not a secret.

## Attach an existing resource

Confirm that the OpenTofu configuration matches the live object before
importing it. Read a guest with `qm config <vmid>` or `pct config <vmid>`.
Compare its VMID, storage, network devices, and hardware with the configured
resource.

The Proxmox provider uses these import identifiers:

- Network interfaces use `<node_name>:<interface>`.
- Virtual machines and containers use `<node_name>/<vmid>`.

Import the live object through the repo control tool:

```bash
./configsctl tofu import \
  '<resource_address>' '<provider_import_id>'
```

Run a complete plan after import. Review every difference using the drift rules
below before applying anything.

## Reattach a renumbered guest

Proxmox treats the virtual machine identifier (VMID) as the guest identity.
OpenTofu cannot update `vm_id` in place. Renumber the live guest, then reattach
its state. Never destroy and recreate the guest for a VMID change.

For a ZFS-backed guest, renaming each dataset preserves its child snapshots.

1. Stop the guest.
2. Rename every ZFS dataset from the old VMID to the new VMID.
3. Update every volume reference in the guest configuration. Update the active
   configuration and every `[snapname]` section.
4. Move the configuration to the new VMID, then start the guest.
5. Remove the resource from state, then import it with the new VMID:

```bash
./configsctl tofu state rm '<resource_address>'
./configsctl tofu import \
  '<resource_address>' '<node_name>/<new_vmid>'
```

Do not use `tofu state mv`. That command changes the resource address but leaves
the old `vm_id` in state. The next plan then proposes a replacement.

## Review drift before applying

Keep `lifecycle.prevent_destroy = true` on managed Proxmox resources. The guard
blocks deletes and replacements. It does not block a destructive update in
place. Add the guard to every newly imported resource.

Read the attribute changes in every plan. The change count does not distinguish
provider bookkeeping from a hardware change. Treat changes to `cpu`, `memory`,
and `disk` as live hardware changes that require confirmation.

A fresh import can add provider defaults such as `timeout_*` values and blocks
that state could not populate. Review those separately from hardware changes.

Configuration must not understate a live disk. Proxmox cannot shrink a
container or virtual machine disk safely. Update OpenTofu when a disk grows on
the hypervisor, or a later plan can propose a destructive shrink.

After repairing a plan failure, read the complete plan again. Hidden drift can
accumulate while plans remain broken.

The provider has these expected readback gaps:

- Ansible owns the live `args` field on the MWAN and OPNsense virtual machines.
  The Proxmox API rejects token writes to that field, so OpenTofu ignores
  `kvm_arguments` instead of removing the live value. The MWAN value sets its
  virtual socket context identifier, which tracks its VMID. The OPNsense value
  serves the out-of-band serial channel.
- Proxmox does not return injected SSH keys. Resources with a configured
  `initialization.user_account` ignore that block so a reimport does not force a
  replacement.
- Proxmox does not store the source template name in `pct config`. Imported
  containers ignore `operating_system.template_file_id`.
- The object stores' disks moved to each hypervisor's slow storage tier with
  `pct move-volume`, and the tack owner guests' backup roots are volumes hot
  plugged with `pct set`. The provider can make neither change without
  replacing the guest, so those containers ignore `disk[0].datastore_id` or
  `mount_point`.
- Ansible owns `/etc/network/interfaces.d/testbed-masquerade.conf` and the extra
  routable IPv6 address on `vmbr1`.
- A container state `id` contains only the VMID. Match a container by both
  `node_name` and `id` when inspecting state across hypervisors. A VMID alone
  does not identify its hypervisor.
