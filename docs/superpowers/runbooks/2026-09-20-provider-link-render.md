> Read the [repository context](../../README.md) before using copied commands or historical plans.

# The daemon renders the provider networkd files: testbed cutover

This page records the proof MWAN-491 requires: the testbed cutover to a release
where the daemon writes each rendered provider's systemd-networkd files, the
comparison of those files against the ones the deploy wrote before, and the
boot ordering with the renderer in place. Each result states the command, the
host it ran on, and what was observed. A step with no recorded output did not
happen.

The daemon writes one `.link` and one `.network` per rendered provider from its
entry in `/etc/mwan/network.json`, names each file after the interface with one
fixed numeric prefix, and opens each file with a marker line. AT&T keeps four
hand-authored files. Astound kept two on the testbed until its provider entry
was added later the same day; that proof is on
[the fourth-provider page](2026-09-20-fourth-provider.md).

The testbed gateway is the guest named `mwan.suburban.goodkind.io` on the
hypervisor `suburban`. Commands marked "gateway" ran there over ssh through
`suburban`; commands marked "controller" ran on the controller. Times are PDT.

## Outcome

| Proof step | Result | Release |
| --- | --- | --- |
| Testbed cutover with reboot | Passed 2026-09-20 09:56 | 202609201600-e-16dd8f1 |
| Rendered files against the files they replaced | Passed 2026-09-20 11:35, eleven of twelve pairs identical | e |
| Boot ordering with the renderer | Passed 2026-09-20 11:20 | e |
| Deploy prune against the daemon's files | Failed check mode 2026-09-20 10:05, fixed in #457 | main 429a6f62, fix 10846744 |
| Fallback in both families | Passed 2026-09-20 10:54 | e |
| Daemon restart with links up | Passed 2026-09-20 10:50 | e |
| Link absent at boot | Passed 2026-09-20 16:56 | e |
| AT&T 802.1X path | Not runnable on the testbed | |
| Production check mode | Passed 2026-09-20 12:34 | main bfb3fa02 |
| Production cutover | Passed 2026-09-20 12:56 | main bfb3fa02, release e |

## Testbed cutover

The testbed pins release `202609201600-e-16dd8f1`, which contains the renderer
at agoodkind/mwan 6c3fc6e2. Configs main carried the matching inventory: #455
(ad29e3c0) removed the webpass `v4_source` line and the six webpass and
monkeybrains templates, and #456 (2e96a7d8) moved the pin.

Gateway, after the deploy and its reboot:

```bash
mwan version
ls -l /etc/systemd/network
cat /var/run/mwan-health.state
```

Observed: `commit=16dd8f1`; twelve files, four of them written by the daemon at
09:54 under the names `20-enwebpass0.link`, `20-enwebpass0.network`,
`20-enmbrains0.link` and `20-enmbrains0.network`; three providers healthy.

The state comparison against the capture taken at release `085fc2b`, before the
cutover, while the deploy still wrote those files:

```bash
ip rule show; ip -6 rule show
ip -br link; ip -br addr
nft list ruleset
```

Observed: policy rules 0 changed lines in both families, links and addresses 0,
firewall ruleset 0.

## The rendered files against the files they replaced

This comparison is the one place the inventory, the template, the loader and
the renderer are all real at once. Both captures are the full text of every
file in `/etc/systemd/network`, the first at release `085fc2b` and the second
at release `e`:

```bash
for f in /etc/systemd/network/*; do echo "== $f"; cat "$f"; done
```

The two sides pair by their `[Match]` block and extension rather than by file
name, because the daemon uses the interface name where the templates used the
provider name. Comment lines and blank lines are stripped from both sides, and
the keys within a section are compared as a set.

Eleven of the twelve pairs are identical:

| Before | After |
| --- | --- |
| `20-webpass.link` | `20-enwebpass0.link` |
| `20-webpass.network` | `20-enwebpass0.network` |
| `30-monkeybrains.link` | `20-enmbrains0.link` |
| `20-att.link`, `20-att.network` | unchanged, the daemon writes neither |
| `30-astound.link`, `30-astound.network` | unchanged, still hand-authored |
| `10-mgmt.link`, `10-mgmt.network` | unchanged, the deploy writes both |
| `40-mwanbr.link`, `40-mwanbr.network` | unchanged, the deploy writes both |

