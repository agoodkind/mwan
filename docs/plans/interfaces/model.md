# Verify interface configuration and operational state

Use the [interface ownership specification](../../interfaces.md) to check how the WAN
daemon validates configuration, identifies kernel devices, and reports runtime state.

## Verify the network document

Use the production loader and libyang. From the repository root, validate the
[minimal network example](../../../gateway/yang/instances/network-min.json) with the
[current YANG models](../../../gateway/internal/yangpub/schema).

```sh
mwan debug check-network gateway/yang/instances/network-min.json gateway/internal/yangpub/schema
```

1. Check physical parents, providers, internal links, and management links. Verify that
   a connection ID stays distinct from a provider name and ASN.
2. Submit conflicting owners, missing or cyclic dependencies, invalid identities, and
   unsupported settings. Verify that startup installs the protective firewall before
   full configuration validation. Verify rejection before the daemon writes networkd
   units or changes links, addresses, and routes. Check that the loader can reject one
   provider while accepting another.
3. Verify shared routing table and rule priority checks. Test generic VLANs, a routed
   IPv6 prefix without delegation, and a provider configured for IPv4 only.
4. Check that the loader distinguishes a local address from an address that
   intentionally forwards traffic. Verify that route, translation, and resolver settings
   retain their configured meaning.

## Verify observed device state

Use the [kernel monitor tests](../../../gateway/internal/netif/monitor_netns_test.go) to
check identity and state.

1. Start with the configured device absent. Change an unrelated device and verify that
   the monitor does not attribute its events to the absent device.
2. Create the intended device, exchange packets, then delete and recreate it. Check the
   observed identity and index after recreation. Repeat with rename or index reuse.
3. Send router advertisements and exercise tentative addresses, deprecation, and expiry.
   Compare the observed flags and lifetimes with kernel state. Verify that the observer
   does not change those values.
4. Create routes with the same destination in different tables or with different
   protocols. Delete one and compare the event with the deleted route. Verify that a
   fresh snapshot reflects state after missed events.

## Verify operational reads

Use the [private datastore test](../../../gateway/internal/commandline/wanconfig_selftest_test.go)
and the [operational ownership test](../../../gateway/internal/commandline/wanconfig_ownership_netns_test.go).
Read through a separate sysrepo connection and compare configured ownership,
assignments, deadlines, kernel state, apply results, and forwarding readiness.

Remove a required link during an apply operation. Check the recorded failure and
dependency. Restore the link, verify packets, and check the reported recovery. Expire an
IPv6 assignment while IPv4 traffic succeeds. Check that the operational data retains the
distinction between the two address families and between acquisition, installation, and
forwarding.
