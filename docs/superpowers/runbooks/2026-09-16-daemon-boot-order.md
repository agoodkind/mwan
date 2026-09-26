> Read the [repository context](../../README.md) before using copied commands or historical plans.

# The daemon's move before udev and networkd: cutover and failure cases

This page records the proof MWAN-492 requires: the testbed cutover with a
reboot, the failure exercise, the production check-mode run, and the
production cutover. Each result names the command, the host it ran on, and
what was observed. A step with no recorded output did not happen.

The unit under test is `mwan-ifmgr@.service` with `DefaultDependencies=no`,
`After=systemd-journald.socket systemd-remount-fs.service`, and
`Before=systemd-udev-trigger.service systemd-networkd.service shutdown.target`.
The hand-authored networkd files stayed in place throughout; nothing here
renders a provider unit.

The testbed gateway is the guest named `mwan.suburban.goodkind.io` on the
hypervisor `suburban`. Commands marked "gateway" ran there over ssh through
`suburban`; commands marked "suburban" ran on the hypervisor. Times are PDT.

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

### Step 2: deploy and reboot

Controller, from a detached worktree at main bbf0841f, after the testbed pin
moved to 202609182217-9c-7cbeb2cd in #430:

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers
```

The first run failed at "Create stack directories": the ssh master to the
gateway went silent 62 seconds after the nghttp2 apt task and
`ServerAliveCountMax` ended it. The gateway showed no package or service
change. The rerun completed: `ok=197 changed=21 failed=0`, release 9c
installed (commit 7cbeb2cd, binhash 6c946872e27d), and the play's own reboot
verdict and egress verdict passed.

### Step 3: prove the order from the journal

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

### Step 4: prove the links and the state are unchanged

Gateway, the Step 1 captures repeated and compared on the controller with
`diff` (link lines sorted, unit-listing dates stripped): rules, firewall
ruleset, links and addresses, and the unit directory all showed 0 changed
lines. The critical chain differed, which is the point.

## Failure cases

Run in the ticket's order on 2026-09-18 with release 9c, except case 2's
rerun, which used release 9f.

### Case 1: fallback in both families

Suburban, 16:11:16, cutting the AT&T and Webpass simulators' upstream:

```bash
ip link set veth900i1 down
ip link set veth901i1 down
```

Gateway: both providers unhealthy by 16:13:28; in both families the rules
became `iif enmwanbr0 lookup monkeybrains` at priority 50. IPv6: a `curl` from
the QA guest to `2606:4700:4700::1111` left the Monkeybrains simulator's
ingress `veth902i0` as `3d06:bad:b01:2400::217`. IPv4: a fetch of
`https://1.1.1.1` from the testbed router exited 0 and left `veth902i0` as
`10.240.206.100`. Name resolution failed for about a minute during the switch
(`curl` exit 6 at 16:13:40) and worked at 16:14:54.

Suburban, 16:15:04: both links set up again. Gateway: both providers healthy
by 16:15:21, rules identical to the pre-drill capture.

### Case 2: a link absent at boot

Suburban, 16:17, release 9c:

```bash
qm set 213 --delete net4
```

Gateway reboot. The boot completed (`running`, all targets active, ssh up),
but the gateway held no policy rule for any provider for over a minute:
`wan.routes` returned from its pass on the missing-link error before
installing anything. Filed as MWAN-504 and fixed in #432: a provider whose
link does not exist gets no rules, and the other providers are reconciled in
the same pass.

Suburban, 16:18:05, reattaching:

```bash
qm set 213 --net4 virtio=BC:24:11:3D:CE:CC,bridge=vmbr6,firewall=0
```

Gateway: udev named the link `enmbrains0`; AT&T and Webpass rules were back
by 16:18:10, Monkeybrains by 16:18:45, with no reboot.

Rerun 2026-09-18 21:56 with release 9f (testbed pin moved in #433, deploy
`ok=199 changed=26 failed=0`, boot `running`, 0 read-only failures). Suburban
deleted `net4` at 21:56:52 and the gateway rebooted. Gateway at 21:57:47:
`running`, no jobs, `enmbrains0` absent, rules for AT&T (priority 100, and
55 for inet6) and Webpass (56 and 200) present in both families, tables 100
and 200 holding their defaults, 36 "provider link missing" lines in the
daemon's journal. Suburban reattached at 21:57:57; gateway showed the
Monkeybrains rules (300, and 57 for inet6) and `monkeybrains:healthy` by
21:58:35, with no reboot.

### Case 3: the AT&T 802.1X path

Not runnable on the testbed. The testbed's AT&T is a direct link with no VLAN
and no 802.1X; only production has the supplicant, the path unit, and the
VLAN. AT&T leaves service about 2026-10-18.

Production shows the sequence on every boot. Gateway, read with `qm guest
exec` on vault, for the cutover boot and the three boots before it, which ran
the old unit:

```bash
journalctl -b <n> -o short-monotonic -u mwan-ifmgr@wan -u wpa_supplicant-mwan -u systemd-networkd -u bringup-att-vlan
```

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

