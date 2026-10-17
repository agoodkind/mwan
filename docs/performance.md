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

The XR11 uses Single Root I/O Virtualization (SR-IOV) to assign each
container a virtual function (VF), a separate PCI interface on the NIC. The
NIC switches packets between VFs while the host kernel runs the container
network stacks and handles interrupts.

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

## XR11 measurement on October 17, 2026

The PowerEdge XR11 used a Broadcom BCM57504 with the `bnxt_en` driver under
Proxmox kernel `7.0.14-20-pve`. The host had an Intel Xeon Silver 4309Y with
8 cores and 16 logical CPUs, and the NIC reported firmware
`236.1.173.0/pkg 36.11.73.00`.

The containers' `mwanbr` VFs shared physical function `0000:19:00.1` and used
a direct transit-subnet route. The physical port `nic1` did not detect a
cable before or after the tests.

| Container | Role | Assigned CPUs | VF |
| --- | --- | --- | --- |
| 313 | WAN | 1-2 | `0000:1a:01.0` |
| 314 | LAN | 13-14 | `0000:1a:01.1` |

`iperf3` 3.18 used each container's network namespace and cgroup with a
1500-byte MTU. The tests ran between 05:46 and 05:54 UTC, on October 16 in
Pacific Daylight Time. TCP used eight streams per direction except for the
single-stream test, while UDP used four streams at 2.5 Gbit/s each with
1400-byte datagrams.

### Throughput and CPU use

The receiver's throughput in Gbit/s and the test durations exclude a
three-second warmup, while driver counter deltas cover the complete run.

CPU averages omit the first four one-second host and cgroup samples. A value
of 1.00 represents one fully busy logical CPU, and the host figures include
the containers and interrupt processing. The results do not subtract the
preceding ten-second idle average of 0.96 host CPU units.

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

### Offloads

The enabled column matches the current settings on the VFs and physical
interfaces, while the disabled column applies only to the VF comparison.

| Feature | Enabled run | Disabled run |
| --- | --- | --- |
| Receive and transmit checksum offload | On | On |
| Scatter-gather | On | On |
| TCP segmentation offload (TSO) | On | Off |
| Generic segmentation offload (GSO) | On | Off |
| Generic receive offload (GRO) | On | Off |
| Hardware GRO | On | Off |
| Receive hashing | On | On |
| Large receive offload | Off | Off |
| `tx-tcp-mangleid-segmentation` | Off | Off |

TSO splits outgoing TCP buffers into packets in the NIC, while GSO provides
software segmentation. GRO combines received packets, and hardware GRO
performs receive aggregation in the NIC.

The first bidirectional run delivered 3.67 times the throughput of the run
with offloads disabled and used about 22% less host CPU. Software interrupt
processing accounted for 0.93 CPU units with offloads enabled and 3.69 with
offloads disabled. The comparison measures the combined effect of the
features over different run durations and includes background host work.

The single-stream receiver's
[hardware aggregation counter](https://github.com/torvalds/linux/blob/master/drivers/net/ethernet/broadcom/bnxt/bnxt_ethtool.c),
`rx_tpa_bytes`, increased by 191,362,528,254 bytes.

The re-enabled run differed from the initial settings only in
`tx-tcp-mangleid-segmentation`, which was on. The final repeat matched the
initial settings on every interface.

### Losses and test limits

The receive counters belong to the receiving VF in each TCP test.

| Test | Retransmissions | Receive drops | Buffer errors | Aggregation errors |
| --- | ---: | ---: | ---: | ---: |
| Single stream | 0 | 0 | 0 | 0 |
| Reverse parallel | 19,823 | 12,792 | 5,865 | 33 |

At an offered UDP load of 9.70 Gbit/s, the receiver lost 1.6208% of datagrams
and its VF counted 200,253 receive drops over the run. The measurements do
not identify the cause of UDP loss.

## WAN and LAN capacity

Container 313 owns physical port 2 as `wan`, and container 314 owns port 0 as
`lan` to the switch. Both physical links negotiate 10 Gbit/s full duplex,
which permits simultaneous WAN-to-LAN and LAN-to-WAN transfers totaling
20 Gbit/s. The final VF result is roughly three times the combined link rate.

The parallel TCP tests used nearly all of each container's assigned CPUs
for traffic generation and reception. The CPU measurements do not isolate
packet forwarding cost.

## Remaining investigation

MWAN-527 covers a comparison with a software bridge and end-to-end forwarding
tests. The VF measurements do not establish the capacity of the complete
WAN-to-LAN path, and the tests did not measure latency or verify hardware
execution of MWAN's BPF programs.
