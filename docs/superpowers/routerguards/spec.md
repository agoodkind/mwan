> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Guard the router against hardware drift and misattributed rollback

## Goal

An OPNsense firmware upgrade started from the web interface refuses to run
when the router's virtual hardware has drifted from its declared shape, and
any deliberate router reboot opens a window on the hypervisor that stops the
watchdog blaming the gateway and lets it recover the router instead.

## Defect this replaces

On 2026-08-01 the hypervisor API added two virtio NICs to the router guest
under the OpenTofu provider's token. Nothing on either host compares a guest's
live hardware against its declared shape, and a hot-added NIC binds nothing
until reconfigured, so the drift stayed invisible for thirty-two days. On
2026-09-02 an operator upgraded the router firmware from the web interface.
That was the guest's first boot carrying four NICs. FreeBSD numbered them by
PCI slot, so the transit link landed on the LAN bridge. No BGP session
formed, and the router held no default route in either family.

Two guards existed and neither helped. The router's own guarded upgrade path
snapshots the guest and validates BGP sessions and kernel default routes at
blocker severity, but it is a command line verb and the upgrade started in a
browser, so it never ran. The watchdog did run. Its only evidence is its own
egress probes through that router, so it found a gateway deploy seven minutes
old inside its window and restored a healthy gateway to a pre-deploy
snapshot. Production lost egress for thirteen minutes, and the gateway lost a
cutover that had already passed its verdicts.

## Contract

1. **The upgrade refuses on drift.** A hook on the guest's upgrade stage
compares the interfaces FreeBSD sees against a declared baseline and exits
non-zero when they disagree. OPNsense then aborts the upgrade before the
kernel apply and before any reboot, and the hook's reason reaches the
operator in the firmware log the web interface already streams.

The question the hook answers is whether this reboot changes any interface
name the guest is using, and only the hypervisor holds that answer. A FreeBSD
interface name is its driver plus a unit number. The unit counts within that
driver alone, assigned as devices attach in slot order at boot, and never
reassigned on a running system. So the guest's live names record the drivers
and slot order it booted with, while the hypervisor's current configuration
determines the names it will boot into next.

The driver is what the adapter model resolves to, and that resolution is many
to one. Several hypervisor models attach to a single FreeBSD driver and then
share one unit sequence, so the model is an input to the answer rather than
the answer itself. The hook groups by the driver each model resolves to,
never by the model as written.

The comparison is over prospective interface names, not over the two device
lists as wholes and not over positions in one sequence spanning every driver.
Resolve each of the hypervisor's devices to its driver, group by driver,
number each group in slot order, and read off the full name each device the
guest already runs would receive. The hook refuses when any of those differs
from the name that device carries today.

Per-driver numbering is what makes the answer right, in both directions. A
device that resolves to a different driver, inserted anywhere, counts in its
own group and moves no name in another, so the guest passes. A device
resolving to the same driver, inserted before or between the existing slots,
takes a unit already in use in that group and pushes every later one up, so
the guest refuses. Numbering every device in one sequence would refuse the
first case falsely, and grouping by model rather than driver would pass the
second whenever the two devices are written as different models that resolve
alike.

Comparing names rather than numbers is what closes the other direction. Change
one device's adapter model in place and its slot does not move, so its unit
can stay the same while its driver changes and its name with it. That guest
loses the interface its configuration names, which is the outage in a
different costume, and only a name comparison sees it.

Both halves need the model, so the record the hypervisor compares against
carries it beside the slot and the hardware address, and the answer it
computes is a name rather than a position. The model-to-driver resolution the
hook applies is a stated table rather than an inference, and a model absent
from it is an answer the hook cannot compute.

The hypervisor is the one that knows, and it dials the guest rather than the
other way round. So the hook asks for an answer and refuses unless one comes
back, inside a bound. A cached answer will not do: one can sit younger than
any staleness bound and still predate the change that matters, and a bound
alone would make the dead-channel case depend on how recently the channel
died rather than on whether it is dead.

Each request carries an identifier the answer must echo, and the hook accepts
only an answer bearing the identifier it sent. Ordering by time would not do
either: it compares two clocks nobody synchronised, and it lets a late answer
to a request that already timed out satisfy the next hook run, which is a
stale verdict wearing a fresh timestamp.

The identifier must not repeat, across hook runs and across restarts of
anything that issues one. A counter that resets, or any scheme whose values
recur, hands a delayed answer from an earlier request a matching identifier
on a later one and reopens the hole the echo exists to close. Draw it from a
source with enough entropy that a collision is not a case anyone plans for.

