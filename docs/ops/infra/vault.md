> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Vault hypervisor

Vault is the production Proxmox hypervisor. It runs the household's core service containers and the two virtual machines that route its traffic, all behind the OPNsense router. This page names roles only; addresses and ids drift, so the config owns them, and the probes below read the live state.

## What runs here

The containers give the household its DNS, with an ad-blocking resolver that forwards upstream to NextDNS and a separate DNS64 resolver that lets IPv6-only clients reach IPv4 hosts. Alongside them run the UniFi network controller, a reverse proxy that also carries an SSH multiplexer and a Cloudflare tunnel, a Minecraft server, an object store, and the Tack project tracker. One more container, sit6, terminates the 6in4 tunnel from the Berylax router and announces that router's IPv6 prefix to the MWAN VM over BGP, and vault loads the kernel's sit tunnel module at boot so that container can create its tunnel.

Two virtual machines do the routing: the OPNsense LAN router, and the MWAN VM that owns the wide-area links. Vault also runs one service of its own outside the guests, the MWAN watchdog, which watches the MWAN VM from the host and rolls it back to a known-good snapshot when a change breaks its connectivity.

## Reading the current state

To see what is actually running rather than what a doc claims, probe vault directly. These are read-only:

```bash
ssh root@<vault> 'qm list'    # virtual machines with their ids and status
ssh root@<vault> 'pct list'   # containers with their ids and status
ssh root@<vault> "systemctl --plain --no-legend list-units 'mwan*'"  # the host MWAN services
```

Reach a specific guest to read its own state, preferring IPv6, using the entry points in [access.md](https://github.com/agoodkind/configs/blob/main/docs/ops/infra/access.md).
