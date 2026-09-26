> Read the [repository context](../../README.md) before using copied commands or historical plans.

# The astound proof: a provider added, re-tiered and removed by inventory

This page records the proof MWAN-331 requires. Each result states the
command, the host it ran on, and what was observed. A step with no recorded
output did not happen.

Astound is an IPv4-only link. Its entry declares `link_files: rendered`, a
hardware-address match, IPv4 by DHCP at route metric 6000, an `ipv6` block with
`dhcp: false` and `accept_ra: false`, table 600, mark 4, mark priority 600,
source priority 58, and no translation prefix. The daemon renders
`20-enastound0.link` and `20-enastound0.network` from it. The simulator is LXC
903 on `suburban`; its ingress from the gateway is `veth903i0` and its uplink is
`veth903i1`. The LAN IPv4 client for every fetch below is the testbed router,
VM 201, driven with `qm guest exec 201` on `suburban`; its default route is
`10.240.240.3`, the gateway's address on `enmwanbr0`. The LAN guests on the
management network have no IPv4 and cannot serve that role.

The testbed gateway is the guest named `mwan.suburban.goodkind.io` on the
hypervisor `suburban`. Commands marked "gateway" ran there over ssh through
`suburban`; commands marked "suburban" ran on the hypervisor; commands marked
"controller" ran on the controller. Every deploy ran from main through a
detached worktree with the release pinned at `202609201600-e-16dd8f1`, and the
binary never changed. Times are PDT.

## Outcome

| Acceptance item | Result | Change |
| --- | --- | --- |
| Units rendered from the entry, no hand-written file | Passed 2026-09-20 15:10 | #462, #464 |
| IPv4 once added, traffic at the simulator ingress | Passed 15:28 | #464 |
| Failing its health removes it from steering; restoring brings it back | Passed 15:18 | #464 |
| Idle at a lower tier while the top tier is healthy | Passed 15:17 | #464 |
| Takes over when the top tiers fail | Passed 15:28 | #464 |
| Re-tiered by one edit and a deploy | Passed 15:58 | #468 |
| Removed by one edit and a deploy, state converges | Passed 16:22 | #469 |

## Add

configs #462 (251273e5) added the entry and deleted the two hand-written
templates `mwan/networkd/testbed/30-astound.link.j2` and
`30-astound.network.j2`. The first deploy from that main at 14:07 failed its
deploy gate: the entry's `ipv6` block declared `accept_ra` alone, the loader
refused the whole document with `interface enastound0: ipv6/dhcp is required`,
and `mwan-ifmgr@wan` restarted every 10 seconds into the same error. The prune
had already deleted the two old templates, the daemon wrote no replacement, and
the gateway ran with no policy rules until the fix. That outage is MWAN-506.
configs #464 (8541c585) added `dhcp: false`.