The hook refuses on either of two disagreements, because a reboot is when
latent drift detonates and it is the last cheap moment to catch it. An
interface the guest uses that would come back under a different name means
this reboot breaks the configuration that names it. The hypervisor's own
configuration disagreeing with the declared record means hardware nobody
declared is present, which is the condition that sat unnoticed for thirty-two
days before the outage. Neither implies the other. A device appended after the
existing slots and left undeclared changes no name and still refuses, on the
declared comparison alone. The same device once declared changes no name and
passes both. The hypervisor holds both facts, so it answers with both.

A copy of the declared baseline rendered onto the guest cannot answer this.
That copy is written at deploy time, so a slot change made afterwards leaves
the stale copy and the live order agreeing, and the guard passes the very
change it exists to catch. The 2026-08-01 write was caught by a count only
because a hot-added device is visible to the running kernel; two devices
exchanging slots are not.

The guest cannot see bridge membership, and it is not the authority on what
was declared. Those belong to the host-side check.

2. **The refusal fails closed and can be overridden.** An answer the hook
cannot obtain is a refusal, stated as such, including one it cannot obtain
because the channel is down. An operator who has decided the guard itself is
the broken thing creates a documented marker file, and the hook then logs
loudly and permits the upgrade. Refusing on a dead channel is deliberate: the
hook cannot tell a quiet hypervisor from a renumbering one, and a postponed
upgrade is recoverable where a renumbered router is not.

3. **The declared baseline has one home.** The per-device record lives in the
service mapping, which already owns pinned hardware addresses, and carries the
slot, the bridge, the hardware address, and the adapter model, because the
model resolves to the driver and the driver is half of every interface name. Both consumers
read it on the hypervisor, where the live configuration it is compared against
also lives: the drift timer reports a difference continuously, and the upgrade
answer carries the same comparison at the moment it matters most. Nothing is
rendered onto the guest, which is neither the authority on what was declared
nor able to see what the hypervisor holds now.

OpenTofu keeps ignoring this guest's network devices: its provider models
network devices as a sequential list, the guest's devices are not sequential,
and that mismatch is what wrote the placeholder NICs in the first place.

4. **Drift surfaces within the hour.** A timer on the hypervisor reads each
guarded guest's live device list and alerts through the existing notifier on
any difference from the baseline, whatever caused it. It reads the guest
configuration the way playbooks already do and never calls the Proxmox HTTP
API.

5. **A deliberate reboot opens a router change window.** A hook on the guest's
stop stage sends one event to the hypervisor before the guest goes down. That
stage runs for every deliberate reboot and shutdown whatever started it, and
does not run on a panic or a power cut, so a crash still presents as a fault.
The event travels as a server-streaming subscription on the existing serial
service, which the host bridge holds open per session beside its heartbeat,
and the bridge records the window on the hypervisor where the watchdog reads
it.

6. **The window protects the gateway and covers the router.** While a router
change window is open the watchdog does not roll the gateway back for lost
egress. If egress has not returned when the window closes, it rolls the
router back instead. The rollback target is the newest healthy snapshot the
watchdog itself took of the router, on the same terms it already takes them
for the gateway, because a snapshot taken once the guest is already shutting
down would capture a staged upgrade rather than the state before it.

7. **The hooks never wedge their caller, and they fail in opposite
directions.** The hook dispatcher runs each hook synchronously with no
timeout of its own, so each hook bounds its own work. There the two part
company. The upgrade-stage hook treats every failure as a refusal, because
refusing costs a postponed upgrade while proceeding blind costs the router.
The stop-stage hook treats every failure as permission to continue, because
losing a window costs a guard while a hook that hangs costs the reboot.

8. **The window is armed before the guest goes down, or not at all.** The
stop-stage hook waits, inside its own bound, for the hypervisor to confirm
the window is recorded. Without that confirmation it proceeds unprotected and
says so. It does not persist the event across the reboot and does not retry
without bound: a queued retry on the shutdown path is a guest-to-host write
held open exactly when the channel documents a wedge, and after the reboot
the window it would have opened is moot.

## Boundaries

The transport invariants stay exactly as they are. The new event is one more
consumer of the existing channel, never a reason to raise the write cap, add a
liveness frame, change the acknowledgement pacing, or close the character
device.

OpenTofu does not gain management of this guest's network devices.

