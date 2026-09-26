> Read the [repository context](../../README.md) before using copied commands or historical plans.

# OPNsense out-of-band daemon

`mwan-opnsense` is the break-glass control channel into the production OPNsense router, and it reaches the router over a serial line with no dependency on the network stack. It exists for the one moment when nothing else works, so it must work every time. This page covers why it is built the way it is, the invariants that must not change, and how to operate it. The code is the source of truth for what it does; this page records the durable reasons so a change is never made blind.

## Why it exists

The daemon gives you gRPC access to OPNsense over a serial channel, so you can reach the router when its network stack is down. SSH cannot fill that role, because it breaks across firmware upgrades and depends on the very network that may be down. A package manager cannot either, so the daemon ships as one self-contained binary dropped onto the guest from the Proxmox host. Judge its blast radius by the failure it exists for: in an outage every other path is gone and this serial daemon is the only way in.

## Topology

Three processes connect you to the OPNsense config. A probe, the `opnsensectl` binary, dials a local Unix socket on the Proxmox host. The host bridge, `mwan-opnsense-host`, a systemd service that always restarts and runs `opnsensectl host serve`, forwards that connection over a multiplexed session across the qemu virtio-serial channel. The daemon, the FreeBSD `opnsensectl` binary installed as `mwan-opnsense`, serves gRPC over that session, reading and writing `/conf/config.xml` and running guest commands.

## The serial transport and its invariants

The transport is qemu virtio-serial, because FreeBSD has no host support for virtual sockets. The device is a FreeBSD tty, which fights binary traffic in several ways, so the code holds a set of invariants that each prevent a wedge the bring-up fought for days. Do not change them without reading this section.

- The persistent file descriptor never closes while the daemon lives. Its close call is a deliberate no-op so the descriptor outlives every serial session, and the serve loop rebuilds the multiplexing and gRPC layers over the same channel when a session ends. The descriptor closes only when the daemon exits.
- The device opens in raw mode with software flow control off. Arbitrary binary bytes contain the flow-control control codes, so leaving flow control on lets the tty eat payload and hang a transfer.
- Each write is capped at 8 KiB. The FreeBSD tty input queue is sized from the baud rate, and a single write larger than the queue silently loses its tail.
- The transfer paces with stop-and-wait acknowledgements, one chunk outstanding at a time, so the virtio receive queue cannot overflow.
- The daemon flushes the tty queue on open, because the driver does not flush on close and leftover bytes from a prior session would corrupt the next handshake.
- The daemon binds the named virtio port, never the raw device. The guest agent shares the raw device numbering, so binding the raw path lets the two collide and steal each other's port, and the named port also survives slot renumbering across reboots.

### What not to touch

These are the load-bearing invariants, and changing any of them reopens a documented wedge: the no-op close and the never-close-while-alive rule, the raw-mode termios with flow control cleared, the 8 KiB write cap and the ack pacing, the multiplexer tuning with keepalive off, the named virtio port, and the flush-on-open with the session-rebuild loop.

## Lifecycle and self-deploy

The daemon starts from rc.d, serves until its context is cancelled, then exits. A stop or self-deploy succeeds only on the round-trip: the lever comes back up and serves the intended binary, and a bad binary auto-reverts. Exiting cleanly and staying down is a failure for a break-glass lever.

On self-deploy you push a new binary over the transfer service, stage it as an atomic swap of the active and previous slots plus a pending-verify marker, then ask the daemon to restart. The daemon re-executes itself in place onto the new binary and keeps its pid, and you mark it healthy once it answers. A new binary that keeps crashing before it is marked healthy is reverted to the previous slot by the rc.d preflight on a respawn, so a bad self-deploy heals itself. Config writes are atomic, so a hard exit never leaves a half-written config. On a clean stop the daemon closes the serial descriptor to unblock its read loop, bounds the graceful stop, and forces exit as a last resort, and it kills its process group so a wedged child can never orphan.

## Session recovery after a host-side disruption

When the host bridge or the chardev drainer restarts or dies mid-transfer, the guest keeps its serial descriptor and its old session open, because the byte stream never breaks. The host re-dials and opens a fresh session over the same stream, the guest's old session reads the new framing as a collision and ends, and the serve loop rebuilds. Recovery is driven by that collision, not by a timer, and completes in under five seconds. This is why the serial stream carries no liveness frames and the multiplexer keepalive stays off: recovery does not depend on a heartbeat, which is only a backstop.

