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

## Address translation comparison

The translation tests use an isolated OPNsense VM and Linux peers on spare
Broadcom virtual functions. Captures at the receiving peer verify translated
addresses and replies before the throughput tests.

| Translation | What the translator changes | What the comparison measures |
| --- | --- | --- |
| NAT64 | The translator converts IPv6 packets to IPv4 packets and converts replies back. | An IPv6 client communicates with an IPv4 server. |
| Stateful NAT66 | The translator changes IPv6 addresses and may change ports. It remembers each connection to translate replies. | An IPv6 client communicates with an IPv6 server through an address mapping. |
| NPT66 under RFC 6296 | The translator changes the network prefix, the beginning of an IPv6 address that identifies its network. It also adjusts the device part of the address to preserve the checksum, a value that detects packet errors. | An IPv6 client communicates with an IPv6 server through a reversible prefix mapping. |
| OPNsense NPTv6 through PF BINAT | PF replaces the network prefix, keeps the device part of the address, and updates TCP or UDP checksums. | The OPNsense prefix-translation case uses a different address calculation from RFC 6296. |

### Translation inside OPNsense

[Tayga in OPNsense](https://docs.opnsense.org/manual/how-tos/tayga.html)
provides NAT64. OPNsense uses its PF firewall for NAT66 and NPT66, as its
[NAT documentation](https://docs.opnsense.org/manual/nat.html) specifies.
Tayga supplies only the IPv6-to-IPv4 comparison. The OPNsense
[NPTv6 rule generator](https://github.com/opnsense/core/blob/26.7.4/src/opnsense/mvc/app/library/OPNsense/Firewall/NptRule.php#L34-L42)
emits PF BINAT rules. The PF kernel replaces the prefix and updates
transport checksums; VPP uses RFC 6296 address correction.

The internal NAT64 path uses Tayga to convert the packet and PF to apply
outbound IPv4 NAT.

```mermaid
flowchart LR
  client[IPv6 client] --> ingress
  subgraph firewall[Same OPNsense VM]
    ingress[OPNsense checks IPv6 traffic] --> tayga[Tayga converts IPv6 to IPv4]
    tayga --> egress[PF applies outbound IPv4 NAT and OPNsense sends the packet]
  end
  egress --> server[IPv4 server]
```

### Translation in a VPP container

Tayga, Jool, and Vector Packet Processing (VPP) use the PowerEdge processor
to translate packets. VPP processes packets in groups and uses the Data
Plane Development Kit (DPDK) to control the network card without the usual
Linux network stack.

The external comparison makes OPNsense route traffic to the VPP container.
VPP translates the packet and sends it back to OPNsense. OPNsense then
routes it to the server. The return traffic uses the reverse sequence.
Both NAT64 configurations include PF outbound IPv4 NAT.

```mermaid
flowchart LR
  client[Client] --> ingress
  subgraph firewall[Same OPNsense VM]
    ingress[OPNsense checks traffic and routes it to VPP]
    egress[OPNsense receives the translated packet and routes it to the server]
  end
  ingress --> translator[VPP container translates the packet]
  translator --> egress
  egress --> server[Server]
```

| Comparison | OPNsense implementation | VPP implementation |
| --- | --- | --- |
| NAT64 | Tayga and PF outbound IPv4 NAT | [VPP NAT64](https://github.com/FDio/vpp/blob/v26.06/src/plugins/nat/nat64/nat64_cli.c) and PF outbound IPv4 NAT |
| Stateful NAT66 | PF outbound IPv6 NAT | [VPP CNAT](https://github.com/FDio/vpp/blob/v26.06/src/plugins/cnat/cnat_node_snat.c), which tracks connections and rewrites addresses |
| IPv6 prefix translation | PF BINAT for the OPNsense NPTv6 feature | [VPP NPT66](https://github.com/FDio/vpp/blob/v26.06/src/plugins/npt66/npt66_cli.c) |

### Translation in the NIC

The public driver interfaces do not establish hardware conversion between
IPv6 and IPv4. Broadcom advertises address and port translation in the
BCM5750x [TruFlow engine](https://docs.broadcom.com/doc/5750X-SB1XX).

Software must calculate address mappings and install rules before the NIC
can use those rules to rewrite packets. Stateful NAT66 also requires
software to allocate ports, install rules for replies, and expire mappings.
A fixed NPT66 test can calculate a translated address in software and test
whether the NIC applies that complete address correctly.

```mermaid
flowchart LR
  client[IPv6 client] --> ingress
  subgraph firewall[Same OPNsense VM]
    ingress[OPNsense checks traffic and routes it to the NIC rule]
    egress[OPNsense receives the translated packet and routes it to the server]
  end
  ingress --> rewrite[NIC applies an IPv6 address rewrite rule]
  rewrite --> egress
  egress --> server[IPv6 server]
  control[Software calculates mappings and installs rules] -.-> rewrite
```

The installed firmware rejected the virtual port allocation command needed
for the Linux hardware test. The spare physical function remained in legacy
mode, and the test did not install rewrite rules.
The public [Linux bnxt TC driver](https://github.com/torvalds/linux/blob/v7.0/drivers/net/ethernet/broadcom/bnxt/bnxt_tc.c)
programs IPv6 address and port rewrites. The public
[DPDK bnxt action table](https://github.com/DPDK/dpdk/blob/v26.03/drivers/net/bnxt/tf_ulp/ulp_rte_handler_tbl.c)
exposes address rewriting and hop-limit decrement, which reduces the
packet's remaining router count.

| Hardware workload | Public interface support | Proof status |
| --- | --- | --- |
| NAT64 | The bnxt interfaces do not expose the required IPv6-to-IPv4 header conversion. The DPDK driver does not implement the NAT64 action and rejects raw header insertion and removal. | A hardware NAT64 implementation remains unproven. Custom code needs a documented way to perform the missing conversion. |
| Stateful NAT66 | Linux TC and DPDK expose IPv6 address and TCP or UDP port rewriting. Software must manage the mappings and rules for each direction. | The installed firmware rejected virtual port allocation before rule installation. This setup does not establish hardware translation throughput. |
| Fixed checksum-neutral NPT66 mappings | Software can calculate complete address pairs according to [RFC 6296](https://www.rfc-editor.org/rfc/rfc6296.html) and request address rewrite rules. The public interfaces do not expose the complete prefix translation algorithm. | The fixed mapping prototype could not create the required virtual port interfaces. It did not test general hardware NPT66. |

A hardware result requires correct translated packets at the receiver and
increasing NIC rule counters while software forwarding remains disabled.
Assigning a VF, enabling checksum offload, or running DPDK does not prove
that the NIC performs translation.

### Translation test conditions

| Test condition | Recorded setup |
| --- | --- |
| Date | October 17, 2026 |
| OPNsense | `26.7.4_1`, FreeBSD `15.1-RELEASE-p3` |
| Guest CPU allocation | 8 vCPUs, or 50.00% of host logical CPU capacity |
| Guest memory | 8 GiB |
| Attachment | Direct PCI passthrough of spare VFs on `0000:19:00.3`; the peers use separate VFs on that physical function. |
| Physical network | The tests use the NIC's internal switch and do not exercise the external WAN or LAN links. |
| OPNsense driver | The updated upstream `if_bnxt` module from the routed comparison |
| OPNsense checksum offload and TCP segmentation offload | On for IPv4 and IPv6 |
| Large receive offload | Off |
| Receive-side scaling | On; 7 queue sets per OPNsense VF |
| Regular receive descriptors | 1024 per queue set, confirmed by boot output |
| Requested completion/data/aggregation descriptors | `8192,1024,1024`; the requested completion and aggregation counts do not prove their active allocation. |
| MTU | 1500 bytes |
| VPP software and compilers | Official VPP `26.06-release` core, compiled with Clang/LLVM `18.1.3`; custom DPDK plugin and DPDK `26.03`, compiled with GCC `13.2.0` |
| VPP driver correction | The packaged plugin submitted an empty firmware command header. The custom driver copies each command word with `memcpy` before writing it to the device register. |
| VPP queues | 4 receive queues and 4 transmit queues per VF, with 1024 descriptors per queue |
| VPP packet processing | Scalar receive code uses receive scatter. The vector configuration disables receive scatter with `no-multi-seg` and uses AVX2 instructions to process groups of packets on the processor. Both configurations use scalar transmit code and translate packets on the processor. |
| VPP configured offloads | IPv4, TCP, and UDP receive checksums; transmit checksums and TCP segmentation; receive-side scaling for IPv4 and IPv6 |
| VPP observed offloads | TCP captures of scalar NAT64 and AVX2 vector NAT66 and NPT66 confirm receive transport checksum validation and receive-side scaling hashes. Their transmitted packets do not request transmit checksum or TCP segmentation offload. The comparison does not include a packet trace for vector NAT64. |
| Traffic generator | Private `iperf3` 3.18 binary in the Linux peer network namespaces |
| Management | SSH and file transfers use IPv6. |
| PowerEdge management address | `3d06:bad:b01:300::254` |
| TCP throughput | The sender does not limit its rate. |
| UDP saturation | 8 uncapped streams from one client; the table records actual offered traffic and loss. |
| UDP interpretation | Saturation loss prevents the received rates from establishing a sustainable capacity with low loss. The tests do not isolate the location of every lost datagram. |
| Measured duration | 20 seconds after a 3-second warmup |
| Idle baseline | 5 seconds before each run |
| CPU sampling | 0.5-second host and cgroup intervals within the common client interval after warmup |
| CPU denominator | All 16 host logical CPUs; component CPU already contributes to total host CPU. |
| Configuration acceptance | Live rules, routes, addresses, queue settings, offloads, and configuration identity match before and after each accepted run. |
| Processing paths | Both NAT64 paths include PF outbound IPv4 NAT. Tayga runs inside OPNsense; VPP runs in a separate container between OPNsense routing stages. The OPNsense IPv6 cases use PF without a user-space translator; the external IPv6 cases use VPP between OPNsense routing stages. The comparison does not isolate the cost of address translation. |
| Client distribution | Parallel NAT64 and IPv6 cases use different client-address counts, as the result table records. |
| Production scope | PF uses minimal benchmark rules. The tests do not exercise the production MWAN classifier, VPN, or IPS processing. |
| Repetition | Each configuration and workload has one accepted run. The results do not establish measurement variance or a consistent ranking of receive modes. |

### VPP idle CPU

VPP polling workers repeatedly check receive queues during idle periods.
The baseline below precedes each configuration’s bidirectional TCP run.
Workload CPU includes the polling cost without subtracting this baseline.

| Configuration | OPNsense CPU, % of host | VPP container CPU, % of host | Total host CPU |
| --- | ---: | ---: | ---: |
| VPP NAT64 and PF, scalar receive | 1.50% | 21.44% | 27.36% |
| VPP NAT64 and PF, vector receive | 0.25% | 21.52% | 25.94% |
| VPP stateful NAT66, vector receive | 0.27% | 21.63% | 26.39% |
| VPP RFC 6296 NPT66, vector receive | 0.39% | 21.47% | 26.26% |

### Single TCP connection

| Configuration | Received, Gbit/s | OPNsense CPU, % of host | VPP container CPU, % of host | Total host CPU | Retransmissions |
| --- | ---: | ---: | ---: | ---: | ---: |
| Tayga NAT64 and PF outbound IPv4 NAT | 3.84 | 23.69% | Not used | 32.12% | 42,389 |
| VPP NAT64 and PF, scalar receive | 4.31 | 29.44% | 20.76% | 59.27% | 559 |
| VPP NAT64 and PF, vector receive | 3.55 | 19.72% | 21.08% | 49.27% | 343 |
| PF stateful NAT66 | 6.21 | 22.31% | Not used | 31.79% | 485 |
| VPP stateful NAT66, vector receive | 3.09 | 20.71% | 21.13% | 48.86% | 44 |
| PF BINAT prefix translation | 5.40 | 9.17% | Not used | 18.62% | 409 |
| VPP RFC 6296 NPT66, vector receive | 4.49 | 37.46% | 21.02% | 65.47% | 117 |

### Parallel TCP connections

| Configuration | Direction | Clients | Streams per direction | Forward, Gbit/s | Reverse, Gbit/s | OPNsense CPU, % of host | VPP container CPU, % of host | Total host CPU | Retransmissions |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Tayga NAT64 and PF | Forward | 8 | 32 | 3.12 | 0.00 | 48.79% | Not used | 63.42% | 93,327 |
| VPP NAT64 and PF, scalar receive | Forward | 8 | 32 | 9.64 | 0.00 | 44.71% | 20.34% | 81.26% | 44,398 |
| VPP NAT64 and PF, vector receive | Forward | 8 | 32 | 9.83 | 0.00 | 44.25% | 20.48% | 80.76% | 43,866 |
| Tayga NAT64 and PF | Reverse | 8 | 32 | 0.00 | 2.85 | 49.44% | Not used | 63.38% | 89,255 |
| VPP NAT64 and PF, scalar receive | Reverse | 8 | 32 | 0.00 | 7.96 | 42.44% | 20.59% | 73.67% | 48,321 |
| VPP NAT64 and PF, vector receive | Reverse | 8 | 32 | 0.00 | 7.65 | 43.70% | 20.65% | 74.54% | 33,125 |
| Tayga NAT64 and PF | Bidirectional | 8 | 32 | 1.48 | 1.62 | 49.75% | Not used | 64.18% | 120,867 |
| VPP NAT64 and PF, scalar receive | Bidirectional | 8 | 32 | 4.75 | 4.90 | 46.49% | 20.36% | 81.32% | 76,352 |
| VPP NAT64 and PF, vector receive | Bidirectional | 8 | 32 | 5.11 | 4.58 | 46.53% | 20.18% | 82.17% | 76,917 |
| PF stateful NAT66 | Forward | 1 | 32 | 20.02 | 0.00 | 44.26% | Not used | 64.62% | 52,300 |
| VPP stateful NAT66, vector receive | Forward | 1 | 32 | 11.22 | 0.00 | 45.50% | 20.51% | 79.04% | 37,935 |
| PF stateful NAT66 | Reverse | 1 | 32 | 0.00 | 21.01 | 46.89% | Not used | 68.40% | 52,968 |
| VPP stateful NAT66, vector receive | Reverse | 1 | 32 | 0.00 | 9.25 | 46.53% | 20.56% | 78.78% | 37,190 |
| PF stateful NAT66 | Bidirectional | 1 | 32 | 11.45 | 9.30 | 48.61% | Not used | 71.53% | 81,424 |
| VPP stateful NAT66, vector receive | Bidirectional | 1 | 32 | 5.13 | 5.67 | 47.25% | 20.46% | 81.50% | 76,895 |
| PF BINAT prefix translation | Forward | 1 | 32 | 20.38 | 0.00 | 47.30% | Not used | 67.58% | 44,090 |
| VPP RFC 6296 NPT66, vector receive | Forward | 1 | 32 | 11.03 | 0.00 | 45.21% | 20.55% | 78.42% | 46,184 |
| PF BINAT prefix translation | Reverse | 1 | 32 | 0.00 | 19.37 | 46.15% | Not used | 65.71% | 60,087 |
| VPP RFC 6296 NPT66, vector receive | Reverse | 1 | 32 | 0.00 | 9.99 | 46.44% | 20.52% | 79.15% | 33,116 |
| PF BINAT prefix translation | Bidirectional | 1 | 32 | 10.64 | 9.91 | 47.85% | Not used | 70.69% | 68,746 |
| VPP RFC 6296 NPT66, vector receive | Bidirectional | 1 | 32 | 5.75 | 4.96 | 47.18% | 20.47% | 81.20% | 65,561 |

### UDP at a fixed offered rate

| Configuration | Clients | Streams | Payload, bytes | Target total, Gbit/s | Actual sent, Gbit/s | Received, Gbit/s | Datagram loss | OPNsense CPU, % of host | VPP container CPU, % of host | Total host CPU |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Tayga NAT64 and PF | 8 | 8 | 1400 | 1.00 | 1.00 | 1.00 | 0.0173% | 47.31% | Not used | 61.77% |
| VPP NAT64 and PF, scalar receive | 8 | 8 | 1400 | 1.00 | 1.00 | 0.97 | 3.1777% | 45.46% | 20.85% | 77.19% |
| VPP NAT64 and PF, vector receive | 8 | 8 | 1400 | 1.00 | 1.00 | 0.94 | 6.1586% | 48.05% | 20.96% | 79.42% |

### UDP saturation

| Configuration | Payload, bytes | Actual sent, Gbit/s | Received, Gbit/s | Datagram loss | OPNsense CPU, % of host | VPP container CPU, % of host | Total host CPU |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Tayga NAT64 and PF | 1400 | 26.24 | 2.23 | 91.4149% | 32.78% | Not used | 93.96% |
| VPP NAT64 and PF, scalar receive | 1400 | 23.08 | 0.87 | 96.2500% | 30.48% | 18.69% | 99.65% |
| VPP NAT64 and PF, vector receive | 1400 | 21.45 | 1.70 | 92.0589% | 33.04% | 18.39% | 99.75% |
| PF stateful NAT66 | 1400 | 27.26 | 1.94 | 92.8632% | 37.14% | Not used | 96.71% |
| VPP stateful NAT66, vector receive | 1400 | 21.85 | 1.56 | 93.0689% | 33.70% | 18.30% | 99.88% |
| PF BINAT prefix translation | 1400 | 29.19 | 1.63 | 94.4289% | 25.29% | Not used | 86.33% |
| VPP RFC 6296 NPT66, vector receive | 1400 | 23.47 | 0.42 | 98.1824% | 29.50% | 19.75% | 99.81% |
| Tayga NAT64 and PF | 64 | 1.30 | 0.011 | 99.1767% | 43.37% | Not used | 97.89% |
| VPP NAT64 and PF, scalar receive | 64 | 1.08 | 0.051 | 95.2808% | 32.72% | 19.09% | 99.82% |
| VPP NAT64 and PF, vector receive | 64 | 1.08 | 0.107 | 90.0500% | 31.68% | 18.92% | 99.90% |
| PF stateful NAT66 | 64 | 1.39 | 0.125 | 90.9922% | 25.37% | Not used | 89.43% |
| VPP stateful NAT66, vector receive | 64 | 1.16 | 0.037 | 96.7810% | 28.64% | 19.49% | 99.86% |
| PF BINAT prefix translation | 64 | 1.27 | 0.219 | 82.7979% | 36.11% | Not used | 97.86% |
| VPP RFC 6296 NPT66, vector receive | 64 | 1.07 | 0.046 | 95.7479% | 32.73% | 19.39% | 99.87% |

### Meaning for address translation

| Use case | Supported conclusion |
| --- | --- |
| Parallel NAT64 TCP | The external VPP configuration delivered more throughput than Tayga and used more total host CPU in each direction. |
| Stateful NAT66 | Direct PF delivered more TCP throughput and used less total host CPU than OPNsense routing through VPP. The measured results favor keeping this translation inside OPNsense. |
| IPv6 prefix translation | PF BINAT delivered more TCP throughput and used less total host CPU than the external VPP RFC 6296 NPT66 configuration. |
| Fixed-rate NAT64 UDP | Tayga lost less traffic and used less total host CPU than either VPP configuration. |
| NAT64 receive mode | Vector reception delivered more saturated UDP traffic than scalar reception. Scalar reception delivered more traffic during the single TCP connection and fixed-rate UDP tests. |
| Hardware translation | The firmware rejection prevents using the tested hardware rewrite path on the installed setup. |

### Hardware test acceptance

| Check | Observed result |
| --- | --- |
| Switchdev request | The kernel returned `Link has been severed`. |
| Firmware command | `HWRM_CFA_VFR_ALLOC`, request `0xfd` |
| Firmware response | `HWRM_ERR_CODE_CMD_NOT_SUPPORTED`, error `0xffff` |
| Physical function after the request | `0000:19:00.3` remained in `legacy` mode. |
| Rewrite rules installed | 0 |
| Hardware translation throughput | The firmware rejection prevented rule installation and measurement. |

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
