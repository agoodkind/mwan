> Read the [repository context](../../README.md) before using copied commands or historical plans.

# The mid-transfer write wedge

A bridge disconnect can hang the guest when the guest is writing to the host. The guest stays
hung until you hard reset the VM. This page covers the trigger, the mechanism, recovery, and how
to reproduce and inspect the wedge. For the daemon and transport it constrains, see
[the OOB daemon doc](daemon.md).

The mechanism below is the best-supported explanation of how the wedge happens.

## Trigger

The wedge needs a guest-to-host write in flight when the host side drops.

A `file pull` or a large command output keeps the guest writing toward the host. A bridge restart
or crash during one can wedge the guest. A `file push` rarely wedges, because the guest is mostly
reading.

## Symptoms

The gRPC channel stops answering. Both guest vCPUs peg in the kernel. sshd, the QEMU Guest Agent,
and the serial console stop responding, because no thread gets a CPU slice.

## Mechanism

A single guest write strands in the kernel.

1. A guest write to the virtio-serial port enters the FreeBSD driver. The driver takes the port
   mutex `vtcpmtx` and busy-spins in `virtqueue_poll`, waiting for the host to consume the
   buffer. `info registers` over the qemu monitor shows a vCPU stuck in `virtqueue_poll`.
2. The host chardev disconnects mid-write. qemu does not complete the in-flight descriptor, so
   the poll never returns. `info chardev` shows the port `disconnected` while the write never
   finishes. This is a known qemu limitation through qemu 11.0 (Red Hat bug 1352977).
3. A second guest thread needs `vtcpmtx` and busy-spins for it. The other vCPU sits in
   `lock_delay`. A guest memory dump shows `vtcpmtx` held by the stuck write thread.

Both vCPUs then spin in the kernel and the guest starves. The spinning write is uninterruptible,
so you cannot signal or kill it.

## Recovery

Run `qm reset <vmid>`. The hard reset recreates the qemu device and clears the wedge. A bridge
restart alone does not clear it, and neither does a guest daemon restart.

To avoid the wedge, do not restart the bridge while a deploy or any guest-to-host transfer is in
flight.

## Fix

The fix is a persistent host-side drainer. The drainer holds the chardev open and always reads
it, so the host side stays connected and guest writes complete while the bridge restarts behind
it. The bridge dials the drainer instead of the chardev.

The drainer is the fix. Change it only from a written design, never ad hoc.

## Reproduce

Reproduce the wedge on a testbed VM, never on production.

1. Start a guest-to-host transfer that keeps the guest writing, such as a `file pull` of a large
   file.
2. A few seconds later, while the transfer is mid-flight, restart or kill the host bridge. The
   chardev then disconnects under the guest's write.
3. Read the guest from the hypervisor with `info registers` over the qemu monitor. A wedge shows
   both vCPUs in the kernel (`CPL=0`, `HLT=0`), one in `virtqueue_poll` and one in `lock_delay`.
   A healthy guest shows both vCPUs halted at the idle loop.

A pull wedges far more readily than a push. Recover with `qm reset <vmid>` between attempts. The
reset reboots the guest and clears the tmpfs scratch file.

## Inspect a wedged guest

Userland is starved, so sshd, the guest agent, and the serial-console shell do not respond. Use
the kernel debugger instead.

Set `debug.kdb.break_to_debugger=1` on the guest. Run `echo nmi | qm monitor <vmid>` from the
Proxmox host. The guest enters DDB on the serial console and dumps every thread's backtrace. Read
the output over the `serial0` socket.

DDB on the stock kernel shows each thread's stack, but it cannot name the owner of a contended
lock. The stock OPNsense kernel ships without `WITNESS`. To attribute `vtcpmtx` to the thread
that holds it, build a debug kernel.

## Build a WITNESS debug kernel

A WITNESS kernel lets DDB print lock ownership with `show alllocks` and `show lockchain`. That
turns "a thread is spinning in `lock_delay`" into "this thread holds `vtcpmtx`". The stock kernel
already has `DDB`, `KDB`, and `DDB_CTF`. You add only `WITNESS` and `DEBUG_LOCKS`.

Match the source to the running kernel. OPNsense builds its kernel from `github.com/opnsense/src`
at the tag for the release. For OPNsense 26.1.2 (FreeBSD 14.3-RELEASE-p8) the tag is `26.1.1`.
Confirm the version with `sysctl kern.conftxt` and the kernel git hash it prints.

1. Free disk. The build needs 5 to 10 GB. Grow the VM disk from the Proxmox host if needed with
   `qm disk resize <vmid> scsi0 +24G`. Then on the guest, run `gpart recover da0`,
   `gpart resize -i 4 da0`, and `zpool online -e zroot da0p4`.
2. Stage the source. The guest has no internet. Clone `opnsense/src` at the tag on the host and
   copy it into the guest `/usr/src`, or clone directly if the guest can reach GitHub.
3. Write the config. In `/usr/src/sys/amd64/conf`, create a file named `SMP-WITNESS`:

   ```
   include SMP
   ident SMP-WITNESS
   options WITNESS
   options DEBUG_LOCKS
   makeoptions DEBUG=-g
   ```

4. Build the kernel with `make -j2 buildkernel KERNCONF=SMP-WITNESS`. On a 2-vCPU VM this takes
   about an hour.
5. Install beside the stock kernel, never over it:
   `make installkernel KERNCONF=SMP-WITNESS KODIR=/boot/kernel.witness`. The stock `/boot/kernel`
   stays as the fallback.
6. Boot it deliberately. The `nextboot` one-shot selector may not pick the debug kernel on this
   loader. Set `kernel="kernel.witness"` in `/boot/loader.conf.local` instead, or pick the kernel
   at the loader prompt over the `serial0` console.
7. Verify the debug kernel is live. `uname -i` prints `SMP-WITNESS`, and `sysctl debug.witness.watch`
   exists. That OID is absent on the stock kernel.
8. Capture lock ownership. Induce a wedge, enter DDB with `echo nmi | qm monitor <vmid>`, then run
   `show alllocks` and `show lockchain` to read which thread owns `vtcpmtx`.
9. Revert. Remove the `kernel="kernel.witness"` line from `/boot/loader.conf.local` and reboot to
   the stock kernel. The stock `/boot/kernel` was never modified.