The command line upgrade path keeps its own snapshot, validation matrix and
rollback. This work guards the reboot rather than replacing that tool, and
does not require an operator to use it.

Drift is reported, never repaired. A hot-added device binds nothing until
reconfigured, so automatic removal is its own foot-gun.

A router that fails on its own rather than deliberately stays out of scope and
belongs to the separate work on teaching the watchdog to check the router
before blaming a gateway deploy.

## Acceptance criteria

AC1: with a spare device added to the testbed router after its existing slots
and no reboot taken, an upgrade started from that router's web interface
aborts naming undeclared hardware, the firmware log carries the hook's
reason, and the guest still runs its previous firmware. This placement
renumbers nothing, so the refusal comes from the declared comparison rather
than the order one.

AC2: with the spare device removed, the same upgrade proceeds and completes.

AC3: with the spare device present and the override marker in place, the
upgrade proceeds, and the hook's warning appears in the firmware log.

AC4: both hooks are present and executable on the guest after a completed
firmware upgrade.

AC5: adding a device to a guarded testbed guest raises one alert within a
single timer interval naming the guest and the difference, and removing it
clears the alert.

AC6: rebooting the testbed router from its web interface records a change
window on the hypervisor before the guest stops, and the same reboot taken
while a testbed gateway deploy sits inside its own window leaves the gateway
untouched.

AC7: with the testbed router's transit link broken so egress does not return,
the watchdog rolls the router back to its newest healthy snapshot, and the
post-rollback checks the router runbook already lists all pass.

AC8: restarting the host bridge while a testbed router reboot is arming leaves
both guest processors idle rather than one polling a virtual queue and one
delayed on a lock, and the router completes its reboot.

AC9 (residual, production topology only): both routers carry two devices, but
the testbed router occupies consecutive slots while the production router
occupies the first and fourth. The drift check's handling of a
non-consecutive slot list is therefore proven only by the first production
run after this ships.

AC10: with the testbed router's two devices exchanged between their slots and
no reboot taken, an upgrade started from that router's web interface aborts
and names the reordering. Nothing changes inside the running guest in this
case, so neither a set nor a count nor any guest-local copy of the baseline
would have caught it.

AC11: with the host bridge stopped, a testbed router reboot still completes in
its normal time, and the hook reports that it could not confirm a change
window.

AC12: with the host bridge stopped, an upgrade started from the testbed
router's web interface aborts, naming the unreachable hypervisor as the
reason, and proceeds when the override marker is present. The result does not
depend on how long ago the bridge stopped.

AC13: with the testbed router's two devices exchanged between their slots at
the hypervisor and the declared record edited to match, and no reboot taken,
an upgrade still aborts. Declaring a layout does not make a guest that has
not booted into it safe to reboot, and this is the case that separates the
two comparisons: the declared one now passes while the order one refuses.

AC14: with a device appended after the testbed router's existing slots and
the declared record edited to match, an upgrade proceeds. Appending renumbers
nothing, so a refusal here would be a false one.

AC15: an answer to a request that already timed out is refused by the next
hook run, including when the guest daemon restarted between the two, and
including when the answer says the reboot is safe.

AC16: with a device resolving to the same driver inserted before the testbed
router's existing slots and the declared record edited to match, an upgrade
aborts. The guest's own interfaces are unchanged and every one of them would
come back under a different name, so this is the case that proves the
numbering runs over the hypervisor's devices rather than over the ones the
guest already has.

AC17: with a device resolving to a different driver inserted before the
testbed router's existing slots and the declared record edited to match, an
upgrade proceeds. It counts in its own driver's sequence and moves no name in
another, so numbering every device in one sequence would have refused this
falsely.

AC18: with a device written as a different adapter model but resolving to the
driver the guest already uses, inserted before the testbed router's existing
slots and the declared record edited to match, an upgrade aborts. The two
models differ while the driver they share does not, so grouping by the model
as written would have passed a reboot that renumbers every interface in that
driver's sequence.

AC19: with one of the testbed router's devices changed to a model resolving to
a different driver in its own slot, and the declared record edited to match,
an upgrade aborts. The slot does not move and the unit can stay the same, so
only a comparison of full interface names catches that the guest loses the
interface its configuration names.

AC20: with one of the testbed router's devices changed to a model the
resolution table does not carry, and the declared record edited to match, an
upgrade aborts naming the unknown model, because a driver the hook cannot
resolve is a name it cannot predict. Editing the record is what isolates the
cause: left unedited, the declared comparison refuses on its own and proves
nothing about resolution.
