# Gateway firewall ownership

The gateway daemon installs and maintains its firewall rules. Configs deploys
the network configuration and released binary. The gateway no longer loads a
separate ruleset file through `nftables.service`.

This specification defines the required behavior. The
[implementation plan](plans/2026-09-26-mwan-341-firewall.md) defines the code
changes, tests, and deployment sequence.

## Current behavior

The gateway firewall module installs filtering, IPv4 address translation,
packet marking, and destination-set definitions. The translation module
creates its IPv6 NAT chains and installs edge-address exceptions. Its kernel
packet program translates IPv6 prefixes. The steering module maintains its
own table.

The gateway daemon starts before device discovery and the network manager. It
installs and inspects protective rules before writing network configuration.
The gateway service reports readiness after that inspection and the required
network-file writes. Working ISP links are not required for readiness.

## Rule ownership

Each table has one rule writer within the gateway daemon:

| Component | Responsibility |
| --- | --- |
| The firewall module | It maintains input and forwarding policy, IPv4 address translation, packet marking, and destination-set definitions. |
| The translation module | It maintains IPv6 edge-address exceptions and the existing kernel prefix translator. |
| The steering module | It assigns providers to connections and excludes paths that cannot serve an address family. |

Each component creates the tables and chains it needs. It replaces only its
own rules. The modules share typed translation configuration without combining
their tables.

The destination refresher continues to update the addresses in the configured
destination sets. The daemon creates missing sets and preserves their
existing elements during reconciliation. Set definitions retain support for
address ranges and automatic merging of overlapping ranges.

The gateway role enables firewall ownership. The failover container and
hypervisor roles retain their existing firewall arrangements.

## Startup and failure behavior

The daemon installs protective rules before it writes or reloads network
configuration. These rules allow management access, loopback, established
connections, address acquisition, and internal routing messages. Unmatched
input and forwarded traffic are dropped. The existing outbound policy is
preserved.

The protective rules depend only on validated local configuration. They must
not wait for an interface to appear, a DHCP lease, a routing session, a
tunnel, or the management datastore.

The daemon first validates the configuration needed for those rules. It
then installs them and validates the remaining configuration before applying
the full policy. If that remaining configuration is invalid, startup fails
with the protective rules still installed. Invalid JSON or invalid management
settings cannot supply trustworthy protective rules; that case leaves the
existing kernel rules unchanged and reports the configuration error.

Service readiness requires successful application and inspection of the
protective rules and completion of the required link-file writes. It does
not require working ISP connections. The current ordering before device
discovery and the network manager remains in effect.

Kernel modules required by the firewall load before the daemon. The daemon
retains its existing restriction against loading kernel modules itself.

Stopping the daemon preserves its installed rules. If the binary cannot run,
it cannot install protective rules. The deployment gate and snapshot rollback
remain the recovery for that failure.

## Rule updates and repair

Each rule writer applies a complete update to its chains in one kernel
transaction. Ordinary reconciliation creates missing structures without
deleting existing tables or clearing entire tables.

A change to a chain's hook, priority, or type requires an explicit migration.
An existing, tested atomic migration may replace that owned chain. Otherwise
the update fails without partially changing the rules and reports the
incompatible chain and required action.

Deleting an owned table or chain requests reconciliation. The daemon recreates
the missing structures and their rules without restarting. Deleting an
individual rule is repaired by periodic reconciliation. Rule replacement and
destination-set refreshes must not trigger a continuous reconciliation loop.

The destination refresher starts after the daemon creates its sets and retries
failed refreshes. After a complete ruleset deletion, the daemon recreates the
set definitions and the refresher repopulates their contents. Acceptance
checks set recovery separately from rule recovery.

Drift detection watches the authoritative daemon configuration instead of the
retired ruleset file. Reconciliation replaces temporary manual rule changes.
A firewall change persists after deployment of the updated network
configuration.

## Packet behavior

The cutover preserves the existing providers' filtering, inbound access,
translation, and connection affinity. Static IPv4 mappings retain precedence
over masquerade. Native routing preserves addresses. IPv6 edge exceptions,
including the published edge address, retain their existing behavior.

The existing translation policy also governs physical and logical interfaces.
The [translation specification](superpowers/wanconfig/translation.md) defines that policy and its
packet requirements. Firewall ownership preserves the translator's kernel
attachments and packet metadata, including internal clients communicating
through their external IPv6 addresses.

Steering continues to consume the shared result for each connection and
address family. The firewall does not create a second health decision.

## Extension for MWAN-507

The [shared routing model](superpowers/wanconfig/model.md) defines direct BGP and tunnel arrangements.
BGP exchanges route announcements between routers. A tunnel encapsulates
ordinary packets between endpoints.

The firewall accepts typed connection data for permitted local protocol
traffic and forwarded traffic. A connection's identity remains independent
of its ISP name or autonomous system number. Rules match configured
interfaces, addresses, protocols, and ports.

MWAN-507 adds peer and tunnel configuration to those inputs. It must preserve
the table ownership, startup sequence, translation modes, and shared
eligibility decision established by MWAN-341. Its changes must not require
another firewall cutover or a replacement translation implementation.

Peer permissions restrict BGP traffic to the configured interface and peer.
Tunnel permissions restrict the outer packets to the configured physical
connection, endpoints, and protocol. Ordinary IPv6 forwarding through a
tunnel has its own permission. The address family of the outer packet does
not select the address family of the enclosed packet.

Protocol permissions exist before a session or tunnel becomes ready.
Readiness controls traffic selection through the shared routing and steering
state. Loss of a later IPv6 path must leave working ordinary IPv4 available.

MWAN-341 does not select a tunnel protocol or implement external BGP.
MWAN-507 supplies those implementations and their integrated acceptance tests.
The firewall design must support them without requiring production peer
addresses, prefixes, or circuit details during development.

## Acceptance

MWAN-354 strengthens the deployment gate before the ownership cutover.
Success requires both configured address families and consecutive successful
probe rounds. A testbed IPv4 translation failure must fail the gate even
when IPv6 works.

MWAN-355 validates generated rules before deployment using the production
writers in an isolated Linux network namespace. Comparison includes rule
order, chain properties, and set definitions. It excludes changing kernel
handles, counters, and refresher-owned set contents. Real packet checks
verify filtering and translation in addition to the ruleset comparison.

Testbed acceptance must demonstrate startup protection, management access,
reboot, daemon restart, invalid configuration, full ruleset repair,
destination-set recovery, and preservation of the current packet behavior.
Downstream clients must demonstrate both address families, balancing,
provider fallback, inbound replies, and the existing translation modes.

Production acceptance uses the same released binary after testbed acceptance.
It compares intended rules with the kernel and verifies downstream traffic
after reboot. The ruleset file is absent and its loading service is masked.

MWAN-507 later verifies direct BGP, tunnels with configured routes, and BGP
over tunnels against this firewall. Its tests include packet-size errors and
independent IPv4 operation. New-circuit production acceptance remains after
October 2026.
