# MWAN packet forwarding performance

MWAN forwards packets in the guest kernel. Routes, policy rules, and nftables
rules do not send packet bytes to the daemon. Separate router and gateway guests
still use host CPU to transfer packets across their virtual bridge.

OPNsense [disables several NIC offloads by default](https://docs.opnsense.org/manual/interfaces_settings.html)
and [disables system-wide receive-side scaling by default](https://docs.opnsense.org/troubleshooting/performance.html).
[IPS mode requires hardware offloads to be disabled](https://docs.opnsense.org/manual/ips.html).
Available acceleration depends on the selected features, driver, and
hardware.

## Attachment paths

The WAN attachment and the router attachment are independent. These diagrams
show PCI passthrough, virtual WAN, and host bridge attachment.

### ISP NIC passed to the gateway

PCI passthrough assigns the physical NIC to the gateway guest. The WAN
attachment does not use a host bridge for packet transfer.

```mermaid
flowchart LR
  nic[ISP NIC] -->|PCI passthrough| gateway[Gateway guest kernel]
```

### ISP NIC attached to the host

A virtual WAN attaches the ISP NIC to the host. The host transfers packets
between that NIC and the gateway guest.

```mermaid
flowchart LR
  nic[ISP NIC] --> host[Host kernel]
  host --> bridge[Virtual bridge]
  bridge --> gateway[Gateway guest kernel]
```

### Router and gateway on one host bridge

Separate router and gateway guests use the host bridge for their shared
attachment. PCI passthrough on a WAN NIC does not remove this transfer.

```mermaid
flowchart LR
  lan[LAN] --> router[Router guest kernel]
  router --> bridge[Host bridge]
  bridge --> gateway[Gateway guest kernel]
  gateway --> nic[Passed through ISP NIC]
```

In the architecture example, ISP-1 and ISP-2 use PCI passthrough. ISP-3 uses a
virtual WAN. Every packet between the router and gateway guests uses their
host bridge.

## Measurement on August 16, 2026

A LAN client using ISP-1 measured 693 Mbit/s download and 1782 Mbit/s upload
with a public speed test. At the upload peak, the two guest copy threads used
1.11 CPU cores on a 12-core host.

| CPU use | Before test | Upload peak |
| --- | ---: | ---: |
| Router guest copy thread | 0.01 cores | 0.59 cores |
| Gateway guest copy thread | 0.01 cores | 0.52 cores |
| Both copy threads | 0.02 cores | 1.11 cores |
| Router guest forwarding | Not measured | 4.54 cores |
| Gateway guest forwarding | Not measured | 2.01 cores |

The guest forwarding measurements total 6.55 cores at the upload peak. The
copy threads account for about 15% of the combined 7.66 cores. This test did
not measure faster links or isolate the cause of the lower download result.

## Planned investigation

The proposed XR11 Broadcom 57504 deployment needs a decision on whether the
host or an LXC owns routing and packet rules. MWAN-527 tracks measurements of
bridge transfer cost and hardware offload for that decision.
