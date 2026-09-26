> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Router guards implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development to implement this plan slice by
> slice. Each slice is one reviewed change.

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs
> commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on
> this page refer to the tree as it was.

**Goal:** Refuse a web-interface firmware upgrade when the router's virtual
hardware has drifted, and open a change window on every deliberate router
reboot so the watchdog stops blaming the gateway and can recover the router.

**Architecture:** Two guards on the guest's own hook stages, one declared
baseline in the service mapping, one drift timer and one extended watchdog on
the hypervisor.

**Tech Stack:** FreeBSD hook scripts calling the monolith's FreeBSD build, Go
for the daemon and watchdog, Ansible for install, Proxmox snapshots for
recovery.

**Spec:** [spec.md](spec.md)

## Global constraints

- Do not change any documented transport invariant. The new event is one more
  consumer of the existing channel: no raised write cap, no liveness frame, no
  changed acknowledgement pacing, no character device close.
- New tools are subcommands of the one binary, never separate binaries. A hook
  script is a thin shim that calls a verb.
- Every input value is declared in group vars, the service mapping, or
  OpenTofu, and read bare. Reading it defensively is banned and linted.
- The router guest has no MWAN configuration directory; its settings are
  FreeBSD run configuration entries.
- Snapshots carry no saved memory, and their names stay under forty
  characters because Proxmox truncates past that silently.
- Go work runs through the repository's make targets, never raw Go.
- Units and files reach hosts through Ansible, never hand-edited.
- Testbed first, production second, and never both hypervisors in one command.

## 1. Declare the baseline

Add the router's per-device record to the service mapping: slot, bridge,
hardware address, and adapter model per device, for the production and testbed
routers. The model is load-bearing rather than descriptive, since it resolves
to the driver and the driver is half of every FreeBSD interface name. Record
the model as the hypervisor writes it; the resolution from model to driver
belongs with the guard that computes names, not with the record. Render
the record onto each hypervisor for the drift timer. Nothing is rendered onto
the guest: a guest-local copy is written at deploy time and cannot answer
what the hypervisor holds now, which is the question the upgrade guard asks.
Leave the OpenTofu resource alone, including its ignored network devices, and
correct its comment to name the service mapping as the baseline's home.

Acceptance: an OpenTofu plan against production comes back empty, and both
hypervisors carry the rendered baseline after a deploy.

## 2. Refuse an upgrade on drift

Add a verb to the FreeBSD build that asks the hypervisor for an answer and
exits non-zero unless one comes back inside a bound saying the reboot is
safe. The request carries an identifier the answer must echo, and the verb
accepts only an answer bearing the identifier it sent: ordering by time
compares two unsynchronised clocks and lets a late answer to a timed-out
request satisfy the next run. Draw the identifier from a source whose values
do not recur across runs or restarts, since a resetting counter gives a
delayed answer a matching identifier on a later request and reopens the hole
the echo closes. Test that case directly, with a daemon restart between the
request and the late answer. The request rides the event stream the
transport slice adds, and the answer rides the direction the host already
dials, so neither side changes who dials whom. Reject a cached answer, which
can sit younger than any staleness bound and still predate the change that
matters.

The hypervisor answers from two comparisons it alone can make. The first is
whether this reboot changes a name the guest uses: resolve each of its devices
to the FreeBSD driver its adapter model attaches to, group by driver, number
each group in slot order, and check that every device the guest reports
running would receive the full interface name it carries today. Carry the
model-to-driver resolution as a stated table, and refuse on a model the table
does not carry rather than guessing a driver.

Four ways to get that comparison wrong, each of which this project already
walked into. Comparing the two device lists as wholes fails a guest an
appended device cannot affect. Dropping the new device before numbering passes
a guest an inserted device of the same driver does affect. Numbering every
device in one sequence regardless of driver fails a guest an inserted device
of another driver does not affect. Grouping by the model as written rather
than the driver it resolves to splits one shared sequence in two and passes an
insertion that renumbers, since several models attach to a single driver. And
comparing numbers rather than names passes a same-slot model change that takes
an interface name away.

