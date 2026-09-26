> Read the [repository context](../../../README.md) before using copied commands or historical plans.

# Recover testbed OPNsense without network access

Use the serial channel when the testbed router's network or SSH daemon is
unavailable. The channel connects suburban to the guest without using the
guest's addresses, packet filter, or routing.

Open a direct SSH session to suburban using the
[infrastructure access guide](https://github.com/agoodkind/configs/blob/main/docs/ops/infra/access.md). Run the commands below
from that host.

## Verify the channel

```sh
opnsensectl daemon version
opnsensectl exec /bin/hostname
```

The first command returns the running daemon's build identity. The second
returns the guest hostname. Both commands must succeed before recovery work.

## Run a recovery command

Run a guest command through the serial channel:

```sh
opnsensectl exec <command> [args...]
```

Inspect the current configuration commands before changing the router:

```sh
opnsensectl config --help
```

The [OPNsense serial daemon](../daemon.md) defines the channel's operating and
recovery limits.

## Escalate when the channel is unavailable

Use the serial-console path in the same access guide. Repair the guest
endpoint with the [OPNsense installation guide](../install.md).
