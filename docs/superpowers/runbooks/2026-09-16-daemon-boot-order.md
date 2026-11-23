> Read the [repository context](../../README.md) before using copied commands or historical plans.

# The daemon's move before udev and networkd: cutover and failure cases

This page records gateway startup and network observations from September 2026.

The unit under test is `mwan-ifmgr@.service` with `DefaultDependencies=no`,
`After=systemd-journald.socket systemd-remount-fs.service`, and
`Before=systemd-udev-trigger.service systemd-networkd.service shutdown.target`.
The hand-authored networkd files stayed in place throughout; nothing here
renders a provider unit.

Times are PDT.

## Outcome

| Proof step | Result | Release |
| --- | --- | --- |
| Testbed cutover with reboot | Passed 2026-09-18 16:15, fourth attempt | 202609182217-9c-7cbeb2cd |
| Fallback in both families | Passed 2026-09-18 16:11 | 9c |
| Link absent at boot | Failed 16:17 (MWAN-504), passed on rerun 21:57 | 202609190338-9f-0ce2fd73 |
| AT&T 802.1X path | Not runnable on the testbed; on production the order is unchanged from the old unit (2026-09-19 09:47 boot) | 9f |
| Daemon restart with links up | Passed 2026-09-18 16:15 | 9c |
| Production check mode | Passed 2026-09-18 22:59 | main 5112babf |
| Production cutover | Two runs stopped 2026-09-18 at the networkd reload; passed 2026-09-19 09:47 | 9f, main 0d205b67 |

## Testbed cutover

Three earlier attempts found three defects in the unit, each fixed before the
next attempt:

