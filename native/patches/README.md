# Required DPDK patches

The Rust native worker requires DPDK 26.03 and the
[reviewed patch set](dpdk-26.03.patch). The build consumes an existing SDK
selected by `MWAN_NATIVE_DPDK_PREFIX`. Prepare the SDK and apply its patches
manually for each lab architecture. The build does not download DPDK or
apply patches. Replacing the custom worker with Rust does not replace the
DPDK library or its Broadcom driver.

The patch set provides these native-worker requirements:

- BNXT kernel-owned representors and explicit KERNEL and DIRECT_DATA roles.
- Controller-owned native group-zero miss actions and dependency checks.
- Shared RX profile-function lookup for direct DATA forwarding.
- Counter validity, freshness, allocation, and cleanup corrections.
- Explicit TCP flag masks when the flag value is zero.
- Checked BNXT ring completion before packet memory release, retained ownership
  after cleanup failure, and ethdev retention when a driver's close fails.

The source artifacts have these identities:

| Artifact | SHA256 |
| --- | --- |
| Official DPDK 26.03 archive | `84989202bbe0fbaeeb62f8fdc63f691880ac0b66cd0dc64c167843c59db29d2c` |
| Required patch set | `a41559acfdff7f47663828ccb645ba8f4379a8c04202f8c3f982b46a8c259487` |

The patch set also contains shared Router translation work. The native worker
does not configure NAT64. VF-source interception requires a separate SOURCE_TX
extension and hardware acceptance; the current worker rejects that origin.
