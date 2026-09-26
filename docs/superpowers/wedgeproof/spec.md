> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Wedge-proof the mwan-opnsense break-glass channel

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs
> commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on
> this page refer to the tree as it was.

## Goal

The OOB serial channel must never PERMANENTLY wedge the OPNsense guest. A brief,
self-clearing transient strand is tolerable. A wedge that needs a hard reset is not.

## Background and physics

qemu strands the in-flight virtio-serial descriptor only on a host-side chardev DISCONNECT.
While the chardev fd stays open, a guest write may spin or block transiently, but qemu
completes it once the host resumes reading. Therefore a permanent wedge is impossible as long
as the chardev fd never closes while the VM is up.

Proven this session by sub-second register sampling on the testbed OPNsense guest (qemu 11.0.0):

- fd-store OFF, drainer `kill -9` mid-pull: the host fd closed on process death, qemu saw a
  disconnect, and the guest wedged permanently (the positive control).
- fd-store ON, the same `kill -9`: the fd stayed open in the systemd store, so qemu saw no
  disconnect. The guest showed the wedge signature for ~2.7s, then self-cleared the instant the
  respawned drainer logged "adopted reclaimed chardev fd". Health returned at +8s.

A qemu-layer fix was ruled out. qemu 11.0.0 still strands; the chardev is a server-mode socket
(`server=on,wait=off`); `reconnect=` is client-mode only; there is no flush-on-disconnect flag.

The wedge signature is a CPU at RIP `ffffffff809e3xx` (virtqueue_poll) AND a CPU at
`ffffffff80c32c5x` (lock_delay), both with HLT=0. Idle is both CPUs HLT=1 at `ffffffff810b37c6`.

## Invariant 1: the chardev fd never closes while the VM is up

This eliminates the permanent wedge. Requirements:

- Store the chardev fd in the systemd fd-store before serving, on the fresh-dial path only.
- Set `FileDescriptorStorePreserve=yes` on the drainer unit so the store survives a crash and a
  stop, not only a clean restart.
- Never close the chardev fd on any drainer exit path. The fd outlives the process; only the
  next instance reclaims it.
- A fresh dial is the only host-side disconnect window. On startup, when the VM is up and a
  stored fd should exist but does not, treat it as a real event: log loudly and account for it,
  rather than silently dialing fresh and opening a disconnect window.
- The fd-store-off positive control becomes a permanent regression guard: with the store
  disabled, the same disruption must still produce the wedge signature, proving the store is
  load-bearing and the detector can see a real wedge.

## Invariant 2: the chardev reader never stalls

This bounds and shrinks the transient. Requirements:

- The chardev read loop must never block on the client write. A hung bridge (socket open, not
  reading) must not stall the read.
- Decouple read from write. A reader goroutine always drains the chardev into a bounded buffer.
  A writer goroutine forwards to the current client.
- On bounded-buffer overflow, drop the CLIENT (close it, force a fresh yamux session), never
  drop bytes. Dropping bytes corrupts the framed yamux stream; that was the litigated failure of
  the original design (reverted in commit 2f5f419). Dropping the client confines corruption to
  the now-dead session, which the client-side transfer stall watchdog and the session rebuild
  already cover.
- Minimize the respawn gap so a `kill -9` unread window shrinks from ~3s toward sub-second:
  `RestartSec` near 0 and minimal startup work before reading resumes.

## Components

- `mwan/go/cmd/mwan/opnsense_drain.go`: the reader/writer decouple (invariant 2) and the
  fd-store store/preserve/never-close paths plus the fresh-dial startup guard (invariant 1).
- The `mwan-opnsense-drain.service` unit and its ansible template:
  `FileDescriptorStorePreserve=yes`, `RestartSec` near 0.
- Tests: drain unit tests for lossless-under-client-churn, drop-the-client-on-overflow (no byte
  loss to a surviving session), and the reader-never-blocks property.

## Acceptance criteria

Validated by the sub-second wedge-proof harness (`qmp_sampler.py` at ~60ms cadence plus the
matrix driver), both directions at >=256MB cut deep mid-transfer, against every chardev-touching
disruption, accumulation with no reset between trials, >=30 genuine trials per cell, per-trial
artifacts retained:

1. Zero PERMANENT wedge: no trial shows the signature sustained until a hard reset. Every trial
   recovers on its own to a working exec probe, recover-at recorded over a >=180s window.
2. Any transient strand is bounded and self-clearing, with its duration measured from the
   sub-second samples.
3. Per-restart fd hand-off proven: "adopted reclaimed chardev fd" logged, counted from a
   pre-trigger timestamp.
4. Positive control still wedges: with the fd-store disabled, the same disruption produces the
   sustained signature.
5. Hung-client path bounded: a deliberately hung bridge does not stall the chardev read without
   bound; the reader keeps draining and the client is dropped.

## Out of scope

- A qemu-layer or AF_VSOCK transport change.
- Changing the documented transport invariants in docs/ops/opnsense/daemon.md ("what not to touch").