Controller, 14:58, from main 8541c585:

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff
./configsctl deploy deploy-mwan --limit mwan_suburban_servers
```

Check mode `ok=158 changed=12 failed=0`; deploy `ok=191 changed=17 failed=0`.

Gateway, after the reboot:

```bash
cat /var/run/mwan-health.state
ls /etc/systemd/network
cat /etc/systemd/network/20-enastound0.link /etc/systemd/network/20-enastound0.network
ip -br addr show enastound0
ip rule show; ip -6 rule show; ip route show table 600
```

Observed at 15:10: `astound:healthy`, `att:healthy`, `monkeybrains:healthy`,
`webpass:healthy`; twelve unit files with `20-enastound0.link` and
`20-enastound0.network` opened by the daemon's marker line. Outside comments,
`20-enastound0.network` is `DHCP=ipv4`, `IPv6AcceptRA=no`, `IPv4Forwarding=yes`
and `RouteMetric=6000`, equal to the deleted template, and the `.link` matches
`MACAddress=bc:24:11:a5:70:06` and sets `Name=enastound0`. `enastound0` had
`10.240.207.2/24 metric 6000` from the simulator's reservation. IPv4 rules
included `600: from all fwmark 0x4 lookup astound`; IPv6 had no astound rule;
table 600 was `default via 10.240.207.1 dev enastound0`.

## Health

Suburban, 15:17:12:

```bash
ip link set veth903i1 down
```

Gateway at 15:17:55: `astound:unhealthy`, the three others healthy, and rule
600 absent while rules 56, 100, 200 and 300 were unchanged.

Suburban, 15:18:01: `ip link set veth903i1 up`. Gateway at 15:18:24:
`astound:healthy` and rule 600 present again.

## Idle at a lower tier

With all four providers healthy, suburban and the router at 15:17:

```bash
timeout 15 tcpdump -ni veth903i0 -c 4 -q host 1.1.1.1 and tcp port 443
qm guest exec 201 --timeout 20 -- /usr/local/bin/curl -4 -s -o /dev/null -w "%{http_code} %{remote_ip}" --max-time 10 https://1.1.1.1
```

The fetch returned `301 1.1.1.1`. The capture ended at its timeout with
`0 packets captured`.

## Takeover

Suburban, 15:19:12:

```bash
ip link set veth900i1 down
ip link set veth901i1 down
ip link set veth902i1 down
```

Gateway: AT&T and webpass unhealthy at 15:22:48. Monkeybrains unhealthy at
about 15:28; its probe cycle takes about 110 seconds, because each of its two
HTTP probes waits out a 22-second timeout before the cycle ends. At 15:28:20
both families had `50: from all iif enmwanbr0 lookup astound`, IPv4 kept rule
600, and IPv6 kept no other provider rule.

Suburban and the router at 15:28:

```bash
timeout 20 tcpdump -ni veth903i0 -c 6 -q host 1.1.1.1 and tcp port 443
qm guest exec 201 --timeout 25 -- /usr/local/bin/curl -4 -s -o /dev/null -w "%{http_code} %{remote_ip}" --max-time 15 https://1.1.1.1
```

The router got its 301 through astound this time. The capture at 15:28:29
recorded `10.240.207.2.21766 > 1.1.1.1.443` and the reply from `1.1.1.1.443`,
the gateway's astound address as source on the hypervisor side of the
simulator, before the simulator masquerades.

Suburban, 15:28:35: the three uplinks set up. Gateway at 15:29:10: all four
healthy, and the policy rules in both families equal to the 15:16 capture line
for line.

## Re-tier

configs #468 (1068d703) changed astound's `tier` from 2 to 1 and deleted its
unread `v4_source` key. Controller, 15:36 and 15:45, from main 1068d703: check
mode and deploy reported the same task counts as the add, with `failed=0` on
both.

Gateway, after the reboot, 15:55: `mwan version` reported `commit=16dd8f1`;
`/etc/mwan/network.json` had `att` and `webpass` at tier 0, `monkeybrains` and
`astound` at tier 1; all four healthy at 15:55:36.

Suburban, 15:55:49: `veth900i1` and `veth901i1` down. Gateway at 15:58:06:
AT&T and webpass unhealthy, no rule at priority 50, IPv4 rules 300 and 600
only, and the three balancer rules in `nft list table inet mwan_steer` read
`numgen random mod 2 map { 0 : 0x03000000, 16777216 : 0x04000000 }`.

Three router fetches of `https://1.1.1.1`, `/cdn-cgi/trace` and `/dns-query`
returned 301, 200 and 400. The capture on `veth903i0` at 15:58:25 recorded
`10.240.207.2.24224 > 1.1.1.1.443`: one of the three flows was balanced onto
astound.

Suburban, 15:58:40: both uplinks up. By 15:59:16 every provider was healthy
again, the balancer rules were back on `0x01000000` and `0x02000000`, and the
rule set matched the pre-drill capture.

## Removal

configs #469 (ab0d4723) deleted the entry. `mwan_astound_iface` and
`mwan_astound_mac` stay for the simulator. Controller, 16:00 and 16:08, from
main ab0d4723: check mode `ok=159 changed=15 failed=0`; deploy `ok=192
changed=20 failed=0`.

Gateway journal, `journalctl -u mwan-ifmgr@wan -o cat`: the daemon restart at
16:17:20 logged `networkd unit files written` with `20-enastound0.link` and
`20-enastound0.network` marked `Removed`; the boot at 16:19:05 logged
`changed=null`. Ten unit files remain. Three providers healthy at 16:19:49.

Compared with the three-provider capture taken at 11:00, before the entry
existed: policy rules 0 changed lines in both families; links and addresses
0 changed lines with the astound NIC's own lines excluded; nftables ruleset 0
structural changes. The astound NIC is `ens23`, DOWN, with no unit file. At
11:00 it was `enastound0` UP with `10.240.207.2/24` under the hand-written
template that MWAN-491 deleted.