The second comparison is its own configuration against the declared record,
which is whether undeclared hardware is present. Either disagreement is a
refusal, since an undeclared device that changes no name still must not
survive into a reboot unexamined.

No answer inside the bound is a refusal, as is every other failure in the
verb. A documented override marker turns the refusal into a warning and a
zero exit. Install the hook through the same task that installs the daemon's
other guest files.

Acceptance: AC1, AC2, AC3, AC10, AC12, AC13, AC14, AC15, AC16, AC17, AC18,
AC19, AC20, and the hook half of AC4.

## 3. Detect drift from the hypervisor

Add a timer unit on each hypervisor that reads every guarded guest's live
device list the way the existing runtime-network discovery task does, over
SSH and never the Proxmox HTTP API, diffs each device's slot, bridge, hardware
address and adapter model against the baseline, and raises one notifier alert
per guest on any difference. Detection only; it changes nothing on any guest.

Acceptance: AC5.

## 4. Carry a guest event to the hypervisor

Add a server-streaming subscription to the serial service that the guest
pushes events into, carrying both the shutdown notice and the upgrade guard's
request for a fresh answer, and a verb the guest calls locally to publish
one. Answers travel back over the direction the host already dials. The
host bridge opens that subscription per session using the same session dialer
its heartbeat already uses, and resubscribes when a session is rebuilt. On
receiving a router shutdown event the bridge records an open change window on
the hypervisor where the watchdog reads it, then answers the guest so the
publishing verb learns the window exists rather than only that the bytes
left. Keep the event small enough that it never approaches the channel's
write cap.

Acceptance: the bridge records a window within seconds of a testbed guest
publishing the event by hand and the verb reports the confirmation, and AC8
holds when the bridge is restarted mid-arm.

## 5. Arm the window and cover the router

Install the guest's stop-stage hook, which publishes the shutdown event and
waits inside a short bound for the hypervisor to confirm the window is
recorded. Any failure, including an unconfirmed window, is permission to
continue the reboot, and the hook says which happened. It neither persists
the event across the reboot nor retries without bound: a queued retry here is
a guest-to-host write held open exactly where the channel documents a wedge,
and the window is moot once the guest is down.

Teach the watchdog a second guarded guest: take healthy snapshots of the
router on the terms it already uses for the gateway, suppress gateway
rollback while a router change window is open, and roll the router back to its
newest healthy snapshot when the window closes with egress still lost. Extend
the watchdog's existing tests for the new decision branch, since that branch
has no coverage today.

Acceptance: AC6, AC7, AC11, and the stop-hook half of AC4.

## 6. Correct the documentation this changes

The watchdog page documents one guarded guest and exactly two rollback
signals, so rewrite it for the second guest and the third signal. Extend the
router operations rule on hot-added devices with the consequence this incident
found, that inert devices renumber every interface on the next boot. Correct
the layout page, which says only the testbed hypervisor runs the serial
bridge while production runs it too. Add the drift timer to the production
hypervisor's service list.

Acceptance: each page states current behavior, and no page repeats a fact that
another page owns.

## Self-review

The dependency order changed during review. Slice 2's guard was going to read
a baseline rendered onto the guest, and that cannot work: the copy is written
at deploy time, so a slot change made afterwards leaves the stale copy and the
live order agreeing and the guard passes the change it exists to catch. The
guard now compares the guest against what the hypervisor publishes, so slice 2
depends on the session work in slice 4. Slices 1, 3 and 6 still stand alone,
and slice 5 depends on slice 4.

The spec's AC9 is residual by construction. Both routers carry two devices;
what differs is the slot layout, consecutive on the testbed and
non-consecutive in production. The drift check's handling of a
non-consecutive list is therefore proven by the first production run rather
than by any testbed run, and no slice claims to close it.

One known gap is carried deliberately. Slice 5 gives the router a rollback for
a deliberate reboot that breaks egress. A router that fails on its own still
produces the misattribution this incident showed, because the watchdog's
evidence is unchanged in that case. That is the separate watchdog work, and
this plan does not close it.
