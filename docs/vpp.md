# Router VPP configuration

The `router` interface manager role configures a separate VPP process through
its local binary API. The `wan` role continues to manage provider selection
and Linux forwarding. Enabling the router role does not enable VPP in the WAN
role.

`mwan install --role router --apply` installs the router service and validation
schema. It does not install the WAN services or import a sysrepo datastore.

MWAN uses the official GoVPP bindings for VPP 26.06. Every connection checks
the reported version and the required request and reply message CRCs before
changing VPP state. A socket or compatibility failure records a reconciliation
error and permits the interface manager to retry on its next interval.

## Network policy

The normal network document defines VPP and host TAP addresses through
`ietf-interfaces:interfaces`. Each interface requires an explicit connection
ID and uses `external` ownership at the Linux interface boundary. The VPP
module applies their settings through the local binary API.

The `goodkind-mwan-vpp:vpp` container defines the router's translation and
forwarding policy. The [router network example](../gateway/internal/networkjson/testdata/vpp-router.json)
contains a complete document.

The policy requires these fields:

| Field | Required behavior |
|---|---|
| `nat64/inside-interface` and `nat64/outside-interface` | Select distinct declared VPP interfaces. |
| `nat64/prefix` | Select a canonical IPv6 translation prefix with an RFC 6052 prefix length. |
| `nat64/pool-address` | Provide unique unicast IPv4 addresses for dynamic translation. |
| `nat64/table-id` | Select VPP forwarding table `0` for the prefix. |
| `nat64/pool-table-id` | Select table `0`, or `4294967295` for all tables. |
| `route` | Configure a destination, next hop, VPP interface, and table. |
| `neighbor` | Configure a static IP address and Ethernet MAC on a VPP interface. |

VPP table IDs use a separate namespace from Linux routing table IDs. The
existing Linux route model retains its main table restriction. The VPP module
uses table `0` and rejects other route tables before changing runtime state.

## Process resources

The existing TOML configuration supplies sockets, ownership state, and TAP
resources. A TAP resource maps the VPP connection to its Linux host connection.
The VPP interface name must match `tap` followed by its configured ID.

```toml
[ifmgr]
role = "router"
reconcile_interval = "30s"

[ifmgr.iface.lo]
name = "lo"

[ifmgr.modules.vpp]
api_socket = "/run/vpp/api.sock"
stats_socket = "/run/vpp/stats.sock"
expected_version = "26.06"
request_timeout = "5s"
state_file = "/var/lib/mwan/vpp.json"
profile = false
profile_queue_polling = false

[[ifmgr.modules.vpp.interface]]
connection_id = "router-vpp-inside"
host_connection_id = "router-host-inside"
id = 64
rx_ring_size = 256
tx_ring_size = 256
rx_queues = 1
tx_queues = 1
worker = 0
rx_mode = "adaptive"

[[ifmgr.modules.vpp.interface]]
connection_id = "router-vpp-outside"
host_connection_id = "router-host-outside"
id = 65
rx_ring_size = 256
tx_ring_size = 256
rx_queues = 1
tx_queues = 1
worker = 0
rx_mode = "adaptive"

[ifmgr.modules.vpp.nat64]
bib_buckets = 1024
bib_memory_size = 134217728
st_buckets = 2048
st_memory_size = 268435456
```

`worker` uses VPP's worker index. `rx_mode` accepts `polling`, `interrupt`, or
`adaptive`. Ring sizes and queue counts must be positive. The NAT64 table
resource section is required when the network document configures NAT64.

MWAN tags each created TAP with its owner and creation ID. MWAN records a
pending ownership operation before each resource write and resolves
interrupted writes through typed API readback. Cleanup verifies the TAP tag and
deletes only recorded resources. An existing untagged TAP with a requested
name causes an ownership error. MWAN does not adopt it implicitly.

VPP starts and stops independently. Process startup configuration controls
worker allocation, memory, buffers, enabled plugins, and idle wait. VPP 26.06
does not expose an idle wait setting through its binary API.

## Measurements

The existing report collector reads VPP interface counters from shared memory
through the configured statistics socket. Saved intervals and report tables
distinguish measured zero traffic from unavailable counters. A counter reset,
changed interface index or creation ID, or replaced statistics socket invalidates that
interval.

Setting `profile = true` also requires an absolute `profile_executable` and a
positive `profile_duration` no longer than the reporting sample interval.
Profiling uses Linux `perf_event_open`, the kernel interface for performance
events, and VPP's typed thread API. It records native CPU clock measurements,
instruction samples, ELF symbols, lost samples, and per-thread availability.
The host must permit the performance event operation.

Recognized NAT64 packet functions identify sampled packet work. Receive
functions process both packets and empty polls. Their aggregate clocks and
symbol samples cannot establish exact empty polling CPU time.

The optional local VPP queue accounting patch adds separate counters for strict
empty receive checks, nonempty receive processing and buffer refill. Setting
`profile_queue_polling = true` requires `profile = true` and the matching
extension API. MWAN acquires a session, requests an immediate baseline, and
stops the session before reading the final shared memory snapshot. These
snapshots do not depend on VPP's normal statistics update interval. Cleanup
releases only the owned session. VPP also releases the session when the API
client disconnects. A missing extension, changed queue topology,
stale snapshot or counter reset makes the measurement unavailable.

Each queue measurement retains its thread ID, interface index, queue ID,
cumulative call and tick counts, and that thread's seconds per tick. Profiling
adds clock reads and counter updates. Elapsed receive intervals include
preemption. They do not measure consumed CPU time
or establish an exact CPU split. Reports display these intervals separately
from the Linux CPU clock. The queue interval also includes the short API
operations before and after instruction sampling; its own duration records
that interval.
