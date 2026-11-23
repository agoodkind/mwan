# Verify interface address acquisition

Use the production WAN daemon, connected Linux namespaces, and real DHCP and router
advertisement servers. Compare protocol packets and server replies with the gateway's
operational state and kernel state. Send downstream packets to verify forwarding
separately from lease validity.

## Check DHCPv4 acquisition and renewal

Use the
[DHCPv4 daemon tests](../../../gateway/internal/commandline/dhcpv4_netns_test.go) for
initial acquisition, classless routes, rebinding, and server rejection.

1. Check the client identifier on the request. Compare the address, gateway, optional
   routes, and lease deadlines with the server response.
2. Stop replies from the original server at renewal time. Verify that the valid address,
   route, and packet delivery persist while the client waits to rebind. Check the new
   deadlines after a valid reply.
3. Exercise a changed address, a changed gateway with the same address, rejection, and
   expiry. Verify that the gateway removes obsolete owned addresses and routes and
   preserves unrelated state.
4. Recreate the interface and verify that only the current client can update its
   assignments.

## Check DHCPv6 interface addresses and delegation

Use the
[DHCPv6 address tests](../../../gateway/internal/commandline/dhcpv6ia_netns_test.go) and
the [delegation tests](../../../gateway/internal/commandline/dhcpv6pd_netns_test.go) for
real server exchanges.

1. Test address requests, prefix requests, and both together. Check that one client uses
   the configured DUID and the configured assignment identifiers for addresses and
   prefixes. Check the prefix hint on the request.
2. Give the address and prefix different preferred and valid lifetimes. Check each
   assignment independently as it deprecates or expires.
3. Exercise partial rejection, address change, prefix length change, duplicate address
   detection, and renewal loss through rebinding. Verify kernel cleanup and translation
   updates after a replacement or withdrawal.
4. Stop router advertisements during a valid DHCPv6 lease. Verify that the router can
   expire independently of the address and prefix. Restore advertisements and check
   forwarding for each family.
5. Send inbound packets to a local DHCPv6 address within a translated prefix and to an
   intentional forwarding address. Check local delivery for the former and downstream
   replies for the latter. Repeat after address renewal, replacement, and expiry.
6. Repeat with a statically routed prefix and a networkd owned connection. Check that
   MWAN respects the configured acquisition owner.

## Check kernel IPv6 autoconfiguration

Use the
[autoconfiguration daemon tests](../../../gateway/internal/commandline/autoconfiguration_netns_test.go)
with forwarding enabled. Verify the configured sysctls and send router advertisements
with independent router and prefix lifetimes. Check that a tentative address does not
establish readiness. Compare deprecation, expiry, and restored advertisements with
kernel addresses, routes, and downstream packets. Recreate the device with a different
index and verify that the daemon restores the configured policy without disrupting an
unrelated link or usable IPv4 service.

## NPT address authority: Journal edges across connection owners

Use the
[mapped address test](../../../gateway/internal/commandline/ifmgr_owned_mapped_netns_test.go)
and the
[systemd NPT test](../../../gateway/internal/commandline/ifmgr_npt_networkd_runtime_test.go)
with an explicit private address journal.

1. Check that the journal records the external `::1/128` edge and its verified link
   identity before the daemon installs the address. Verify the edge uses the `npt-edge`
   scope and ordinary address cleanup leaves it alone. Restart the daemon and verify
   translation and replies. Compare acquisition and ordinary addresses with the
   configured connection owner.
2. Change only the owner while the verified link remains present. Check continuity of
   the journal record and translated packets. If the test deletes a VLAN, verify that
   its replacement acquires its own valid address.
3. Change the prefix, expire a delegation, and withdraw a provider in separate runs.
   Verify that the daemon removes managed translation references before releasing an
   obsolete edge. Check that an unrelated provider still forwards packets.
4. Present an identical foreign address or an address with a different prefix length.
   Verify that the daemon rejects the conflict and preserves the foreign address.
5. Fail translation inspection or removal and verify that the daemon retains the edge
   record for retry. Remove the last NPT configuration and verify that cleanup resumes
   after restart.
6. While DHCPv6 validates a saved delegation, check that only the journaled edge inside
   the matching, still valid prefix remains present. Verify that a saved prefix alone
   does not activate translation or publish forwarding readiness.
