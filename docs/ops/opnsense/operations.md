> Read the [repository context](../../README.md) before using copied commands or historical plans.

# OPNsense Operational Notes

These are the non-obvious behaviors and configurations that matter when running
OPNsense as a BGP-only edge router. Static WAN gateways do not own the default
route. FRR owns the kernel default route.

If you stand up another OPNsense to replace this one, the steady-state config
section is the canonical end target. The operational rules section is the
list of foot-guns to avoid.

---

## Steady-state OPNsense config

Validated on prod (`router.home.goodkind.io`).

### Gateways (System / Gateways / Configuration)

| Gateway | Disabled | force_down | defaultgw | Notes |
|---|---|---|---|---|
| NAT64_GW | 0 | **1** | 0 | Enabled but force_down so it does NOT win default selection. Static route to `:6464::/96` still installs because the route lookup ignores `force_down`. Tayga keeps translating. |

### Static routes (System / Routes / Configuration)

| Network | Gateway | Why |
|---|---|---|
| `3d06:bad:b01:6464::/96` | NAT64_GW | NAT64 prefix delegated to Tayga for v6 to v4 translation |

Only the NAT64 static route belongs here.

### Outbound NAT (Firewall / NAT / Outbound)

Mode: **Manual** (or Hybrid; either works as long as the manual rules below exist).

Explicit manual NAT rules are required because the OPNsense auto-NAT loop only
runs when an interface gateway is configured. BGP owns the default route here,
so these manual rules provide the outbound NAT contract.

| # | Interface | Source | Translation | Static port | Purpose |
|---|---|---|---|---|---|
| 1 | WAN | INTERNAL net (alias) | Interface address | NO | All LAN-side networks NAT to WAN address |
| 2 | WAN | THIS FIREWALL | Interface address | NO | OPNsense itself (loopback + interface IPs) NAT when egressing WAN |

INTERNAL net resolves to all v4 networks of every interface in the INTERNAL
group. When you add a new VLAN to INTERNAL, the rule auto-includes it.

THIS FIREWALL (`(self)` in pf) covers OPNsense-initiated outbound for
services that bind to loopback or any LAN-side address.

Optional rule for IPsec/IKE if used: same source as #1 but dst port 500 with
`Static port` checked (auto-NAT generates this; if you do IPsec, replicate it).

### Default routes (kernel, owned by FRR/zebra)

```text
default            10.250.250.3       UG1          vtnet1   # BGP via VM 113 primary
default            3d06:bad:b01:fe::3 UG1          vtnet1   # BGP via VM 113 primary
```

`UG1` flag indicates installed by zebra (FRR). Status in `vtysh -c "show ip[v6] route 0.0.0.0/0"`
should be `Status: Installed, Selected` for the BGP entry.

### FRR / Quagga

- Plugin: `os-frr` installed
- BGP mode enabled
- Two neighbors per family:
  - `10.250.250.3` and `3d06:bad:b01:fe::3` (VM 113, primary, route-map sets local-pref 200)
  - `10.250.250.4` and `3d06:bad:b01:fe::4` (LXC 116, backup, route-map sets local-pref 100)

---

## Operational rules (foot-guns)

### Rule 1: Never enable a v4 or v6 gateway entity at top priority

Source evidence: `src/etc/inc/system.inc` lines 703-723 + 651-654.

```php
$gateway = $gateways->getDefaultGW($down_gateways, $ipproto);
if (empty($gateway['gateway'])) {
    continue;       // SKIP because no default gateway candidate
}
...
system_default_route($gateway, $routes);  // route delete + add
```

`system_routing_configure` runs `system_default_route` for each address family
**only if** `getDefaultGW` returns a non-empty gateway. `system_default_route`
unconditionally calls:

```shell
route delete -inet6 default
route add -inet6 default <new_gateway>
```

zebra on FreeBSD does not see `RTM_DELROUTE`, so its BGP-installed default
stays `Status: None` while the kernel sits with whatever OPNsense reinstalled
(or nothing). Recovery requires `service frr stop && route delete + route start`.

| Gateway state for family | system_default_route called? | BGP route survives Apply? |
|---|---|---|
| Any enabled non-down gateway exists | YES | NO (kernel default replaced) |
| All gateways for family are deleted/disabled/force_down | NO (early `continue`) | YES |

So: when no v4 or v6 gateway is selectable as default, future Apply events
skip `system_default_route` and leave BGP's kernel default untouched.

### Rule 2: Auto outbound NAT also requires a gateway

Source: `src/etc/inc/filter.inc` lines 220-238.

```php
foreach ($fw->getInterfaceMapping() as $intf => $ifcfg) {
    if (substr($ifcfg['if'], 0, 4) != 'ovpn' && !empty($ifcfg['gateway'])) {
        // generate auto outbound NAT rule
    }
}
```

When the WAN interface has no configured gateway, this condition is false and
OPNsense does not generate auto-NAT for v4 LAN to WAN traffic.

Replacement: the two manual rules in the steady-state section. They cover
the same ground regardless of gateway state.

### Rule 3: NAT64_GW disabled vs force_down