To inspect the guest's serial reads and writes at the syscall level, run dtrace on the OPNsense guest with the no-libraries flag, because the bundled dtrace libraries fail to compile on this kernel and the default invocation aborts.

## Operate it

The exact command flags drift, so this section names the verbs and describes behavior. Run `opnsensectl <verb> --help` for the current flags, which are the source of truth.

### Pick the right channel

The daemon is one of three out-of-band channels into OPNsense, and each fits a different job. Prefer the gRPC-over-serial daemon for short control-plane calls such as `version`, `state`, config reads, and the upgrade validate checks, because it survives a packet-filter or routing break that would silently fail the guest agent. Use the QEMU guest agent for read-only probes. Use SSH over the privileged admin path for large file pushes and long command runs, which it carries better than the serial daemon. Fall back to the serial console as the kernel-level last resort, since it is the only signal that survives a kernel panic, a botched bootloader, or lost network state.

### Update the binary

The OPNsense deploy installs the FreeBSD binary of a published opnsensectl release and restarts the daemon onto it with no manual step. It reads the daemon through `opnsensectl` on the Proxmox host, so the Proxmox deploy installs the same opnsensectl release there first. Before it decides, the deploy reads the running daemon's version once from the Proxmox host, because an earlier install can replace the binary on disk under a daemon that never restarted. The deploy copies the release beside the active binary rather than over it. When the release differs from the active binary, when anything other than exactly one instance named by the rc.d pidfile runs, or when the running daemon does not report the released commit or its version cannot be read, the deploy stops every instance it finds by command line, including a supervisor the pidfile no longer names, escalating from TERM to KILL. With no instance running, it runs the release's `install` to write the rc.d script and run shim that match it, copies the release to the previous slot, moves it into the active slot, and starts one instance through the rc.d script. The binary, the rc.d script, and the shim change together while the daemon is stopped, because a binary started by another release's rc.d script and shim exits and the supervisor restarts it forever. It never uses the restart call, because the in-place re-exec behind that call once left the daemon answering nothing for seven minutes. The deploy then waits for the version call from the Proxmox host to report the released commit, runs `/bin/hostname` over the exec call, confirms that exactly one instance runs, and marks the binary healthy. A failed check fails the deploy and leaves the new binary running, because the deploy arms no automatic revert. Every rc.d start, including one through `service`, gives the daemon the boot PATH.

To update the daemon while the network is down, use the serial channel it serves. Push the new binary to the staging slot, stage it by its hash to swap it into the active slot while keeping the previous binary as the rollback slot, restart onto it, verify with the version and state calls, and then run `opnsensectl daemon mark-healthy`. `opnsensectl daemon revert` reverts by hand.

### Upgrade the firmware

The `opnsensectl upgrade` verb drives a firmware upgrade as a state machine, so each phase records an artifact and refuses to run out of order. The phases run in order: prepare takes a snapshot and captures the pre-upgrade state including the installed firmware versions, execute applies the pending firmware update the way the OPNsense web interface does and reboots only when OPNsense requires it (a target in another release series runs a major upgrade, which always reboots), validate passes only when both the IPv4 and IPv6 targets answer a ping from the Proxmox host within a minute, the daemon answers its version call, and the router runs `/bin/hostname` over the exec call, and commit or rollback either deletes the snapshot and locks the cycle or restores it and re-validates. Run the phases by hand the first time on a target so you see each artifact land, then use `opnsensectl upgrade run` to chain them with auto-rollback on a hard failure.

Prefer the gRPC transport for the upgrade so the control path survives a routing break during the upgrade. The egress checks ping from the Proxmox host, so they prove the router's egress only on a host whose default routes pass through that router. During the execute window, which runs ten to thirty minutes, watch the upgrade log the daemon streams, the OPNsense system log over SSH which drops during the reboot, and the serial console which is the only signal that survives a panic.

### Recover a wedged channel

The daemon can wedge under a heavy command or a large input payload. Recover in order of least disruption. First retry the wedging operation over SSH or the serial console, which do not share the daemon's state. Then reset the guest, which recovers the guest agent and usually the daemon. If the daemon stays down, reach the Proxmox host over its out-of-band tunnel and restart the service there. For a guest that will not return over any network path, attach the serial console and recover at the kernel level. After a rollback the guest reboots and the daemon starts with it, and it is reachable as soon as the serial socket is up.

To install OPNsense and the daemon on a fresh VM, read [install](install.md). For the wedge mechanism in depth, its trigger, evidenced cause, and how to inspect a wedged guest with a kernel-debug build, read [wedge](wedge.md).
