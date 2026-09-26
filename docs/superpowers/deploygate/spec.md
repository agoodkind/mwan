> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Deploy gate outage survival

The MWAN deploy's reboot gate reports the true verdict even when the
controller loses every route to production during the reboot window. The
controller's own tunnel rides the VM being rebooted, so the design assumes
the controller can reach nothing on the production side, vault included,
for the whole window.

## Defect this replaces

The play runs the gate as tasks delegated to the hypervisor, and delegation
opens SSH from the controller at task start. When the tunnel drops with the
rebooting VM, that connect fails and Ansible aborts the play as UNREACHABLE,
even though the reboot completed and every service converged. The 2026-08-07
production deploy hit exactly this: exit 1, unreachable=1, healthy VM.

## Contract

1. **Observation is hypervisor-local.** Before the reboot is scheduled, and
   while the connection provably works, the play starts a transient systemd
   unit on the hypervisor running a new gate verb, `wait-deploy`. The verb
   chains the existing reboot verdict (boot id change) and egress verdict,
   then atomically writes one JSON verdict file keyed by the deploy trace id
   and the pre-reboot boot id. Observation never depends on the controller
   again.
2. **Collection stays off the Ansible connection plane.** A controller-local
   poller reads the verdict file over raw SSH, trying each collect address in
   order (tunnel first, out-of-band second; the list is per-environment group
   vars), on a fixed cadence until the gate budget plus slack expires.
   Ansible never retries an unreachable task, so no delegated task may sit in
   the collection path.
3. **Verdict semantics are unchanged.** A reboot that never fired fails the
   play loudly with no rollback, because the VM still runs the deployed
   config. A failed egress verdict runs the play's existing rollback tasks
   after a connection reset; if the hypervisor is reachable only out-of-band
   at that moment, the play fails loudly instead, and recovery belongs to
   the watchdog. No verdict within budget fails loudly with no rollback.
4. **Staleness and death are distinguishable.** The collector accepts only a
   verdict whose trace id and pre-reboot boot id match this run. A missing
   verdict with a dead transient unit is reported as gate death, never as
   silence.

## Boundaries

The watchdog remains the only recovery authority when the controller cannot
reconnect; the play never blocks past its budget and never rolls back on a
verdict it could not collect. The out-of-band collect path is a lossy WISP
link, so the poller treats per-attempt failures as normal and only the
overall budget decides.

## Acceptance criteria

- AC1: A testbed deploy whose controller-to-hypervisor route is blocked for
  about 120 seconds spanning the reboot completes exit 0 and reports the
  true verdict after the route returns.
- AC2: A deploy whose scheduled reboot genuinely never fires still fails
  loudly without rollback.
- AC3: A deploy whose gate unit is killed mid-window fails loudly and names
  gate death as the cause.
- AC4: A stale verdict file from a prior run is never accepted as this
  run's verdict.
- AC5: A normal testbed deploy with no outage behaves exactly as today:
  exit 0 and the same verdicts.
- AC6 (residual, production topology only): the out-of-band fallback address
  cannot be exercised on the testbed; the first production deploy after this
  ships is its proof.
