> Read the [repository context](../../README.md) before using copied commands or historical plans.

# MWAN gateway layout

The `wan` role runs the local Linux gateway services. Install its embedded unit files and schema with this command:

```bash
mwan install --role wan --apply
```

The installer enables `mwan-agent.service`, `mwan-ifmgr@wan.service`,
`rousette.service`, and `nghttpx-wanconfig.service`. It does not stop or restart
running services.

## WAN links

The gateway uses policy routing to send outbound traffic over AT&T and Webpass.
It selects Monkeybrains as a health fallback.

- The `enwebpass0` interface connects to Webpass. It receives a dynamic carrier-grade
  NAT IPv4 address and a provider-delegated IPv6 prefix.
- The `enatt0.3242` interface connects to AT&T through an 802.1X-authenticated
  VLAN. It receives a dynamic IPv4 address and an AT&T-delegated IPv6 prefix.
- The `enmbrains0` interface connects to Monkeybrains. It receives a static
  public IPv4 address, a SLAAC IPv6 address, and a DHCPv6 delegated prefix.
  Network Prefix Translation maps the internal IPv6 range onto that prefix.
  The prefix can change, so inspect its current value with `mwan pd enmbrains0`.

The untagged `enatt0` interface connects to the AT&T optical network terminal
and parents the tagged `enatt0.3242` interface. See the
[ONT access and 802.1X procedure](ont.md) for terminal access and VLAN
authentication diagnosis.