The twelfth pair differs by one key. `30-monkeybrains.network` set `[DHCPv6]
DUIDRawData=` to an empty value, and `20-enmbrains0.network` omits the key: the
testbed sets `mwan_monkeybrains_duid_raw_data` to an empty string, and the
model's DUID pattern rejects an empty value. systemd-networkd applies the same
default for an empty assignment and for an absent key.

## Boot ordering with the renderer

Read the ordering from systemd's own bookkeeping on this guest. The journal's
raw `__MONOTONIC_TIMESTAMP` field disagrees with those timestamps by a
non-constant amount here: `systemd-udev-trigger` reads 16.794 in the raw field
against 9.703 from `systemctl show`, and `systemd-networkd` reads 16.867
against 12.452. `journalctl -o short-monotonic` agrees with `systemctl show`.

Gateway, boot `0da6f387`:

```bash
systemctl show mwan-ifmgr@wan.service systemd-udev-trigger.service systemd-networkd.service -p Id -p ExecMainStartTimestampMonotonic -p ActiveEnterTimestampMonotonic
```

Observed, monotonic microseconds:

| Unit | ExecMainStart | ActiveEnter |
| --- | --- | --- |
| `mwan-ifmgr@wan.service` | 9515267 | 9521218 |
| `systemd-udev-trigger.service` | 9699463 | 10196892 |
| `systemd-networkd.service` | 12450690 | 14418571 |

`systemd-analyze critical-chain mwan-ifmgr@wan.service` reports the daemon at
`@3.470s` after `systemd-remount-fs.service`. The gateway reports `running`
with 0 failed units.

The daemon rewrote nothing at that boot. Its log line reads:

```
msg="ifmgr: networkd unit files written" dir=/etc/systemd/network changed=null
```

The four rendered files already matched what the renderer produces. The daemon
therefore issued no networkd reload. Those files carry mtime 09:54, written by
the daemon restart the deploy triggered, before the reboot. This boot proves
the ordering and proves that a second pass over unchanged content rewrites
nothing. It leaves one property unproven: that a fresh write finishes before
the udev trigger reads a `.link` file. A boot with that directory empty would
prove it.

## The deploy prune against the daemon's files

Deleting a daemon-written file removes addressing from that provider link until
the daemon writes the file again, and the reload the prune notifies applies
that gap immediately. `deploy-mwan` prunes `/etc/systemd/network` by its own
list, and the shortened list omits the four files the daemon writes.

Controller, 2026-09-20 10:05, from main 429a6f62:

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff
```

Observed: `failed=0`, and the prune task reported deleting
`20-enwebpass0.link`, `20-enwebpass0.network`, `20-enmbrains0.link` and
`20-enmbrains0.network`.

The fix merged as 10846744 (#457). A second `find` task lists every file in
that directory containing the daemon's marker line, and the prune loop skips
those paths. Controller, from that branch:

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff
```

Observed: `ok=159 changed=19 failed=0`, and the prune task skipped all twelve
files.

## Failure cases

MWAN-492 ran these against a gateway where the deploy wrote the provider files.
They run again here because a different writer owns those files.

### A daemon restart with links up

Gateway, 10:50:48:

```bash
systemctl restart mwan-ifmgr@wan
```

The daemon logged `ifmgr: starting` at 10:50:49.141, `ifmgr: networkd unit
files written` with `changed=null` at .178, and `ifmgr: Daemon ready` with
`module_count` 4 at .216. Zero rename lines and zero reload lines. Every link
kept its name and the four rendered files kept their 09:54 mtimes.

Health passed through `att:unknown`, `monkeybrains:unknown` and
`webpass:unknown`, which is the startup state MWAN-329 requires, and returned
to all three healthy about 37 seconds after the restart.

### Fallback in both families

Suburban, 10:52:08, cutting the AT&T and webpass simulators' upstream:

```bash
ip link set veth900i1 down
ip link set veth901i1 down
```

Gateway at 10:54:22: `att:unhealthy`, `webpass:unhealthy`,
`monkeybrains:healthy`. In both families the LAN rule became `50: from all iif
enmwanbr0 lookup monkeybrains`, and IPv6 kept `57: from 3d06:bad:b01:2400::/60
lookup monkeybrains`.