1. 2026-09-18 00:20, release 93. `After=local-fs.target` deadlocked against
   the unit's own `Before=systemd-udev-trigger.service`: `/boot/efi` mounts by
   PARTUUID, that device unit waits for the coldplug trigger, the trigger
   waited for the daemon, and the daemon waited for `local-fs.target`. The
   boot stalled 90 seconds until the device unit timed out; `ssh.service`
   stayed inactive with no error. The ordering itself was already right
   (daemon at monotonic 95.586, trigger 95.608, networkd 95.976). Fix: drop
   `After=local-fs.target` (#422).
2. 2026-09-18 10:30, release 96. `PrivateTmp=` and `StateDirectory=` each
   order the unit after `systemd-tmpfiles-setup.service`, which waits for
   `local-fs.target`, so the same stall returned. Fix: drop both and create
   `/var/lib/mwan` with a tmpfiles rule (#425).
3. 2026-09-18 13:50, release 99. The boot completed and the ordering was
   proven (daemon 5.217, trigger finished 5.357, `local-fs.target` 6.630,
   networkd 7.885), but the daemon could not write `/var/lib/mwan` for its
   whole lifetime (113 read-only failures, one per tick), because
   `ProtectSystem=strict` fixed the mounts as they were at start and the root
   filesystem was still read-only. Fix: `After=systemd-remount-fs.service`
   (#428). The same boot showed `WantedBy=sysinit.target` in the file but
   `multi-user.target` in `systemctl show`, because the play's `enabled: true`
   does not move an existing wants symlink; the install target went back to
   `multi-user.target` (#430).

The fourth attempt is the proof.

### Step 1: capture before the cutover

Gateway, saved on the controller as `before2-*.txt`:

```bash
ip rule show; ip -6 rule show
nft list ruleset
ip -br link; ip -br addr
ls -l /etc/systemd/network
systemd-analyze critical-chain mwan-ifmgr@wan.service
```

### Step 2: prove the order from the journal

Gateway, after the reboot:

```bash
systemctl is-system-running
systemctl is-active local-fs.target basic.target multi-user.target ssh mwan-ifmgr@wan
systemctl list-jobs
systemctl show mwan-ifmgr@wan -p WantedBy -p After
journalctl -b -o short-monotonic -u mwan-ifmgr@wan -u systemd-udev-trigger -u systemd-networkd -u systemd-remount-fs
```

Observed: `running`; every listed unit `active`; no jobs; zero failed units.
`WantedBy=multi-user.target`; `After` includes `systemd-remount-fs.service`.
Monotonic: root re-mounted read-write at 5.483062, daemon started at
5.483576, udev trigger finished at 6.265155, networkd started at 7.609721.
Read-only failures in the daemon's journal: 0. `/var/run/mwan-health.state`
updated every tick.

### Step 3: prove the links and the state are unchanged

Gateway, the Step 1 captures repeated and compared on the controller with
`diff` (link lines sorted, unit-listing dates stripped): rules, firewall
ruleset, links and addresses, and the unit directory all showed 0 changed
lines. The critical chain differed, which is the point.

## Failure cases

Run in the ticket's order on 2026-09-18 with release 9c, except case 2's
rerun, which used release 9f.

### Case 1: fallback in both families

At 16:11:16, the AT&T and Webpass simulator links lost upstream connectivity.

Gateway: both providers unhealthy by 16:13:28; in both families the rules
became `iif enmwanbr0 lookup monkeybrains` at priority 50. IPv6: a `curl` from
the QA guest to `2606:4700:4700::1111` left the Monkeybrains simulator's
ingress `veth902i0` as `3d06:bad:b01:2400::217`. IPv4: a fetch of
`https://1.1.1.1` from the testbed router exited 0 and left `veth902i0` as
`10.240.206.100`. Name resolution failed for about a minute during the switch
(`curl` exit 6 at 16:13:40) and worked at 16:14:54.

At 16:15:04, both links returned. Both providers became healthy
by 16:15:21, rules identical to the pre-drill capture.

### Case 2: a link absent at boot

At 16:17, one provider link was absent during boot. The boot completed
(`running`, all targets active, SSH up),
but the gateway held no policy rule for any provider for over a minute:
`wan.routes` returned from its pass on the missing-link error before
installing anything. The gateway later reconciled the remaining providers
while the missing link stayed absent.

Gateway: udev named the link `enmbrains0`; AT&T and Webpass rules were back
by 16:18:10, Monkeybrains by 16:18:45, with no reboot.

On the 2026-09-18 21:56 rerun, the gateway booted with `running` state and
zero read-only failures. At 21:57:47:
`running`, no jobs, `enmbrains0` absent, rules for AT&T (priority 100, and
55 for inet6) and Webpass (56 and 200) present in both families, tables 100
and 200 holding their defaults, 36 "provider link missing" lines in the
daemon's journal. The link returned at 21:57:57; the gateway showed the
Monkeybrains rules (300, and 57 for inet6) and `monkeybrains:healthy` by
21:58:35, with no reboot.

### Case 3: the AT&T 802.1X path

Not runnable on the testbed. The testbed's AT&T is a direct link with no VLAN
and no 802.1X; only production has the supplicant, the path unit, and the
VLAN. AT&T leaves service about 2026-10-18.

Production showed the same sequence on the cutover boot and the three earlier
boots:

| Boot | Daemon started | networkd started | VLAN DHCPv4 lease | Supplicant AUTHENTICATED |
| --- | --- | --- | --- | --- |
| 2026-09-19 09:47, new unit | 3.64 | 5.14 | 5.53 | 9.38 |
| previous, old unit | 4.70 | 4.78 | 5.10 | 9.07 |
| two before, old unit | 4.19 | 4.39 | 4.70 | 8.76 |
| three before, old unit | 4.48 | 4.62 | 4.93 | 8.98 |

The daemon's earlier start left the sequence unchanged. The plan expected the
VLAN to take its lease only after AUTHENTICATED; on production the VLAN has
always leased about four seconds before the supplicant authenticates, with the
old unit as with the new one.

### Case 4: a daemon restart with links up

Gateway, 16:15:36:

```bash
systemctl restart mwan-ifmgr@wan
```

Links identical before and after; no kernel link events; no networkd carrier
change; 0 rename lines; 0 read-only failures.

## Production

The production observations came from the gateway and downstream clients.

### Networkd reload interruption

During two production networkd reloads at 23:03 and 23:12, networkd rewrote
all twelve files and reconfigured every link. Reconfiguring `enmwanbr0`
removed routes installed by the daemon and BGP from provider tables
(`10.250.250.0/29`, `fe::2/128`, and `3d06:bad:b01::/60 via fe::2`) and
removed policy rules. The daemon restored rules within 200 ms. Its reconcile
pass started 100 ms before route deletion. The routes returned at 23:04:38 and
23:12:39. During the gap, LAN replies followed the marked provider table's
WAN route. LAN clients lost internet for 14 to 40 seconds while gateway probes
passed.

### Production boot

At 09:47, the gateway reported a running system, active required units, no
failed units, `WantedBy=multi-user.target`, and release `9f` (`0ce2fd73`).
Monotonic timestamps showed `systemd-remount-fs.service` finished at 3.618,
the daemon started at 3.644, the udev trigger started at 3.720 and finished at
4.002, and networkd started at 4.780. The daemon reported zero read-only
failures. Three providers were healthy.

The pre-deploy and post-reboot captures showed unchanged policy rules, links,
addresses, and unit-directory contents. The firewall ruleset differed only in
pinned-address set elements, which the refresher updates.

The Cloudflare tunnels lost their connections from 09:47:31 to 09:47:42
during reboot and stayed connected during the daemon restart.
