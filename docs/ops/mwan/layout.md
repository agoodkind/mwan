> Read the [repository context](../../README.md) before using copied commands or historical plans.

# MWAN host layout

MWAN runs as one binary spread across a few hosts, and each host runs only the subcommands its role needs. Production runs on the vault hypervisor in San Francisco, and the suburban hypervisor in New Jersey runs a testbed that mirrors it, with the same roles on matching guests. The binary is built in [agoodkind/mwan](https://github.com/agoodkind/mwan), which documents its subcommands and what each one installs.

## Roles and units

- The MWAN VM is the WAN router. It runs `mwan agent` under `mwan-agent.service` and `mwan ifmgr` in the wan role under `mwan-ifmgr@wan.service`. Where the rendered configuration turns publishing on, the testbed gateway today, that ifmgr also publishes its loaded configuration into the wanconfig management datastore at startup, so the RESTCONF surface serves what the running daemon holds.
- The failover LXC is the backup BGP peer. It runs `mwan agent` under the same agent unit and `mwan ifmgr` in the failover role under `mwan-ifmgr.service`.
- The Proxmox host watches and recovers the VM from outside it. It runs `mwan ifmgr` for its own out-of-band interface and `mwan watchdog`. The testbed host additionally runs `opnsensectl host serve`, the Unix-socket bridge to the testbed OPNsense serial channel.
- The OPNsense VM runs `opnsensectl daemon serve`, the FreeBSD daemon that edits the router config over the serial channel. It has no `/etc/mwan/`; its runtime settings are in `/usr/local/etc/opnsensectl.conf`, and its supervision settings are FreeBSD `rc.conf.d` entries.

The ISP-simulator containers and the unrelated service containers on these hosts run no MWAN command.

Each host's MWAN units come from the released binary, not from this repository. After the deploy installs the binary, it runs `mwan install --role <role> --apply`, which restarts nothing, so the deploy's handlers decide the restarts. The roles are `wan` for the MWAN VM, `failover` for the failover LXC, and `host` for a Proxmox host.

## Binary rollout order

Roll a new MWAN binary onto the testbed first and production second, and verify each host before moving to the next.

1. suburban host
2. testbed MWAN VM
3. testbed failover LXC
4. testbed OPNsense
5. production failover LXC
6. production MWAN VM
7. vault host
8. production OPNsense

A production step needs a live verification and a saved rollback copy of the binary before the swap.

## WAN links

MWAN drives three wide-area networks. It load-balances outbound traffic across AT&T and Webpass and uses Monkeybrains only as a health fallback. That WAN selection is policy routing on the active router, separate from the BGP-based failover between the active router and its standby.

- `enwebpass0` carries Webpass, a Google Fiber line. It takes a dynamic carrier-grade NAT IPv4 address and a provider-delegated IPv6 prefix.
- `enatt0.3242` carries AT&T over an 802.1X-authenticated VLAN, and takes a dynamic IPv4 address and an AT&T-delegated IPv6 prefix.
- `enmbrains0` carries Monkeybrains as the lossy fallback. It takes a public static IPv4 address, a SLAAC IPv6 address, and a DHCPv6 prefix delegation, and MWAN maps its internal IPv6 range onto the first block of that delegation with network prefix translation. The delegation renumbers, so the ifmgr pd source reads the live prefix rather than a stored one (`mwan pd` shows it).

The untagged parent interface `enatt0` carries the management link to the AT&T optical network terminal and is the Layer 2 parent of the tagged `enatt0.3242`. That terminal's access and the 802.1X bring-up chain are in [ont.md](ont.md).
