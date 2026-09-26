> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Suburban MWAN testbed

Suburban is the NJ Proxmox hypervisor. The testbed mirrors production MWAN
using the same Ansible templates with testbed group vars.

## Bridges

Each bridge carries one role:

- The Comcast uplink bridge is suburban's own internet path and the outbound
  NAT boundary. Nothing testbed-internal attaches to it.
- The VM management bridge is the uplink behind suburban: the ISP sims default
  through it, so replies to tunnel and testbed sources drain to suburban, and
  their internet traffic masquerades onward to Comcast.
- The MWAN internal bridge is the OPNsense transit link. Suburban holds no
  address on it, matching vault, so it reaches the guests through the router
  rather than across the bridge.
- The VLAN-aware trunk carries VMNET, the guest segment every testbed guest
  and the router share, plus the tagged VLANs mirroring production.
- Each simulated ISP has its own bare-L2 bridge terminating one WAN link for
  the MWAN VM; the Monkeybrains one also carries the failover's upstream NIC.

## Guests

OpenTofu provisions every suburban guest, and Ansible configures what runs
inside it.

A guest that mirrors a production service takes its counterpart's VMID plus 100,
and the simulated ISPs, which have no production counterpart, sit in the 9xx
range. No id is shared between the two hypervisors.

That separation is what makes a misdirected command safe. The hypervisors are
independent Proxmox installations rather than a cluster, so a shared id is legal,
but a command that lands on the wrong host then finds a guest at that id and
succeeds against it. With no id in common the same mistake fails outright.

The [testbed OPNsense recovery guide](../opnsense/testbed/access.md) uses the
serial channel when network access is unavailable.

Each ISP simulator terminates one WAN link for the MWAN VM and uplinks through
suburban, which returns tunnel and testbed traffic and masquerades internet
traffic out to Comcast. The three IPv6-capable sims also serve DHCPv6-PD
through kea-dhcp6 and router advertisements through radvd; astound is
IPv4-only and runs neither. Each sim declares capability flags and downstream
subnets in the suburban group vars, with a comment beside each flag
explaining it.

The four sims differ because the real WANs do. Monkeybrains runs the full
dynamic stack, so the MWAN VM
receives a DHCPv4 lease, a DHCPv6 address, a delegated prefix, and a SLAAC
address exactly as the real Monkeybrains delivers. AT&T offers a dynamic
DHCPv4 link pinned stable by a MAC reservation, over which the sim routes a
static block that the MWAN VM translates one-to-one to its internal services;
the testbed cannot reproduce 802.1X or the VLAN, so that link is a plain NIC.
Webpass offers a static link that sits inside its own static block, as
production Webpass does, so the MWAN VM holds its mapped addresses on that
link and the sim routes no block. Astound offers a dynamic DHCPv4 link pinned
stable by a MAC reservation and carries no IPv6 at all: no delegated prefix,
no routed static block, and no SLAAC address, matching the real Astound
circuit. It proves that a plain IPv4-only link can be added this way, nothing
more. The gateway does not yet steer traffic through it: the link comes up so
a provider can be added, re-tiered, and removed against real hardware.

Prefix delegation sizes match production, and NPT translates the first `/60`
of each delegation. The delegated prefixes deliberately avoid the `02xx` space
that management, LAN, internal, and SLAAC already use. Astound delegates no
prefix, so NPT has nothing to translate for it.

## Production vs testbed

Every deploy playbook serves both environments; the `--limit` group selects
which one, and the matching group vars carry each side's values.

| Component      | Production (vault)             | Testbed (suburban)                 |
| -------------- | ------------------------------ | ---------------------------------- |
| MWAN VM        | mwan.home.goodkind.io          | mwan.suburban.goodkind.io          |
| Failover LXC   | mwan-failover.home.goodkind.io | mwan-failover.suburban.goodkind.io |
| OPNsense       | router.home.goodkind.io        | router.suburban.goodkind.io        |
| Hypervisor     | vault                          | suburban                           |
| MWAN limit     | `mwan_servers`                 | `mwan_suburban_servers`            |
| Failover limit | `mwan_failover_servers`        | `mwan_failover_suburban_servers`   |
| OPNsense limit | `opnsense_servers`             | `opnsense_suburban_servers`        |
| Extras limit   | n/a                            | `suburban` (deploy-testbed)        |

## Testbed-only infrastructure

The ISP sim LXCs, suburban-side safe IPv6 sysctl defaults, and suburban
masquerade rules only exist on the testbed. The bridge shape stays in
OpenTofu, the testbed deploy ships the early-boot sysctl defaults, and
`mwan-ifmgr` reconciles the live per-bridge Router Advertisement policy
continuously from the suburban host config.

## Suburban gotchas

These are suburban-specific facts that differ from vault and cost time when
unknown.

- **Cloud-init drive storage.** `local-lvm` is disabled on suburban; only
  `local-zfs` is active. Guest `initialization.datastore_id` must be `local-zfs`,
  or a cloud-init drive regen fails with `storage 'local-lvm' is not available`.
- **`args` ownership.** VMs with a virtio-serial or vsock device (the MWAN VM and the OPNsense guest)
  carry their `args` set by Ansible as `root@pam`, because the Proxmox API rejects
  `args` writes from a token. OpenTofu must not manage `kvm_arguments` for those
  VMs (`lifecycle.ignore_changes = [kvm_arguments]`), or a plan tries to null the
  field and the apply fails with `VM is locked`. See
  [OPNsense operations](../opnsense/operations.md) Rule 8.
- **Management return path.** The MWAN VM's management interface has no policy
  route, mirroring prod, so on-link replies to peers on the shared guest
  segment return directly. A management policy table carrying only a default
  route shadows the connected route and triangles on-link replies through the
  gateway, which breaks reachability.
- **Reachability probing.** ICMP echo to testbed guests works from the
  workstation. A probe flow survives only when its reply returns the way the
  request came. The suburban cloudflared connector therefore sources proxied
  pings from its own address on the guest segment, so replies come back
  on-link. A probe sourced from an off-link address reads a healthy guest as
  down, because the reply crosses the testbed router stateless and its
  default deny drops it.
- **Watchdog host config address.** The suburban watchdog must target the
  MWAN VM's current management address. A stale address degrades its VM
  health probe to the TCP and PVE fallback channels (the vsock channel still
  works because it is CID-based), and a wedged snapshot plus a tight retry
  loop can hold the VM lock. Redeploying the suburban Proxmox host converges
  the config.