The production gateway is the guest `mwan.home.goodkind.io` on the hypervisor
`vault`. The controller reaches it through the Cloudflare tunnel that
terminates on `mini`, then the router. Reads during the outage used
`qm guest exec 113` on vault.

### Check mode

Controller, 2026-09-18 22:59, from main 5112babf, after #434 moved the
production pin to 202609190338-9f-0ce2fd73 and #438 let check mode run to the
reboot step:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers --check --diff
```

`ok=180 changed=16 failed=0`, ending at "End a check run before the reboot".
Expected changes: the mwan binary and stack packages, the ifmgr unit with the
new ordering block, the `/var/lib/mwan` tmpfiles rule, the networkd directory
rewrite, header comments in three sshd and tmpfiles files, apt lists, the
install-updater script, and the deploy-gate binary on vault. One unexpected
change: `/etc/msmtprc`, whose password every run regenerates; the diff printed
it, fixed in #439.

### Cutover attempts

Controller, 23:03 and 23:12, from main 5112babf:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers
```

Both runs reached the "Reload networkd" handler and then lost the controller's
ssh path to the gateway ("Network is unreachable"), so the daemon restart and
the reboot never ran. Recap of each: `ok=187 changed=20 unreachable=1` and
`ok=186 changed=13 unreachable=1`.

Cause, from the gateway's networkd journal, the daemon's journal, and the
cloudflared journals on vault and mini. The play emptied `/etc/systemd/network`
and rewrote all twelve files, so `networkctl reload` reconfigured every link.
Reconfiguring `enmwanbr0` deleted the routes the daemon and BGP had installed
on it in the provider tables (`10.250.250.0/29`, `fe::2/128`,
`3d06:bad:b01::/60 via fe::2`) and the policy rules. The daemon re-added the
rules within 200 ms; it repairs the routes only in a reconcile pass, and both
times its pass had started 100 ms before the deletion, so the routes returned
on the 60 second tick (23:04:38 and 23:12:39). In between, replies to LAN
hosts carried the connection's fwmark, the fwmark rule selected the provider
table, and that table's only route sent them out the WAN. Every LAN host lost
internet for 14 to 40 seconds while the gateway's own probes passed. The
2026-09-17 production deploy survived the same deletion because its pass ran
200 ms after it. The deploy side is fixed in #441 (the directory is pruned by
list, so an unchanged file is never rewritten and the reload reconfigures only
changed links); the daemon side is MWAN-505.

Production after the two runs: the pre-9f daemon still running under the new
unit file, the 9f binary and the twelve networkd files on disk, no reboot,
three providers healthy, and policy rules equal to the pre-deploy capture.

### Proof of the deploy fix on the testbed

Controller, 2026-09-19 09:26, a deploy from main 0d205b67 to the testbed
gateway: `ok=196 changed=15 failed=0`. Every networkd file reported `ok`, the
prune task removed nothing, and no "Reload networkd" handler ran. Gateway,
after its reboot: the previous boot's networkd journal has no "Reloading"
and no "Reconfiguring" line, and the twelve files keep their 2026-09-18
timestamps.

### Check mode, second run

Controller, 2026-09-19 09:40, from main 0d205b67:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers --check --diff
```

`failed=0`. Changes: `/etc/mwan/mwan.env`, `/etc/mwan/config.toml` and the
sysctl file from #440, the daemon restart handler they notify, apt lists, the
deploy timestamp, and `/etc/msmtprc` with its password hidden. All ten
networkd files reported unchanged and no reload handler was queued.

### Cutover

Controller, 2026-09-19 09:40, from main 0d205b67:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers
```

`ok=212 changed=15 failed=0`; the play's reboot verdict completed, the egress
verdict passed, and every provider link held its mapped addresses. The daemon
restart handler ran at 09:46:51 and the reboot at 09:47:26.

Gateway, after the reboot, read with `qm guest exec` on vault:

```bash
systemctl is-system-running
systemctl is-active local-fs.target basic.target multi-user.target ssh mwan-ifmgr@wan systemd-networkd
systemctl list-jobs
systemctl --failed
systemctl show mwan-ifmgr@wan -p WantedBy -p ExecMainStartTimestamp
journalctl -b -o short-monotonic -u mwan-ifmgr@wan -u systemd-udev-trigger -u systemd-networkd -u systemd-remount-fs
```

Observed: `running`; every listed unit `active`; no jobs; 0 failed units;
`WantedBy=multi-user.target`; the running binary is release 9f (0ce2fd73).
Monotonic: `systemd-remount-fs.service` finished at 3.618, the daemon started
at 3.644, the udev trigger started at 3.720 and finished at 4.002, networkd
started at 4.780. Read-only failures in the daemon's journal: 0. Three
providers healthy.

The Step 1 captures repeated against the pre-deploy baseline: policy rules,
links and addresses, and the unit directory listing all 0 changed lines; the
firewall ruleset differed only in the elements of the pinned-address sets,
which the refresher timer rewrites.

Vault's and mini's cloudflared tunnels lost their connections from 09:47:31
to 09:47:42, the reboot itself, and none at the daemon restart.
