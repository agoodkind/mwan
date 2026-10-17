# MWAN packet forwarding performance

MWAN forwards packets in the guest kernel. Routes, policy rules, and nftables
rules do not send packet bytes to the daemon. A host bridge uses host CPU to
transfer packets between router and gateway guests. A NIC can instead switch
packets between hardware virtual functions assigned to separate containers.

OPNsense [disables several NIC offloads by default](https://docs.opnsense.org/manual/interfaces_settings.html)
and [disables system-wide receive-side scaling by default](https://docs.opnsense.org/troubleshooting/performance.html).
[IPS mode requires hardware offloads to be disabled](https://docs.opnsense.org/manual/ips.html).
Available acceleration depends on the selected features, driver, and
hardware.

## Attachment paths

The WAN attachment and the router attachment are independent. These diagrams
show PCI passthrough, virtual WAN, host bridge, and hardware virtual-function
attachment.

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

### Router and gateway on hardware virtual functions

Single Root I/O Virtualization (SR-IOV) creates virtual functions (VFs), which
provide separate PCI interfaces on one NIC. The XR11 assigns one VF to each
container. The NIC switches packets between these VFs without an external
cable or a software host bridge. The host kernel still runs the containers'
network stacks and processes interrupts.

```mermaid
flowchart LR
  gateway[Gateway LXC] --> gatewayvf[Gateway VF]
  gatewayvf --> nic[NIC internal switch]
  nic --> routervf[Router VF]
  routervf --> router[Router LXC]
```

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

## XR11 VF measurements on October 17, 2026

Tests ran between 05:46 and 05:54 UTC, on October 16 in Pacific Daylight Time.
The PowerEdge XR11 used a Broadcom BCM57504, an Intel Xeon Silver 4309Y with
8 physical cores and 16 logical CPUs, and Proxmox kernel `7.0.14-20-pve`.
The `bnxt_en` driver reported firmware `236.1.173.0/pkg 36.11.73.00`.

WAN container 313 and LAN container 314 each owned a `mwanbr` VF. Their PCI
functions, `0000:1a:01.0` and `0000:1a:01.1`, shared physical function
`0000:19:00.1`. Its external port, `nic1`, reported an absent cable before
and after testing. The endpoints used their direct transit-subnet route.
These tests exercised the NIC's internal VF switch.

Each container had two assigned logical CPUs: 1-2 for container 313 and
13-14 for container 314. `iperf3` 3.18 ran in each container's network
namespace and cgroup, which applied those CPU assignments to the test
processes. The interfaces used a 1500-byte MTU. TCP used eight parallel
streams except for the single-stream test. The UDP test used four streams,
each requesting 2.5 Gbit/s with 1400-byte datagrams.

### Throughput and CPU use

Throughput uses the receiver's result after a three-second warmup. Durations
exclude that warmup. CPU means use one-second host and container cgroup
samples and exclude the first four samples. One CPU unit represents one
fully busy logical CPU. Each container can use two; the host has sixteen.
Host CPU includes the containers, interrupts, and background services.
The preceding ten-second idle sample averaged 0.96 host CPU units. The
measurements do not subtract that baseline.

Throughput columns use Gbit/s. Bidirectional tests used eight streams in
each direction.

| Test | Duration | 313 to 314 | 314 to 313 | 313 CPU | 314 CPU | Host CPU |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Single TCP stream | 30 s | 47.87 | 0.00 | 1.70 | 0.94 | 3.25 |
| Parallel TCP | 60 s | 66.13 | 0.00 | 2.00 | 1.80 | 4.38 |
| Reverse parallel TCP | 60 s | 0.00 | 69.74 | 1.98 | 1.96 | 4.63 |
| Bidirectional TCP | 60 s | 31.31 | 30.42 | 1.95 | 2.00 | 4.45 |
| Bidirectional, disabled | 30 s | 7.77 | 9.07 | 1.50 | 1.51 | 5.75 |
| Bidirectional, re-enabled | 60 s | 32.79 | 31.53 | 1.98 | 2.00 | 4.51 |
| UDP stress | 30 s | 9.54 | 0.00 | 2.00 | 1.02 | 3.76 |
| Bidirectional, initial settings | 30 s | 32.59 | 27.91 | 1.95 | 2.00 | 4.53 |

### Offload evidence

The comparison temporarily disabled TCP segmentation offload (TSO), generic
segmentation offload (GSO), generic receive offload (GRO), and hardware GRO on
both VFs. These features split outgoing packets or combine incoming packets
to reduce processing work. The comparison kept checksum offloads enabled.
It measured their combined effect and did not isolate each feature's cost.

Combined bidirectional throughput fell from 61.73 to 16.84 Gbit/s with those
features disabled. Host CPU use increased from 4.45 to 5.75 units, and
software interrupt processing increased from 0.93 to 3.69 units. The
enabled run delivered 3.67 times the throughput with about 22% less host CPU
use. The runs used different durations and included background host work.

The single-stream receiver's `rx_tpa_bytes` counter increased by
191,362,528,254 bytes. The [Broadcom driver exposes this packet aggregation
counter](https://github.com/torvalds/linux/blob/master/drivers/net/ethernet/broadcom/bnxt/bnxt_ethtool.c).
Its increase supplies runtime evidence of receive aggregation beyond the
enabled feature flags. Driver counter deltas include warmup and do not
align exactly with the steady-state throughput intervals.

Re-enabling TSO also enabled `tx-tcp-mangleid-segmentation`, which initially
had been off. The first re-enabled run therefore differed from the initial
settings by that flag. The final repeat followed restoration of that flag
and measured 60.50 Gbit/s combined. Every feature flag on both VFs and the
physical WAN and LAN interfaces matched the initial snapshot before and
after that repeat.

### Losses and test limits

The single-stream run recorded zero TCP retransmissions and zero receive-ring
drops. The saturated parallel runs recorded receive-ring discards, receive
buffer errors, aggregation errors, and TCP retransmissions. The
69.74 Gbit/s run recorded 19,823 retransmissions; container 313 recorded
12,792 receive-ring discards, 5,865 receive buffer errors, and 33 aggregation
errors during its complete run.

The UDP sender generated 9.70 Gbit/s, and the receiver reported 9.54 Gbit/s
with 1.6208% packet loss. Container 314 recorded 200,253 receive-ring discards
during that complete run. The different sampling intervals prevent a direct
one-to-one comparison between driver discards and steady-state UDP loss.

The parallel TCP tests used nearly all of each container's assigned CPU
capacity. Their CPU measurements include traffic generation and reception.
The results establish VF interconnect throughput and active offloads. They
do not establish MWAN routing, NAT, firewall, BPF program offload, or external
WAN-to-LAN forwarding performance. The tests did not compare the same
workload over a software host bridge or demonstrate lossless saturation.

Both containers remained running after testing. WAN Internet and
intercontainer ping checks each received all three replies. The test
processes exited, and cleanup removed the temporary tools.

## Remaining investigation

MWAN-527 still needs a controlled bridge-versus-VF comparison and a routed
workload with external LAN and WAN endpoints. Those measurements must
separate endpoint CPU use from forwarding work and record packet loss
alongside throughput.
