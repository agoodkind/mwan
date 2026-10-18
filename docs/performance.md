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

The upload measurements compare CPU use for routing and NAT inside the router
and gateway guests with the host CPU used to copy packets between those guests.

Test measurements:

| Measurement | Result |
| --- | ---: |
| Download | 693 Mbit/s |
| Upload | 1782 Mbit/s |
| Recorded host CPU capacity | 12 cores |
| Copy threads' share of combined guest work at upload peak | About 15% |

The CPU breakdown distinguishes guest processing from host packet copying.

Host CPU use:

| CPU use, % of entire host | Before test | Upload peak |
| --- | ---: | ---: |
| Router guest copy thread | 0.08% | 4.92% |
| Gateway guest copy thread | 0.08% | 4.33% |
| Both copy threads | 0.17% | 9.25% |
| Router guest forwarding | Not measured | 37.83% |
| Gateway guest forwarding | Not measured | 16.75% |
| Both guests forwarding | Not measured | 54.58% |
| Forwarding and copy threads | Not measured | 63.83% |

## Vault and PowerEdge translation performance

The comparison evaluates PowerEdge translation options against Vault's
installed OPNsense router under the recorded workload and CPU allocations.

Test conditions:

| Test condition | Value |
| --- | --- |
| Host CPU capacity | Vault: 12 logical CPUs; PowerEdge: 16 |
| OPNsense VM allocation | Vault: 33.33% of host CPU capacity; PowerEdge: 50.00% |
| Traffic | 8 clients; TCP: 4 streams each, uncapped; UDP: 1 stream each, 1400-byte payload, 5 Gbit/s per direction |
| Measured duration | 20 seconds; TCP excludes 3 seconds of warmup |
| Network | Vault uses its physical LAN; PowerEdge uses the NIC internal switch. |
| DPDK driver | The test build includes the [submitted BNXT firmware-command fix](https://patches.dpdk.org/project/dpdk/patch/20261018022929.91865-1-alex@goodkind.io/). |
| PowerEdge software settings | Earlier measurements: TruFlow disabled, VF MSI-X capacity 8; the enabled rerun is in progress. |
| NIC test settings | TruFlow enabled; VF MSI-X capacity 16; OPNsense uses 8 queues. |
| Hardware scope | Fixed address mappings, with a separate source address per client; PF NAT66 combines sources. The tested driver rejects NAT64. |

### TCP

TCP tests compare transfer capacity without a sender rate limit.

TCP throughput and CPU use:

| Setup / translation | Received Gbit/s, forward / reverse | Routing / translation CPU | Total host CPU |
| --- | --- | --- | --- |
| NAT64: Vault / Tayga | 0.93 / 0.35 | 36.90% | 75.69% |
| NAT64: PowerEdge / Tayga | 1.41 / 1.51 | 49.75% | 63.35% |
| NAT64: PowerEdge / VPP | 5.24 / 4.90 | 65.80% | 80.68% |
| NAT66: Vault / PF | 2.35 / 0.28 | 28.97% | 71.33% |
| NAT66: PowerEdge / PF | 10.65 / 10.48 | 49.12% | 73.94% |
| NAT66: PowerEdge / VPP | 5.49 / 5.41 | 67.79% | 81.77% |
| Fixed NAT66: PowerEdge NIC + OPNsense | 6.27 / 5.92 | 48.54% | 65.28% |
| Fixed NAT66: PowerEdge NIC directly | 31.81 / 36.97 | 0.0065% | 96.25% |
| NPT: Vault / PF BINAT | 2.33 / 0.22 | 28.95% | 72.02% |
| NPT: PowerEdge / PF BINAT | 10.53 / 12.44 | 48.61% | 74.38% |
| NPT: PowerEdge / VPP, RFC 6296 | 6.18 / 4.95 | 67.78% | 81.95% |
| Fixed NPT: PowerEdge NIC + OPNsense | 6.44 / 5.74 | 48.39% | 65.08% |

### UDP

UDP tests compare packet delivery under the same requested load.

UDP throughput, loss, and CPU use:

| Setup / translation | Sent Gbit/s | Received Gbit/s | Delivery loss | Routing / translation CPU | Total host CPU |
| --- | --- | --- | --- | --- | --- |
| NAT64: Vault / Tayga | Test failed | Test failed | Test failed | Not measured | Not measured |
| NAT64: PowerEdge / Tayga | 5.00 / 5.00 | 1.53 / 1.59 | 68.91% / 68.24% | 49.08% | 87.03% |
| NAT64: PowerEdge / VPP | 5.00 / 5.00 | 0.75 / 0.96 | 85.08% / 80.72% | 67.90% | 96.02% |
| NAT66: Vault / PF | 2.53 / 5.00 | 1.72 / 1.06 | 31.15% / 78.78% | 30.64% | 91.57% |
| NAT66: PowerEdge / PF | 5.00 / 5.00 | 4.50 / 2.59 | 9.66% / 48.17% | 46.11% | 89.80% |
| NAT66: PowerEdge / VPP | 5.00 / 5.00 | 2.81 / 1.52 | 43.64% / 69.56% | 59.99% | 92.97% |
| Fixed NAT66: PowerEdge NIC + OPNsense | 5.00 / 5.00 | 3.70 / 3.60 | 25.96% / 27.96% | 48.70% | 91.50% |
| Fixed NAT66: PowerEdge NIC directly | 5.00 / 5.00 | 4.99 / 4.99 | 0.13% / 0.16% | 0.0051% | 56.10% |
| NPT: Vault / PF BINAT | 2.35 / 5.00 | 1.27 / 1.17 | 45.50% / 76.66% | 27.96% | 96.32% |
| NPT: PowerEdge / PF BINAT | 5.00 / 5.00 | 4.58 / 4.60 | 8.37% / 7.91% | 47.88% | 93.73% |
| NPT: PowerEdge / VPP, RFC 6296 | 5.00 / 5.00 | 2.49 / 2.62 | 50.22% / 47.53% | 63.63% | 96.84% |
| Fixed NPT: PowerEdge NIC + OPNsense | 5.00 / 5.00 | 2.95 / 3.24 | 40.91% / 35.17% | 48.22% | 89.21% |

Rate pairs list simultaneous traffic from client to server and from server to
client, in that order. Routing/translation CPU combines OPNsense with its
external translator or NIC rule controller, except that the direct NIC test
counts only its controller.

Delivery loss includes sent packets that the receiver did not record.

The direct NIC test does not execute OPNsense or manage connections.

PF BINAT replaces the prefix and updates checksums, while VPP applies the
different [RFC 6296 address calculation](https://www.rfc-editor.org/rfc/rfc6296.html).

```mermaid
flowchart LR
  client[Client] --> ingress[OPNsense]
  ingress --> local[Tayga or PF]
  local --> server[Server]
  ingress --> external[VPP or NIC rewrite]
  external --> egress[OPNsense]
  egress --> server
  client --> direct[Direct NIC rewrite]
  direct --> server
```

## Remaining investigation

MWAN-527 tracks the forwarding comparison with a software bridge.
