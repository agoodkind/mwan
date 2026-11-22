# Verify gateway configuration and protocol acceptance

Validate the intended network document and model directory with the installed gateway
executable. Use `mwan debug check-network` for shared configuration validation and
`mwan debug check-firewall` for firewall validation in a privileged Linux environment.
Run each command with `--help` for its required inputs.

## Check the configuration boundary

1. Include continuing provider links, parents, internal interfaces, and management
   interfaces. Verify explicit ownership, stable identities, family settings, routes,
   and resolver settings.
2. Validate the exact document the daemon will load. Check that conflicting writers,
   unsupported required options, and missing dependencies fail validation or startup at
   the appropriate boundary.
3. Start the production daemon with the validated document and a private state
   directory. Check startup protection, installed firewall state, and address ownership.
   If NPT uses an edge address, check its journal and the actual translated packets.

## Check acquisition and forwarding

Use connected Linux namespaces with real Kea and radvd processes. Include VLAN devices,
an upstream provider peer, and a downstream sender. Run the
[protocol namespace service](../../../gateway/compose.test.yaml) for real protocol and
packet cases and the [systemd service](../../../gateway/compose.systemd.yaml) for cases
that require networkd, resolved, or udev. The
[CI workflow](../../../.github/workflows/ci.yml) records the current commands and
cleanup.

1. Exercise acquisition, renewal, rebinding, expiry, changed assignments, and restart.
   Check protocol identifiers in packets and compare recorded deadlines with the server
   exchanges.
2. Verify return traffic to a delegated prefix through the gateway's stable link-local
   next hop. Check IPv4 and IPv6 downstream packets, inbound mapping replies, and
   translation independently.
3. Exercise absent provider startup, interface recreation with a different index, and
   owned route deletion. Verify repair and continued service on another provider.
4. Vary new connections according to the configured hash mode. Compare packet counts at
   provider peers with eligibility, tiers, and weights.
5. Check that the resolver state reflects the configured DNS and domain policy for
   static and acquired addresses. Check updates after a lease changes or expires.
6. Record the daemon and protocol server versions, configuration, kernel state, and
   packet results. Use the isolated results for the behavior each suite exercises.
   Measure service startup and cold boot separately when those checks are required.
