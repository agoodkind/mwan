> Read the [repository context](../../../README.md) before using copied commands or historical plans.

# Recover a WireGuard roaming failure

Recover the suburban tunnel by recording both peer views, verifying MWAN
steering, and resetting only suburban's learned OPNsense endpoint.

## 1. Record both peer views

Capture the learned endpoints and handshake times before changing either peer:

```bash
ssh suburban \
  'wg show wg0 endpoints; wg show wg0 latest-handshakes'
ssh agoodkind@router.home.goodkind.io \
  'sudo wg show all endpoints; sudo wg show all latest-handshakes'
```

Each endpoint names the remote peer, so the two values should not be equal.
Suburban should show one public address for `home.goodkind.io`, while OPNsense
should show suburban's current public address and port.

Capture the suburban observer log. A `wg-peer-stalled` alert confirms that a
previously active peer crossed the handshake-age threshold, but it does not
identify which WAN path failed.

```bash
ssh suburban \
  'journalctl -u mwan-ifmgr@host -n 200 --no-pager'
```

## 2. Verify MWAN steering

Read the live rules on the production MWAN gateway:

```bash
ssh root@mwan.home.goodkind.io \
  'nft list chain inet mangle prerouting'
ssh root@mwan.home.goodkind.io \
  'nft list chain inet mwan_steer prerouting'
```

The mangle chain must mark new inbound traffic by provider, pin
OPNsense-initiated WireGuard traffic, and restore the connection mark for
established traffic. The steering chain must restrict provider selection to
new traffic whose packet mark is still zero.

If either chain or rule is missing, stop this recovery. Restore the MWAN data
plane with [Inspect the data plane](../../mwan/dataplane.md#inspect-the-data-plane)
before changing a WireGuard endpoint.

## 3. Reset suburban's learned endpoint

This command changes the live endpoint but does not rewrite the WireGuard
configuration. Use the OPNsense peer public key shown by the first suburban
command:

```bash
ssh suburban
wg set wg0 peer <opnsense-public-key> endpoint home.goodkind.io:51820
ping6 -c 3 <opnsense-address-across-the-tunnel>
```

The ping creates traffic immediately, so WireGuard can complete a new
handshake through the selected public address.

## 4. Verify recovery

Run the endpoint and handshake commands from step 1 again. Recovery requires
all three results:

- Suburban shows a current public address for `home.goodkind.io`.
- OPNsense shows suburban's current public address and port.
- The latest handshake advanced and traffic across the tunnel succeeds.

If the endpoint moves again and the handshake stalls, preserve the new output
and return to MWAN routing diagnosis. Repeating the endpoint reset would erase
the next useful observation without correcting the path.
