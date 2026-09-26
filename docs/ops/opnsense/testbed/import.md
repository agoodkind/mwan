> Read the [repository context](../../../README.md) before using copied commands or historical plans.

# OPNsense Testbed Config Import

This runbook is current guidance for importing a production-shaped router
config into the suburban testbed OPNsense guest,
`router.suburban.goodkind.io`. The import mechanics and rollback constraints
are in [the import mechanics page](../import.md).

## Core Rules

Restore each candidate config onto a wiped or freshly restored OPNsense
testbed baseline. Do not layer candidate configs on top of an already-mutated
router state.

Do not use MWN1, the OPNsense gRPC helper exposed by `mwan-opnsense`, as the
reload mechanism during config-import debugging. Use direct console, QEMU Guest
Agent, or SSH for restore and reload. Test MWN1 only after the router is stable
again.

Use the serial console while reloads or reboots are in progress. If SSH drops,
continue observing through the console path.

An import swaps the entire `/conf/config.xml`, which includes the `<apikeys>` block. The testbed substitutions transform produces an XML with no API keys at all, so the freshly-imported OPNsense has no API access until you mint one. After every import, mint a fresh root API key via the PHP `OPNsense\Auth\API->createKey('root')` helper and store the resulting key and secret in the vault with `configsctl set-secrets`.

## Every Change Gate

Run this gate for every config change, including small XML edits.

### 1. Scope Check

- Confirm the target is the testbed OPNsense guest, `router.suburban.goodkind.io`.
- Confirm no command names `vault` or a production guest. No testbed guest
  shares a VMID with production, so a command aimed at the wrong hypervisor
  fails rather than reaching a counterpart, but the target still has to be
  right.
- Confirm the intended hypervisor is `suburban`.

### 2. Baseline Capture

- Capture Proxmox VM state and QEMU config.
- Capture the router config's hash and byte size.
- Verify QEMU Guest Agent ping and guest hostname.
- Verify the serial console path is reachable.
- Verify SSH reachability only as a convenience signal, not as recovery proof.

### 3. Backup Before Mutation

- Take a Proxmox snapshot without `--vmstate 1`.
- Back up the raw router config file.
- Record the pre-change config hash.
- Write down the rollback command before applying anything.

### 4. Apply Method

- Restore the candidate config onto a wiped or freshly restored OPNsense
  baseline.
- Do not use MWN1 for reload.
- Use direct console, QEMU Guest Agent, or SSH for config restore and reload.
- Watch the serial console during reload or reboot.

### 5. During Reload

- Watch serial console output.
- Tail relevant OPNsense logs, especially `configd` and system logs.
- Record the exact command that hangs, exits, or drops connectivity.
- Do not call a reload hung without console or log evidence.

### 6. Post-Change Validation

- QEMU Guest Agent works.
- Serial console is responsive.
- SSH is reachable.
- Web UI TCP `443` is reachable.
- DNS returns `NOERROR`.
- Basic `configctl` actions work.
- Interface device bindings match the expected testbed topology.
- Default routes and gateways are sane.
- pf rules load.
- NAT sanity checks pass.
- FRR/BGP state is sane if FRR is expected.
- The [serial recovery checks](access.md) pass after the router is stable.

### 7. Failure Rule

- If any baseline check fails, stop.
- Roll back immediately.
- Re-run the entire baseline after rollback.
- Record the failing command, relevant logs, and active config hash.
