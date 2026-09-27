> Read the [repository context](../../README.md) before using copied commands or historical plans.

# MWAN data plane

The `mwan-ifmgr@wan` unit maintains gateway packet policy independently of
the BGP speaker. Its firewall module installs input, forwarding, IPv4
translation, and marking rules. The `npt` module owns `table ip6 nat` and the
IPv6 prefix translator. The `wan.routes` module owns `ip rule` and the per-WAN
routing tables. The `health` module produces WAN health verdicts. The
`steering` module assigns new connections to eligible providers. A health
transition requests an immediate reconcile that updates routing before the
next periodic tick.

## Shared per-WAN foundation

`wan.routes` and `npt` read one WAN list from the gateway's network
configuration. The daemon reads the local firewall settings first and installs
protective rules before it validates the full document or writes network
files. Each WAN entry identifies its interface, provider, and policy-routing
settings. The internal prefix and edge addresses apply to all WAN entries.

## wan.routes ifmgr module

The `wan.routes` module owns policy routing. It watches each WAN interface
over netlink and reconciles the per-WAN tables and the `ip rule` set on every
default-route change plus a periodic tick, so it does not miss a late
RA-learned default route. It owns priorities 50/55/56/57/100/200/300. Its
fwmark rules share a priority with the networkd from-edge rules without
thrash.

## npt ifmgr module

The `npt` module creates and maintains `table ip6 nat`. It runs after the
firewall module. The `npt` module reads the installed IPv4 firewall rules to
determine IPv4 translation readiness. It self-disables when the network
configuration lists no WANs.

For delegated NPT, the module derives each configured external prefix from
the live DHCPv6-PD delegation on that WAN's interface. It does not substitute
a static prefix when a delegation is missing. It skips that WAN for the
current reconcile.

The module alerts when a provider configured for delegated translation loses
its delegation. It clears the alert when the delegation returns. The module
does not raise this alert for a provider without delegated translation.

The module creates the IPv6 NAT table and its `prerouting` and `postrouting`
chains. It replaces both chains' rules in one kernel transaction. Its nftables
watcher requests reconciliation when the table or either chain is deleted.
The firewall and steering modules repair their own deleted structures.

Stopping `mwan-ifmgr@wan` preserves its installed rules in the kernel. A
restart reapplies the policy. A deleted table or chain does not enforce its
policy until the next successful reconcile.

## Health state and the email guard

The `health` module writes one state file, `/var/run/mwan-health.state`. The
`wan.routes` and `steering` modules read it on every reconcile, and every verdict
change rewrites it atomically.

Every daemon start puts each configured provider at `unknown` and probes it from
scratch, so a verdict rests on the probes of the run that reports it. No file
records a verdict across a restart.

The warmup state decides the first email after a restart. A provider that comes
back healthy transitions `unknown -> healthy`, which has no earlier alert to
resolve and sends nothing. A provider that is broken at startup transitions
`unknown -> unhealthy` and raises its alert immediately, rather than staying
silent until it has been healthy once.

Failure modes worth knowing:

- A missing or empty `table ip6 nat` indicates that IPv6 edge-rule
  programming did not complete or the rules were deleted. Check the
  `mwan-ifmgr@wan` logs for the NPT reconcile result. The module creates
  missing structures on its next reconcile.
- PCI and virtio devices can appear after the daemon starts. AT&T 802.1X
  authentication can also finish later. The daemon re-evaluates those
  interfaces on interface events and periodic ticks.

For terminology, prefer **healthy / unhealthy / unknown** for WAN state. Avoid
**up / down** for health, because that conflicts with `ip link` administrative
state.

## Tracing

The ifmgr daemon emits structured JSON logs to `/var/log/mwan-ifmgr.jsonl`,
and each line carries the active `traceId` so events across a deploy or a
boot correlate. `update-att-pinned-dests.sh` still writes shell-side JSON to
`/var/log/mwan-debug.log` when `mwan_debug_logging: true`.

Trace ID sources:

- `mwan-trace-boot.service` writes `/run/mwan-trace-id` and
  `/var/lib/mwan/trace-id` at boot.
- The deploy playbook writes the same files at the start of deploy.

Quick check on MWAN:

```bash
cat /run/mwan-trace-id
mwan debug trace-tail
```

## Inspect the data plane

The gateway service reports readiness after it installs and inspects
protective rules and writes required network files. It does not wait for
working ISP links. The gateway `nftables.service` is masked after the
ownership cutover. Its stopped state does not indicate a firewall failure.

Inspect the current kernel policy on MWAN:

```bash
ssh root@mwan.home.goodkind.io
wpa_cli status
systemctl status wpa_supplicant-mwan systemd-networkd networkd-dispatcher \
  mwan-ifmgr@wan cloudflared
mwan deploy-gate inspect-firewall /etc/mwan/network.json /usr/local/share/wanconfig/yang
mwan debug npt
mwan debug connectivity
mwan debug systemd
mwan debug trace-tail
```

The inspection command compares the configured firewall policy with the
current network namespace. It makes no kernel changes and exits unsuccessfully
when rules or chain properties differ. Use `mwan debug npt` to inspect the
separate IPv6 NAT rules.

IPv6 sanity checks:

```bash
ip -6 route show table 100
ip -6 route show table 200
ip -6 rule show
nft -a list chain ip6 nat postrouting
nft -a list chain ip6 nat prerouting
```
