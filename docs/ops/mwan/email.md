> Read the [repository context](../../README.md) before using copied commands or historical plans.

# MWAN email and alert routing

MWAN email routes through one notifier boundary that owns per-(kind, key)
state-change suppression. An alert emits when a condition becomes active,
stays silent while the condition is unchanged, re-emits on a per-kind repeat
cadence, and emits once more when the condition resolves. Journald carries the
kind on every emit, so a typo in a kind string is visible in the logs.

## Configure routing

Two config sections govern routing. The `[email]` section carries the
recipient, sender, subject prefix, bind interface, minimum level, and
cooldown. The `[notify]` section carries the repeat cadence: `repeat_every`
is the global default, and `per_kind` overrides it per alert kind. A zero
`repeat_every` means alerts fire once per transition and never repeat. When
the `[notify]` section is empty, the older `[ifmgr.alerts]` cadence settings
are honored as a fallback.

```toml
[notify]
repeat_every = "1h"

[notify.per_kind]
"vsock-fallback"       = "30m"
"ops-transport-failed" = "30m"
```

## Rotate the SMTP2GO key

The vault stores `vault_smtp2go_api_key`. Deploys render an environment file
at `/etc/mwan/secrets.env` (mode 0640, root only) on every MWAN host, and the
systemd units load it, so the binary reads `SMTP2GO_API_KEY` from the
environment; an environment value overrides the config field. Rotate the key
with `configsctl set-secrets`, then redeploy the MWAN hosts.

## Failure modes

A sender error (a 5xx from the provider, a network blip, a missing bind
interface) is logged to journald, and the per-(kind, key) active flag stays
set so the next state change still emits. The repeat cadence keeps firing; a
persistently broken transport produces a journald error per cadence tick, not
retry email. There is no on-disk retry queue.

The suppression state is in-memory only. After a process restart, the first
event of any kind and key pair always emits, because erring toward emitting
after a crash beats silently swallowing a real alert. A planned restart can
therefore produce one extra email per active condition.

Any non-empty kind string is accepted; there is no allow-list. Unseen kinds
get the same state-change suppression and the default repeat cadence.

When the `[email]` section is absent or the recipient is empty, alerts still
surface as journald log lines and no email is attempted. A notifier
construction failure collapses to a null notifier that drops events, and the
failure itself is logged at boot.