Suburban, 10:54:50: both links set up again. The gateway returned all three
providers to healthy, and its 15 policy rules matched the pre-drill capture in
both families with 0 changed lines. The four rendered files kept their 09:54
mtimes throughout, so a health transition rewrites no unit file.

A `curl` from the gateway itself proves nothing about this drill and returned
`000` in both families during it. The rule the drill moves selects traffic
arriving on the internal bridge, and the gateway's own sockets use the main
table instead. Observe LAN fallback from a LAN host, as the 2026-09-18 drill
did from the QA guest and the testbed router at the simulator ingress
`veth902i0`.

### A link absent at boot

Run at 16:54 after the astound proof, with three providers in inventory.
Suburban, 16:54:27, detaching the monkeybrains NIC:

```bash
qm set 213 --delete net4
```

Gateway, 16:54:34: `systemctl reboot`.

Gateway at 16:55:18:

```bash
systemctl is-system-running
systemctl is-active mwan-ifmgr@wan
systemctl list-jobs
systemctl --failed
cat /var/run/mwan-health.state
ip -br link
ip rule show; ip -6 rule show
journalctl -b -u mwan-ifmgr@wan -o cat
```

Observed: `running`, `active`, no jobs, 0 failed units; `att:healthy`,
`monkeybrains:unknown`, `webpass:healthy`; `enmbrains0` absent; IPv4 rules 56,
100 and 200 and IPv6 rules 55, 56, 100 and 200. The daemon logged
`networkd unit files written` with `changed=null` at 16:54:49, `Daemon ready`
with `module_count` 4, and then `wan.routes: provider link missing; treating
it as having no gateway` for monkeybrains in both families.

Suburban, 16:55:38, reattaching:

```bash
qm set 213 --net4 virtio=BC:24:11:3D:CE:CC,bridge=vmbr6,firewall=0
```

Gateway at 16:56:16, `uptime -p` reporting 1 minute and no reboot: udev named
the device `enmbrains0` from the daemon-written `20-enmbrains0.link`; the link
leased `10.240.206.100/24` and its IPv6 addresses; `monkeybrains:healthy`;
rules 300 and 57 present again and the full rule set equal to the pre-detach
capture in both families.

## Production

### Check mode

Controller, 2026-09-20 12:34, from main bfb3fa02:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers --check --diff
```

`ok=175 changed=18 failed=0`. The change list matched the testbed's from
09:00. The prune reported deleting `20-webpass.link`, `20-webpass.network`,
`30-monkeybrains.link` and `30-monkeybrains.network`. Release 085fc2b writes
those four templates without a marker line, and the other eight files in
`/etc/systemd/network` are `10-mgmt`, `40-mwanbr`, and AT&T's four, which the
shortened `mwan_networkd_files` still names.

### Cutover

Controller, 2026-09-20 12:56, from main bfb3fa02:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers
```

`ok=211 changed=27 failed=0`. `Reload networkd` completed before
`Restart mwan-ifmgr@wan`; both handlers succeeded. The VM rebooted after both.

Gateway 113 after the reboot, read with `qm guest exec` on vault:

```bash
uptime -p
systemctl is-system-running
systemctl is-active mwan-ifmgr@wan
mwan version
cat /var/run/mwan-health.state
ls /etc/systemd/network
```

`up 0 minutes`, `running`, `active`, `commit=16dd8f1`, all three providers
healthy, twelve networkd files. AT&T's `20-att.link`, `20-att.network`,
`21-att-vlan.netdev` and `21-att-vlan.network` are the deploy's templates,
unchanged. `20-enwebpass0.link`, `20-enwebpass0.network`, `20-enmbrains0.link`
and `20-enmbrains0.network` are the daemon's renders.

### State compared with the pre-deploy capture

```bash
ip rule show; ip -6 rule show
ip -br link; ip -br addr
nft list ruleset
```

Policy rules: 0 changed lines in both families. Links and addresses: 0
changed lines. The nftables ruleset structure is unchanged; only the
pinned-address set elements differ because the refresher timer rewrites them
every six hours.

## Not runnable

The AT&T 802.1X path is not runnable on the testbed. The testbed's AT&T is a
direct link with no VLAN and no supplicant, and MWAN-492 settled that case
from production boots.
