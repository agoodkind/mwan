> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Networking and DHCP diagnosis

IPv6 connectivity is P0. Treat IPv6 issues as critical priority and diagnose the
IPv6 stack first, then IPv4. See [the infrastructure overview](https://github.com/agoodkind/configs/blob/main/docs/infra.md) for
the current live network state and [access.md](https://github.com/agoodkind/configs/blob/main/docs/ops/infra/access.md) for SSH paths between
hosts.

## Configuration preferences

- Prefer IPv6 literals over IPv4 in configuration files and bindings:
  `[::1]` rather than `127.0.0.1`, `[::]` rather than `0.0.0.0`.
- When verifying connectivity (SSH, curl, etc.), test the IPv6 address first.

## DHCPv6 reservation matching in KEA

### Root cause: DUID instability

Containers and VMs sometimes get a different IPv6 address than the
MAC-based reservation specifies. The usual cause is that the client's DUID
changes.

systemd-networkd generates DUIDs that can change:

- `DUIDType=vendor` (default) is derived from `/etc/machine-id` and changes when
  the container is recreated.
- `DUIDType=link-layer` is derived from the MAC and is stable if the MAC is
  pinned.
- `DUIDType=uuid` is derived from the product UUID and may change.

Two related problems compound the DUID issue:

- When DUID changes, KEA creates new leases but the old ones remain active and
  conflict with the new ones.
- Without `host-reservation-identifiers` matching the actual identifier used in
  reservations, KEA never prioritises MAC-based matching.

### KEA configuration principles

Three knobs control reservation matching.

`mac-sources` tells KEA how to extract a MAC address from DHCPv6 packets:

- `"duid"` extracts the MAC from a DUID-LL (format `00:03:00:01:XX:XX:XX:XX:XX:XX`).
- `"ipv6-link-local"` extracts the MAC from the EUI-64 link-local address.
- `"client-link-addr-option"` uses option 79 if provided by a relay agent.

`host-reservation-identifiers` controls which identifier types KEA checks and
in what order. The reservation's identifier must be in this list, otherwise it
will not match.

Per-reservation identifier types:

- `duid` matches the DUID directly and requires a stable DUID.
- `hw-address` matches the MAC and requires that MAC extraction works.

Decision tree:

1. Is the DUID stable for a given MAC?
   - DUID-LL: use `duid` in the reservation,
     `host-reservation-identifiers: ["duid", "hw-address"]`.
   - DUID changes: use `hw-address` in the reservation and make sure
     `mac-sources` can extract the MAC.
2. Can the MAC be extracted at all?
   - DUID-LL (`00:03:00:01:...`): `mac-sources: ["duid"]` works.
   - DUID-EN (`00:02:...`): `mac-sources: ["duid"]` does not work. Use
     `["ipv6-link-local"]` or `["client-link-addr-option"]`.
   - Other formats: typically need `["ipv6-link-local"]` or a relay agent.

### Required systemd-networkd configuration

```ini
[DHCPv6]
DUIDType=link-layer
```

This produces a DUID-LL of `00:03:00:01:XX:XX:XX:XX:XX:XX` where the last six
bytes are the MAC. DUID-LL is stable as long as the MAC is pinned. If the MAC
changes, the DUID changes.

## Diagnosis workflow

### 1. Inspect container or VM state

```bash
ssh root@<host> "ip -6 addr show eth0"
ssh root@<host> "networkctl status eth0 | grep -i duid"
ssh root@<host> "ip link show eth0 | grep -i link/ether"
```

### 2. Inspect KEA leases

```bash
ssh <kea-host> "sudo cat /var/db/kea/kea-leases6.csv | grep -i '<mac>'"
```

The [kea/Rakefile](https://github.com/agoodkind/configs/blob/main/kea/Rakefile) provides higher-level helpers, for
example `rake lease:get6_by_ip[<ip>]` and
`rake lease:cleanup6_by_mac[<mac>]`.

### 3. Identify DUID instability

Look for multiple different DUIDs for the same MAC:

- DUID-EN format `00:02:00:00:XX:XX:...` is machine-id based and unstable.
- DUID-LL format `00:03:00:01:XX:XX:XX:XX:XX:XX` is MAC based and stable.

### 4. Inspect KEA configuration

```bash
grep -A 2 "host-reservation-identifiers" /path/to/kea-dhcp6.conf
grep "mac-sources" /path/to/kea-dhcp6.conf
```

### 5. Clean up stale leases

By MAC, when DUID has changed:

```bash
cd /path/to/bind-kea
rake lease:cleanup6_by_mac[<mac>,<subnet_id>]
```

By DUID, when you have a specific DUID:

```bash
rake lease:cleanup6_conflicts[<duid>,<subnet_id>]
```

By IP, when the wrong IP is known:

```bash
rake lease:delete6_by_ip[<ip>]
```

### 6. Renew DHCP on the client

```bash
ssh root@<host> "systemctl restart systemd-networkd"
# or
ssh root@<host> "networkctl renew eth0"
```

### 7. Verify the reservation is honoured

```bash
ssh root@<host> "ip -6 addr show eth0 | grep 'scope global'"
ssh <kea-host> "sudo cat /var/db/kea/kea-leases6.csv | grep '<expected-ip>'"
```

## Common issues

### Container gets a different IP each time

1. Check DUID stability:

   ```bash
   ssh root@<host> "networkctl status eth0 | grep -i 'DUID:'"
   ssh <kea-host> "cat /var/db/kea/kea-leases6.csv | grep '<mac>' \
     | awk -F',' '{print $2}' | sort -u"
   ```

2. Identify the DUID format:
   - `00:02:...` is DUID-EN (machine-id based, unstable across recreations).
   - `00:03:00:01:...` is DUID-LL (MAC based, stable if the MAC is pinned).
   - `00:04:...` is DUID-UUID (may change).
3. Determine root cause:
   - DUID changes: check `DUIDType` in systemd-networkd config.
   - MAC changes: check Proxmox or VM config for MAC pinning.
   - Multiple DUIDs exist: stale leases from previous DUID formats.
4. Fix at the source: configure `DUIDType=link-layer`, pin the MAC in the
   hypervisor config, or clean up the conflicting leases.

### Reservation exists but is not honoured

1. Verify the reservation:

   ```bash
   grep -A 5 "reservations" /path/to/kea-dhcp6.conf
   grep "host-reservation-identifiers" /path/to/kea-dhcp6.conf
   ```

2. Check the client identifier:

   ```bash
   ssh root@<host> "networkctl status eth0 | grep -i 'DUID:'"
   ssh root@<host> "ip link show eth0 | grep -i link/ether"
   ```

3. Check active leases:

   ```bash
   ssh <kea-host> "cat /var/db/kea/kea-leases6.csv | grep '<mac-or-duid>'"
   ```

4. Identify the mismatch:
   - Reservation uses `hw-address` but the client sends DUID-EN, so no MAC is
     extractable.
   - Reservation uses `duid` but the DUID format changed.
   - Multiple active leases with different identifiers for the same MAC.
   - `host-reservation-identifiers` does not include the identifier type used.
5. Fix at the source: update the reservation or `host-reservation-identifiers`,
   clean up conflicting leases, or change `mac-sources`.

### Multiple active leases for the same MAC

Root cause: the DUID changed over time and created multiple lease entries.

```bash
rake lease:cleanup6_by_mac[<mac>,<subnet_id>]
```

## Last resort: hardcoded DUID

Only use when the root cause cannot be fixed. If a DUID continues to change
despite `DUIDType=link-layer`, you can hardcode it:

1. Read the current DUID:

   ```bash
   ssh root@<host> "networkctl status eth0 | grep -i 'DUID:'"
   ```

2. Extract the raw DUID without the type prefix. DUID-LL is
   `00:03:00:01:bc:24:11:1d:2c:0f` and the raw MAC-based portion is
   `bc:24:11:1d:2c:0f`.
3. Pin the value in systemd-networkd:

   ```ini
   [DHCPv6]
   DUIDType=link-layer
   DUIDRawData=bc:24:11:1d:2c:0f
   ```

4. Update the KEA reservation to use the DUID rather than the MAC.

This is a workaround. The proper fix is to ensure DUID stability through MAC
pinning and `DUIDType=link-layer`.

## IPv6 reachability through MWAN

### Webpass small-payload ICMPv6 drop

Webpass silently drops ICMPv6 echo requests with payload `<= 8` bytes, so
the BSD `ping6` default (8-byte payload) returns 100% loss even when the
path is healthy. Probe IPv6 from FreeBSD or macOS with `ping6 -s 16 ...`
instead. Linux `ping6` defaults to a 56-byte payload and is unaffected.

### MWAN dataplane state for the customer prefix

Three pieces of state on MWAN must be present for OPNsense LAN traffic to
reach the public IPv6 internet:

- **Webpass PD lease.** `mwan pd enwebpass0` shows the live delegation.
  The delegation renumbers, so the command output is the source of truth,
  never a stored value.
- **NPT rules** in `nft list table ip6 nat`. The ifmgr npt module creates the
  table and programs its rules from the live delegation. `mwan debug npt`
  displays the current rules.
- **Internal return route.** The internal prefix (`mwan_internal_prefix`
  in the MWAN group vars) must route to the OPNsense edge over the
  internal link, in the main table and in every WAN table. The ifmgr
  wan.routes module writes these routes. Without them, MWAN cannot forward
  conntrack replies for any non-shared LAN `/64` back to OPNsense.

### Source-sweep diagnostic

When only some IPv6 sources work, sweep across every routable `/64` from
OPNsense:

```bash
ssh router 'for src in 3d06:bad:b01::1 3d06:bad:b01:1::1 3d06:bad:b01:2::1 \
    3d06:bad:b01:a::1 3d06:bad:b01:10::1 3d06:bad:b01:64::1 \
    3d06:bad:b01:fe::2; do
        echo "=== source $src ==="
        ping6 -c 2 -s 16 -S "$src" 2606:4700:4700::1111
done'
```

Interpretation:

- All sources work: IPv6 path healthy.
- All sources fail: WAN side broken (Webpass, NPT, or upstream).
- Shared LAN `/64` (`3d06:bad:b01::1`) + transit `/64`
  (`3d06:bad:b01:fe::2`) work; everything else fails: MWAN is missing the
  internal `/60` return route. The shared `/64` reaches OPNsense without a
  route because OPNsense `vtnet0` and MWAN `enmgmt0` are L2 neighbors on
  that subnet.

### Verify and recover the internal return route

On MWAN, grep the route table dump for the internal prefix
(`mwan_internal_prefix` in the MWAN group vars):

```bash
ip -6 route show table all | grep '3d06:bad:b01::/60'
```

Expected: one entry in the main table and one in each provider's routing table.
The provider list in the MWAN group vars names the table ids.

If empty, recover with `systemctl restart mwan-ifmgr@wan`, which forces a
full reconcile pass; the pass is idempotent.

The wan.routes module watches every WAN interface and the internal link
over netlink and reconciles on each default-route change plus a periodic
tick, so any flap of those interfaces reinstalls the route automatically.

## Pre-commit checklist

1. Verify `host-reservation-identifiers: ["hw-address"]` (or the matching
   identifier list) in KEA config.
2. Verify `mac-sources` includes the right extraction method.
3. Verify `DUIDType=link-layer` in systemd-networkd config.
4. Verify the MAC address is pinned in Proxmox or VM config.
5. Check for stale leases before deploying changes.
6. Verify reservation matching after deployment.
