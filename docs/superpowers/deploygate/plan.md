> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Deploy gate outage survival: implementation plan

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs
> commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on
> this page refer to the tree as it was.

Six slices, each verified before the next. The overall acceptance bar is the
spec's criteria: a testbed deploy survives a simulated 120-second controller
outage spanning the reboot and reports the true verdict (AC1), while the
never-rebooted, gate-death, stale-verdict, and no-outage paths keep their
exact semantics (AC2 through AC5). Testbed first; production ships only
after every testbed criterion passes and only with an explicit go.

## 1. Add the wait-deploy verb to the gate binary

Add a `wait-deploy` mode that runs the existing reboot wait and egress wait
in sequence and writes one JSON verdict file atomically (temp file plus
rename): trace id, pre-reboot boot id, both exit codes, timestamps. Inputs:
vmid, old boot id, both budgets, trace id, verdict path. Extend the existing
table-driven gate tests with the chained verdicts and the atomic write.

Acceptance: `make test` green; a hand run on suburban writes a well-formed
verdict file for a no-op case.

## 2. Start the gate as a transient unit before the reboot

In [deploy-mwan.yml](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-mwan.yml), before the
reboot-scheduling task, add a delegated task that launches the verb under
`systemd-run` with a unit name carrying the trace id. This task runs while
the connection works; nothing after the reboot trigger may require the
delegate connection until collection succeeds.

Acceptance: a testbed deploy shows the unit running on suburban during the
reboot window and the verdict file appearing at its end.

## 3. Replace the delegated waits with a local collector

Delete the two delegated wait tasks. Add a controller-local task that loops:
raw SSH to each collect address in order, read the verdict file, validate
trace id and boot id, on a 10-second cadence until the combined reboot and
egress budgets plus 120 seconds of slack expire. On a dead unit with no
verdict, return gate death. Follow
with `meta: reset_connection` so later delegated tasks open fresh
connections.

Acceptance: a normal testbed deploy passes end to end through the collector
(AC5); a run with a doctored stale verdict file is rejected (AC4).

## 4. Declare per-environment collect addresses

Add the collect address list to the MWAN group vars: production lists the
tunnel address then the out-of-band address; the testbed lists its single
suburban path. Values come from the service inventory, not literals in the
play.

Acceptance: `configs lint` green; rendered plays show the right list per
environment.

## 5. Wire the verdict into the fail and rollback branches

The fail-without-rollback task keys on the reboot exit code from the parsed
verdict; the rollback chain keys on the egress exit code and runs only when
the delegate is reachable on its inventory address; an out-of-band-only
delegate fails the play loudly and names the watchdog as recovery owner.

Acceptance: forced-verdict runs on the testbed exercise both branches
(AC2 via a cancelled reboot timer; rollback branch via an injected failed
egress verdict).

## 6. Validate the outage on the testbed, then ship

Block the controller's route to suburban for about 120 seconds spanning the
reboot with a self-expiring nftables rule installed via a systemd timer, run
the full deploy, and confirm exit 0 with the true verdict (AC1). Kill the
gate unit mid-window in a second run and confirm the gate-death failure
(AC3). Production ships on the next explicitly authorized deploy, which also
proves the out-of-band fallback (AC6).

Acceptance: AC1 through AC5 demonstrated on the testbed with command output
recorded; AC6 noted as pending until the next production deploy.
