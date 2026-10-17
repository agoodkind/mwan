# MWAN packet forwarding performance

MWAN forwards packets in the guest kernel. Routes, policy rules, and nftables
rules do not send packet bytes to the daemon. A host bridge uses host CPU to
transfer packets between router and gateway guests. A NIC can instead switch
packets between hardware virtual functions assigned to separate containers.

Every CPU percentage uses the complete capacity of the experiment’s host.
Host CPU includes guest work, traffic generation, and other active host work.

OPNsense [requires disabled hardware offloads in IPS mode](https://docs.opnsense.org/manual/ips.html).
OPNsense [disables several NIC offloads by default](https://docs.opnsense.org/manual/interfaces_settings.html)
and [disables system-wide receive-side scaling by default](https://docs.opnsense.org/troubleshooting/performance.html).

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

| Scope | Evidence |
| --- | --- |
| Client connection | ISP-1 public speed test |
| Faster links | The test did not exercise faster links. |
| Download bottleneck | The test did not isolate the bottleneck. |

| Measurement | Result |
| --- | ---: |
| Client download | 693 Mbit/s |
| Client upload | 1782 Mbit/s |
| Host CPU cores | 12 |

| CPU, % of recorded host | Before test | Upload peak |
| --- | ---: | ---: |
| Router guest copy thread | 0.08% | 4.92% |
| Gateway guest copy thread | 0.08% | 4.33% |
| Both copy threads | 0.17% | 9.25% |
| Router guest forwarding | Not measured | 37.83% |
| Gateway guest forwarding | Not measured | 16.75% |
| Both guests forwarding | Not measured | 54.58% |
| Forwarding and copy threads | Not measured | 63.83% |

## XR11 endpoint measurement on October 17, 2026

The VF benchmark measures endpoint traffic between the containers through
the NIC's internal switch.

| Claim | Boundary | Basis |
| --- | --- | --- |
| Routed capacity | Unmeasured | Endpoints used the VF transit subnet. |
| Forwarding latency | Unmeasured | The test did not use routed probes. |
| Forwarding CPU cost | Not isolated | CPU includes endpoint work. |
| UDP loss cause | Unidentified | The test did not trace lost datagrams. |
| Bridge CPU savings | Unmeasured | The test did not use a host bridge. |
| BPF hardware offload | Unverified | The test did not inspect NIC execution. |

### Test conditions

| Condition | Value |
| --- | --- |
| Host | Dell PowerEdge XR11 |
| CPU | Intel Xeon Silver 4309Y |
| CPU topology | 8 cores, 16 logical CPUs |
| Each container’s CPU allocation | 12.50% of host capacity |
| Kernel | Proxmox `7.0.14-20-pve` |
| NIC | Broadcom BCM57504 |
| Driver | `bnxt_en` |
| NIC firmware | `236.1.173.0/pkg 36.11.73.00` |
| Physical function | `0000:19:00.1` |
| `nic1` link | The port did not detect a cable before or after tests. |
| UTC interval | 05:46 to 05:54 |
| Pacific date | October 16, 2026 |
| Tool | `iperf3` 3.18 |
| Process placement | Container network namespace and cgroup |
| VF MTU | 1500 bytes |
| TCP streams | 8 per direction, except the single-stream test |
| UDP streams | 4 at 2.5 Gbit/s each |
| UDP datagram size | 1400 bytes |
| Warmup | 3 seconds |
| CPU sampling | 1-second host and cgroup samples; first 4 omitted |
| Host CPU accounting | Includes all host work; does not subtract idle use |

| Container | Role | CPUs | VF (`mwanbr`) | Physical port |
| --- | --- | --- | --- | --- |
| 313 | WAN | 1-2 | `0000:1a:01.0` | 2 (`wan`) |
| 314 | LAN | 13-14 | `0000:1a:01.1` | 0 (`lan`) |

### Endpoint throughput and CPU use

Receiver throughput excludes warmup, while driver counter deltas cover the
complete run.

#### TCP direction and stream count

| Workload | Duration | Forward, Gbit/s | Reverse, Gbit/s | WAN container CPU | LAN container CPU | Host CPU |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Single TCP stream | 30 s | 47.87 | 0.00 | 10.60% | 5.88% | 20.29% |
| Parallel TCP | 60 s | 66.13 | 0.00 | 12.48% | 11.27% | 27.40% |
| Reverse parallel TCP | 60 s | 0.00 | 69.74 | 12.38% | 12.24% | 28.93% |

#### Bidirectional TCP and offloads

The re-enabled run also enabled `tx-tcp-mangleid-segmentation`. The initial
settings repeat restored that flag to its original value. The comparison
changes several offloads together and does not isolate a single feature.

| VF configuration | Duration | Forward, Gbit/s | Reverse, Gbit/s | Combined, Gbit/s | WAN container CPU | LAN container CPU | Host CPU |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Initial settings | 60 s | 31.31 | 30.42 | 61.73 | 12.18% | 12.49% | 27.84% |
| Segmentation and aggregation disabled | 30 s | 7.77 | 9.07 | 16.84 | 9.39% | 9.45% | 35.91% |
| Segmentation and aggregation re-enabled | 60 s | 32.79 | 31.53 | 64.33 | 12.40% | 12.48% | 28.21% |
| Initial settings repeat | 30 s | 32.59 | 27.91 | 60.50 | 12.21% | 12.49% | 28.33% |

#### UDP stress

| Workload | Duration | Received, Gbit/s | WAN container CPU | LAN container CPU | Host CPU |
| --- | ---: | ---: | ---: | ---: | ---: |
| Parallel UDP | 30 s | 9.54 | 12.49% | 6.37% | 23.50% |

### Offloads

The current settings match the initial and final runs on both VFs and the
physical interfaces. TSO splits outgoing TCP buffers into packets in the NIC,
while GSO provides software segmentation. GRO combines received packets,
and hardware GRO performs receive aggregation in the NIC.

| Feature | Current | VF disabled | VF re-enabled |
| --- | --- | --- | --- |
| Receive and transmit checksum offload | On | On | On |
| Scatter-gather | On | On | On |
| TCP segmentation offload (TSO) | On | Off | On |
| Generic segmentation offload (GSO) | On | Off | On |
| Generic receive offload (GRO) | On | Off | On |
| Hardware GRO | On | Off | On |
| Receive hashing | On | On | On |
| Large receive offload | Off | Off | Off |
| `tx-tcp-mangleid-segmentation` | Off | Off | On |

| Measurement | Result |
| --- | ---: |
| Throughput ratio, first bidirectional / disabled | 3.67 |
| Host CPU decrease, enabled vs disabled | 8.07 percentage points |
| Software interrupt CPU, first bidirectional | 5.81% |
| Software interrupt CPU, disabled | 23.05% |
| Hardware aggregation bytes, single-stream receiver | 191,362,528,254 |
| Idle host CPU, preceding ten-second average | 6.02% |

The [driver's hardware aggregation counter](https://github.com/torvalds/linux/blob/master/drivers/net/ethernet/broadcom/bnxt/bnxt_ethtool.c),
`rx_tpa_bytes`, records bytes combined in the NIC before host processing.

### Packet loss

The receive counters belong to the receiving VF in each TCP test.

| Test | Retransmissions | Receive drops | Buffer errors | Aggregation errors |
| --- | ---: | ---: | ---: | ---: |
| Single stream | 0 | 0 | 0 | 0 |
| Reverse parallel | 19,823 | 12,792 | 5,865 | 33 |

| UDP measurement | Result |
| --- | ---: |
| Offered load | 9.70 Gbit/s |
| Received throughput | 9.54 Gbit/s |
| Datagram loss | 1.6208% |
| Receiver VF drops over the complete run | 200,253 |

## XR11 routed comparison on October 17, 2026

The routed benchmark compares an OPNsense VM with a Linux network namespace
on the XR11. Both configurations forward between the same Linux peer
namespaces through the NIC’s internal switch.

| Scope | Boundary |
| --- | --- |
| Packet processing | IPv4 routing with minimal stateful PF or nftables rules |
| Physical network | The tests did not use the external WAN or LAN links or a host bridge. |
| Production behavior | The tests did not exercise the MWAN classifier, NAT, VPN, IPS, or IPv6. |
| Comparison | Results compare complete configurations and do not isolate operating-system cost or individual offload gains. |
| CPU accounting | Host CPU includes the router, peer traffic generators, and background work. The Linux tests do not isolate router CPU. |
| Repetition | Each configuration and workload has one run. Small differences do not establish a stable ranking. |

### Routed test conditions

| Condition | OPNsense | Linux |
| --- | --- | --- |
| Software | OPNsense `26.7.4_1`, FreeBSD `15.1-RELEASE-p3` | Proxmox kernel `7.0.14-20-pve` |
| Driver | Updated upstream [if_bnxt VF support](https://github.com/opnsense/src/commit/01510f095c2ba12ced4959a102403c261c95b2a4), built against kernel commit `083dc7025377`; the stock module did not attach the VFs. | Native `bnxt_en` |
| Attachment | Direct PCI passthrough of spare VFs | Direct VF assignment to a network namespace |
| Spare physical function | `0000:19:00.3` | Same physical function |
| Router VFs | `0000:1a:03.0` and `0000:1a:03.2` | Same VFs after VM removal |
| Peer VFs | `0000:1a:03.1` and `0000:1a:03.3` | Same VFs and peer namespaces |
| Guest vCPU allocation in final comparisons | 50.00% of host logical CPU capacity | Router and peers share the full host without a separate router CPU limit. |
| Guest memory | 8 GiB | Not applicable |
| Guest CPU model | `host` | Not applicable |
| Router receive queues per VF | 7 | 4 combined channels |
| VF MSI-X table | 8 entries; expansion request returned unsupported | Same hardware |
| Final receive data descriptors per ring | 1024 | 511 |
| OPNsense receive completion/data/aggregation descriptors | `8192,1024,1024` | Not applicable |
| Checksum offload and TSO | On | On |
| GSO, GRO, and hardware GRO | Not applicable | On |
| LRO | Off | Off |
| Receive-side scaling | On, `bits=3`, 8 bound network workers | Receive hashing on |
| Global `net.isr.dispatch` in final comparisons | `deferred` | Not applicable |
| IPv4 protocol dispatch | Explicit `hybrid` policy | Not applicable |
| `tx-tcp-mangleid-segmentation` | Not applicable | On for router VFs; off for peer VFs |
| MTU | 1500 bytes | 1500 bytes |
| Lab addressing | Existing approved transit subnet split into isolated segments | Same peer addresses and routes |
| Traffic generator | Host `iperf3` 3.18 in peer network namespaces | Same binary and placement |
| UDP streams | 4 | 4 |
| Measured workload duration | 30 seconds per run | 30 seconds per run |
| Warmup | 3 seconds | 3 seconds |
| Receiver throughput | Excludes warmup | Excludes warmup |
| CPU sampling | 1-second host and VM cgroup intervals; first 4 omitted | Same host sampling; first 4 omitted |
| Hardware and software-queue counter interval | Complete run, including warmup | Complete run, including warmup |
| Run start times, Pacific | 00:56 to 01:36 | 01:39 to 01:41 |

### OPNsense CPU and offload trials

The [kernel’s IPv4 handler](https://github.com/opnsense/src/blob/083dc7025377/sys/netinet/ip_input.c#L129-L140)
uses an explicit hybrid policy when receive-side scaling is active. The
software-queue snapshot confirms that IPv4 retained this policy with a global
direct setting. The global-setting trials do not isolate IPv4 dispatch modes.

| Guest vCPU allocation | Global dispatch setting | Checksum and TSO | Streams per direction | Forward, Gbit/s | Reverse, Gbit/s | VM CPU | Host CPU | Retransmissions |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 25.00% | deferred | On | 8 | 8.67 | 6.20 | 24.65% | 42.54% | 119,553 |
| 25.00% | deferred | Off | 8 | 6.90 | 6.84 | 23.44% | 42.67% | 54,018 |
| 25.00% | hybrid | On | 8 | 6.35 | 7.71 | 24.10% | 43.14% | 92,579 |
| 50.00% | deferred | On | 8 | 11.69 | 8.11 | 46.28% | 66.34% | 88,797 |
| 50.00% | hybrid | On | 8 | 9.39 | 9.47 | 42.76% | 68.12% | 93,869 |
| 50.00% | direct | On | 8 | 11.41 | 5.51 | 37.02% | 60.38% | 45,953 |

### OPNsense receive rings

The larger-ring runs improved combined TCP throughput and reduced hardware
receive discards with both recorded global settings.

| Receive data descriptors | Global dispatch setting | Streams per direction | Forward, Gbit/s | Reverse, Gbit/s | VM CPU | Host CPU | Retransmissions | Hardware receive discards |
| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 256 | deferred | 32 | 9.94 | 10.29 | 45.85% | 76.12% | 300,601 | 359,687 |
| 256 | hybrid | 32 | 11.01 | 9.13 | 47.05% | 75.87% | 278,604 | 341,146 |
| 1024 | deferred | 32 | 11.76 | 10.99 | 48.55% | 79.25% | 97,175 | 66,857 |
| 1024 | hybrid | 32 | 10.10 | 11.02 | 48.23% | 77.57% | 111,199 | 50,778 |

### Routed TCP throughput and CPU use

The fixed-load workload uses the same target rate in each direction. TCP
retransmissions remain part of the comparison; payload throughput does not
establish lossless forwarding.

| Workload | Configuration | Streams per direction | Target per direction, Gbit/s | Forward, Gbit/s | Reverse, Gbit/s | VM CPU | Host CPU | Retransmissions |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Single stream | OPNsense | 1 | Unlimited | 4.20 | 0.00 | 8.41% | 26.70% | 434 |
| Single stream | Linux | 1 | Unlimited | 32.00 | 0.00 | Not applicable | 30.19% | 0 |
| Parallel bidirectional | OPNsense | 32 | Unlimited | 11.76 | 10.99 | 48.55% | 79.25% | 97,175 |
| Parallel bidirectional | Linux | 32 | Unlimited | 20.86 | 20.19 | Not applicable | 96.71% | 471,054 |
| Fixed bidirectional | OPNsense | 32 | 9.00 | 9.06 | 9.07 | 41.42% | 72.87% | 47,090 |
| Fixed bidirectional | Linux | 32 | 9.00 | 9.00 | 9.00 | Not applicable | 51.53% | 377,678 |

### Routed UDP throughput and packet loss

The small-datagram generators did not sustain the requested load. Sent rates
below report actual offered traffic. The [kernel RSS configuration](https://github.com/opnsense/src/blob/083dc7025377/sys/net/rss_config.c#L496-L525)
does not use UDP ports for its default hash.

| Datagram payload, bytes | Configuration | Global dispatch setting | Target total, Gbit/s | Sent, Gbit/s | Received, Gbit/s | Datagram loss | VM CPU | Host CPU |
| ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1400 | OPNsense | deferred | 9.00 | 9.00 | 5.54 | 38.4611% | 25.21% | 62.76% |
| 1400 | OPNsense | hybrid | 9.00 | 9.00 | 3.45 | 61.6831% | 19.32% | 53.55% |
| 1400 | OPNsense | direct | 9.00 | 9.01 | 3.61 | 59.7357% | 25.47% | 60.09% |
| 1400 | Linux | Not applicable | 9.00 | 9.02 | 8.66 | 4.0317% | Not applicable | 55.39% |
| 64 | OPNsense | deferred | 1.00 | 0.80 | 0.27 | 65.6674% | 25.61% | 76.15% |
| 64 | Linux | Not applicable | 1.00 | 0.76 | 0.50 | 33.3590% | Not applicable | 68.45% |

Only the final OPNsense global-direct UDP trial captures software-queue
deltas. Its counters identify packet drops in the guest IPv4 software queue.
The Linux hardware aggregation counter confirms that receive aggregation
operated during routed TCP.

| Counter evidence | Result |
| --- | ---: |
| OPNsense IPv4 software queue drops on guest CPU 0, final global-direct UDP trial | 15,705,223 |
| OPNsense IPv4 software queue drops on other guest CPUs in that trial | 0 |
| Linux router hardware aggregation bytes, parallel bidirectional TCP | 170,330,560,865 |

### Meaning for the WAN and LAN configuration

| Use case | Supported conclusion |
| --- | --- |
| Bulk bidirectional TCP | The larger OPNsense guest and receive rings met the fixed payload target. Linux used less total host CPU at that target and recorded more retransmissions. |
| A large individual TCP transfer | The native Linux configuration delivered more throughput in the single-stream test. |
| High UDP packet rates | Both configurations lost datagrams at the tested offered loads. The OPNsense software-queue trace identifies a guest queue limit during its final UDP trial. |
| Offload configuration | Keep the verified Linux offload profile. The OPNsense results apply to the tested upstream driver and larger receive rings. |

## WAN and LAN capacity

Endpoint VF throughput exceeded the combined physical-link rate with the
enabled offload configuration.

| Measurement | Result |
| --- | ---: |
| WAN negotiated rate | 10 Gbit/s full duplex |
| LAN negotiated rate | 10 Gbit/s full duplex |
| Combined WAN-to-LAN and LAN-to-WAN link capacity | 20 Gbit/s |
| Final combined endpoint VF throughput | 60.50 Gbit/s |
| Final endpoint VF / combined physical-link rate | About 3.03 |

## Remaining investigation

MWAN-527 tracks the forwarding comparison with a software bridge.
