# Verify recovery of saved DHCP assignments

Run process recovery checks with the production WAN daemon, real DHCP servers, private
state directories, and connected Linux namespaces. Use the [DHCPv4 restart tests](../../../gateway/internal/commandline/dhcp_restart_netns_test.go),
the [DHCPv4 recovery discovery tests](../../../gateway/internal/commandline/dhcp_recovery_discover_netns_test.go),
and the [DHCPv6 restart test](../../../gateway/internal/commandline/dhcpv6_restart_netns_test.go).

## Check recovery exchanges

1. Acquire an assignment and restart the daemon before it expires. Compare client
   identity, server packets, and the original deadlines. Confirm that restarting the
   daemon does not extend a lease.
2. Verify the DHCPv4 INIT-REBOOT request. Exercise rejection and delayed server
   availability. Compare kernel addresses and routes before validation, after rejection,
   and after any replacement. Check that a late or recreated interface receives a
   request from the current client.
3. Verify DHCPv6 Confirm for address-only recovery. Verify Rebind whenever saved state
   contains a valid delegated prefix. Check a partial reply with different deadlines
   for address and prefix assignments. Verify that validity follows the server response
   for each association. The [DHCPv6 Confirm server test](../../../gateway/internal/netif/dhcpv6_restart_netns_test.go)
   exercises address-only recovery.
4. Exercise an expired record, invalid storage, a different link identity, and a changed
   route metric. Check that the daemon preserves unrelated state and reconciles valid
   assignments under the current routing policy. A surviving kernel address does not
   prove lease validity.
5. Verify that configuration removal clears the daemon's obsolete journaled objects.
   Start with an empty lease store and check that the daemon negotiates a new lease.

## Check storage and readiness

Use a private lease directory in the test configuration. Require mode `0700` on the
directory and mode `0600` on lease records. Interrupt a record replacement and check
that recovery reads a complete record. Reject corrupt or incompatible data. Check that
an uncertain clock or a new boot does not validate the saved lease without a server
reply. Inspect the configured state paths and boot dependencies in the
[installed systemd unit](../../../gateway/installspec/mwan-ifmgr@.service).
Verify writable storage and the recorded deadlines separately from the startup result.
Check translated downstream traffic in both usable address families after recovery.

Record the daemon, server exchange, original deadlines, restored kernel state, and
packet results. The process tests establish recovery in isolated namespaces. Check cold
boot separately when verifying boot ordering or installed service permissions.
