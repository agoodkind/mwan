> Read the [repository context](../../README.md) before using copied commands or historical plans.

# OPNsense config import mechanics

Importing a foreign router config means replacing the router's on-disk config
file and rebooting. Boot always runs schema migration and the full ordered
service-reconfigure chain, so a file swap plus reboot is the canonical way to
bring a foreign config fully live. Verified against OPNsense core 25.7, live
and upstream; re-verify these mechanics on a major upgrade.

## What each entry point actually does

The GUI restore accepts any well-formed XML with no field-level validation,
replaces the on-disk file atomically under an exclusive lock, writes an audit
entry and a backup snapshot, and then reboots by default. It does not migrate
the schema in-process on the default path; migration happens on the next boot.
If the uploaded XML fails to parse, the previous config is restored and
nothing changes on disk.

The REST API has no upload-and-replace endpoint. Its revert endpoint only
swaps in an existing local backup by name, without migration, service
reconfigure, or reboot, so the live system keeps running the old state until
something reboots it.

A direct file replacement plus reboot triggers the same boot-time migration
and the same ordered reconfigure chain as the GUI path. It skips the GUI's
extras: no atomic replace, no audit entry, no backup snapshot, and no
pre-reboot interface check.

## The interface-mismatch gate

If the new config names an interface device the host does not have, the GUI
postpones the reboot and routes the operator to interface assignment. The
boot-time check instead drops the boot into an interactive console prompt,
which stalls a headless guest. The pre-import interface gate in our transform
exists to prevent exactly that stall: before writing the new config, every
interface device it names must resolve on the host, or the import stops.

A single `<lock>` flag on any one interface entry disables the entire
boot-time mismatch check, not just the check for that interface. The GUI's
pre-reboot variant ignores locks and still checks everything. Treat any
`<lock>` in an import candidate as review-by-hand, because with one present a
bad device name sails through boot and surfaces only as a service failure.

## Import contract

Automated imports use direct file replacement plus reboot and adopt the GUI's
safety practices:

1. Run the pre-import interface gate; any unresolved device is a hard stop.
2. Write the new config to a temp file with root-only mode and atomically
   rename it over the target, instead of a plain copy.
3. Snapshot the current config into the router's local backup directory
   before the swap, so the GUI history and the REST revert endpoint have a
   rollback target.

Never flush the config history as part of an import; that deletes every local
rollback target.

## Rollback constraints

- Schema migration is a one-way door. After an import boots, per-model
  migrations have written into the config. Swapping an older file back works
  on disk, but the next boot migrates it forward again, and a missing
  downgrade path surfaces then, not at swap time.
- Rollback swaps must write the same backup snapshot forward imports write,
  or the GUI history shows no record of the rollback.
- There is no hot apply. Bringing a swapped config live without a reboot
  would require running the boot chain's ordered reconfigure calls
  individually; no single helper does this.
- There is no transaction across services. Once interfaces reconfigure, a
  later plugin failure does not roll them back. Design around apply, observe,
  decide, never all-or-nothing.

The testbed import runbook with its per-change gates is
[the testbed import page](testbed/import.md).