| Setting | Effect on `getDefaultGW` | Effect on static route install |
|---|---|---|
| `disabled=1` | Skipped (good, doesn't grab default) | **Skipped**. `gatewaysIndexedByName(false, ...)` excludes disabled gateways. The `:6464::/96` static route logs `gateway IP could not be found` and is NOT installed. NAT64 breaks. |
| `disabled=0` + `force_down=1` | Skipped (good) | **Installed**. `gatewaysIndexedByName` ignores `force_down`. NAT64 keeps working. |

Use `force_down`, not `disabled`, when you want a gateway to exist for
static-route reference but never be selected as default.

### Rule 4: If you must enable a gateway temporarily

Restart FRR afterward to recover the BGP default, deleting only the family whose gateway you enabled so the healthy family is not disturbed:

```shell
# Replace <family> with inet (IPv4) or inet6 (IPv6) to match the gateway you enabled.
ssh agoodkind@3d06:bad:b01::1 "sudo service frr stop && sudo route -n delete -<family> default 2>/dev/null; sudo service frr start"
```

### Rule 5: GUI Apply consequences are conditional

For most gateway/route/interface changes, the Apply triggers
`system_routing_configure`. The defaults steps run per the rules above.
The static-route rebuild always runs (delete + add for every
`<staticroutes><route>` entry) but only affects whatever specific networks
those routes cover, not the default. So it's safe assuming Rule 1 holds.

### Rule 6: Duplicate `<if>device</if>` declarations silently drop the loser

`interfaces_configure` builds `$hardware[$ifcfg['if']] = $if`, keyed by device name. Two interface entries on the same untagged device cause the second to overwrite the first in the map. Iteration order is alphabetical by `<descr>` via `strnatcmp` in `config.inc:340`. The losing interface stays in the GUI config but binds no address to any kernel interface. The testbed substitutions transform strips `opt6` so MANAGEMENT remains the only untagged interface mapped to `vtnet0`.

### Rule 7: `pkg upgrade -y` must run BEFORE `pkg install`

The OPNsense install ISO ships one snapshot of the package set. The mirror has moved on by the time you install anything. Running `pkg update -f` then jumping straight to `pkg install os-frr` pulls a libyang2 built against `pcre2-10.47` onto a system that still has `pcre2-10.45`. `vtysh` then fails at startup with `ld-elf.so.1: /usr/local/lib/libpcre2-8.so.0: version PCRE2_10.47 required by libyang2.so.2 not defined`. Insert `pkg upgrade -y` between `pkg update -f` and the first `pkg install`.

### Rule 8: Proxmox restricts `args` qemu-server field to literal `root@pam`

Setting `args`, which OpenTofu exposes as `kvm_arguments`, fails with HTTP 500
for every API token. Proxmox reports `only root can set 'args' config` regardless
of `privsep` or assigned role. Proxmox hard-codes this restriction.

Create virtual machines that need `args` manually with `qm create` as root over
SSH. This includes virtual machines with a virtio-serial chardev, such as the
MWAN OPNsense guests. Import the completed virtual machine through the repo
control tool. [OpenTofu state operations](../../../opentofu/backend.md) documents
state attachment and drift review.

### Rule 9: Hot-adding a NIC needs `configctl interface reconfigure`

`qm set <vmid> --netN ...` adds the NIC at the hypervisor level. OPNsense's kernel sees the new `vtnetN` device, but the in-OPNsense interface config does not auto-bind to it. The new device comes up `IFDISABLED` until `configctl interface reconfigure <wan|opt...>` runs on the guest. Run reconfigure for whichever OPNsense interface is supposed to bind to the new device.

---

## Recovery snippets

### BGP default got wiped on v4 + v6

```shell
ssh agoodkind@3d06:bad:b01::1 'sudo service frr stop'
ssh agoodkind@3d06:bad:b01::1 'sudo route -n delete -inet default 2>/dev/null'
ssh agoodkind@3d06:bad:b01::1 'sudo route -n delete -inet6 default 2>/dev/null'
ssh agoodkind@3d06:bad:b01::1 'sudo service frr start'
sleep 5
ssh agoodkind@3d06:bad:b01::1 'sudo netstat -rn | grep ^default'
```

Should show BGP defaults restored (`UG1` flag).

### NAT64 stops working

Check (from any v6-capable host on LAN):

```shell
dig @3d06:bad:b01::64 +short AAAA ipv4.google.com
ping6 <synthesized addr from above>
```

If DNS64 returns synth but ping fails, check the `:6464::/96` static route
on OPNsense (`netstat -rn -f inet6 | grep 6464`). If missing, check that
NAT64_GW is `disabled=0` and `force_down=1` (not merely `disabled=0`, or it
becomes a default-route candidate) and that the static route is `disabled=0`.

### Outbound NAT stops working (LAN clients lose v4 Internet but v6 works)

Most likely cause: the manual NAT rules are missing. Check Firewall / NAT /
Outbound for the two manual rules. Verify with `pfctl -sn | grep ^nat` from
OPNsense shell; should see at least one `nat on vtnet1 inet from <internal_net>
to any -> (vtnet1:0)` rule.

---

## Snapshots without saved RAM

Take every OPNsense snapshot without saved RAM, on production and testbed alike. A snapshot that saves RAM resumes on rollback with a stale wall clock, dead TCP sockets, and a stale resolver cache, which wastes hours chasing a failure that is really stale state. The web GUI defaults saved RAM on for a running VM, so do not take these snapshots from the GUI.

After a rollback, confirm the guest agent and serial console respond, SSH and the web UI answer, DNS resolves, and the default routes, firewall rules, and BGP state are sane before trusting the router again.
