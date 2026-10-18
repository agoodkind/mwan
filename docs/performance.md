# Vault and PowerEdge translation performance

PowerEdge PF delivered the strongest software results for NAT66 and OPNsense prefix translation. External VPP improved concurrent NAT64 TCP; Tayga lost fewer UDP packets at the tested load.

Rate pairs show client to server / server to client simultaneously. Every CPU percentage uses the entire host: routing/translation CPU counts OPNsense and its translator or NIC controller, while total host CPU also includes traffic generation.

| Test condition | Value |
| --- | --- |
| Host CPU capacity | Vault: 12 logical CPUs; PowerEdge: 16 |
| OPNsense VM allocation | Vault: 33.33% of host CPU capacity; PowerEdge: 50.00% |
| Traffic | 8 clients; TCP: 4 streams each, uncapped; UDP: 1 stream each, 1400-byte payload, 5 Gbit/s per direction |
| Measured duration | 20 seconds; TCP excludes 3 seconds of warmup |
| Network | Vault uses its physical LAN; PowerEdge uses the NIC internal switch. |
| PowerEdge software settings | Earlier measurements: TruFlow disabled, VF MSI-X capacity 8; the enabled rerun is in progress. |
| NIC test settings | TruFlow enabled; VF MSI-X capacity 16; OPNsense uses 8 queues. |
| Hardware scope | Fixed address mappings, with a separate source address per client; PF NAT66 combines sources. The tested driver rejects NAT64. |

## TCP

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

## UDP

A failed test does not establish throughput; loss counts sent packets that the receiver did not record. The direct NIC test bypasses OPNsense and measures fixed rewrites, without dynamic connection management.

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

The direct result does not establish the NIC maximum because the host also runs both traffic generators. PF BINAT changes the prefix and updates checksums; VPP applies the different RFC 6296 address calculation, and the NIC test uses precomputed address pairs.

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
