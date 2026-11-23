# Verify owned links, addresses, and route repair

Use the production WAN daemon with the
[interface ownership specification](../../interfaces.md). Compare configuration and
operational state with actual kernel objects and downstream packets.

## Check owned links

Use the
[owned link daemon test](../../../gateway/internal/commandline/ifmgr_owned_links_netns_test.go)
with connected namespaces.

1. Start with one provider absent. Verify packets through another provider, then create
   the missing device and verify recovery.
2. Configure multiple VLANs on a shared parent. Check the device type, tag, name,
   configured MAC and MTU, and packet delivery. Do not infer a bridge from an interface
   name.
3. Remove one child and restart. Verify that the parent and other child remain usable.
   Present an ambiguous or incompatible device and verify that the daemon preserves it.
4. Compare observed identity with the link journal before accepting a recreated device.
   Match an owned physical link by permanent MAC. For a virtual link, compare its alias
   and recorded identity. Check that a reused kernel index cannot establish ownership.
   Keep the journal configured until the daemon prunes the last owned link.
5. Submit invalid configuration and verify that startup leaves protective firewall
   policy in place without writing networkd units or changing links. Submit invalid
   firewall policy and verify that the prior rules remain intact.

## Check static and mapped addresses

Use the
[static address daemon test](../../../gateway/internal/commandline/ifmgr_owned_static_netns_test.go)
and the [mapped address daemon test](../../../gateway/internal/commandline/ifmgr_owned_mapped_netns_test.go).

1. Install static IPv4 and IPv6 addresses and a configured main table route. Verify
   packet delivery and preserve foreign addresses, kernel SLAAC addresses, and connected
   routes. Check that removing an owned IPv4 primary address preserves a foreign
   secondary address on the same subnet.
2. Remove an owned address and change only the route metric. Verify the journal and
   kernel changes. Restart with a changed address or gateway and check that unrelated
   objects remain.
3. Remove a required device during an apply operation. Check that operational state
   reports the kernel failure separately from assignment validity. Recreate the device
   and verify packet delivery.
4. Test an on-link mapped address and a routed mapping. Verify downstream replies and
   one writer per address. Check that a connection with a different configured owner
   retains its own acquisition and address writer.

## Check route and rule repair

Use a reconciliation interval long enough to distinguish repair triggered by an event
from periodic repair. Delete an owned return route during traffic. Check that the daemon
repairs the route and restores replies before the next periodic reconcile. Repeat with a
provider default and a policy rule without generating a link event.

Keep a route with the same destination but a different table or protocol. Verify that
repair preserves it. Delete several owned objects together and check that reconciliation
settles after repair. Withdraw an assignment before deleting its old route and verify
that the daemon does not restore obsolete policy.
