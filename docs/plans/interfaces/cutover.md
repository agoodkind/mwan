# Verify exclusive interface ownership

Use isolated Linux interfaces and the production WAN daemon to verify that release
leaves one writer per object. Apply the
[interface ownership specification](../../interfaces.md) and use the
[connection release systemd test](../../../gateway/internal/commandline/deploygate_release_runtime_test.go)
to check the release boundary.

## Check release and reacquisition

1. Record the current owner, link identity, addresses, routes, and protocol clients.
   Verify downstream IPv4 and IPv6 traffic for the configured families.
2. Exclude the selected connection from new traffic. Stop its current writer and
   protocol clients. Inspect the kernel state and active clients to confirm release.
   Preserve shared parents and unrelated interfaces.
3. Start acquisition with the replacement owner. Preserve DHCP identifiers and observe
   new lease negotiation. Check addresses, routes, lifetimes, translation, readiness,
   and packets before restoring selection.
4. Repeat with the owners reversed. For an owned VLAN that disappears during release,
   verify its recreation and confirm the replacement link identity before checking
   packets. Check continuity on links that remain present.
5. Restart the service and check saved assignment recovery separately from fresh
   acquisition. Record any interruption or renumbering.

## Check dependent networking

Run the link, acquisition, and packet checks with at least one other usable provider.
Verify other provider traffic during release and reacquisition. Check internal BGP,
transit return routes, inbound mapping replies, and management separately. Delete an
owned return route during traffic and verify repair.

When checking startup without networkd, confirm that every required test interface has
an owner that can configure it without networkd. Check that required clients start,
unrelated writers remain inactive, and the daemon applies the firewall before network
operations that depend on it.
