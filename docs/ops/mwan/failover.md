> Read the [repository context](../../README.md) before using copied commands or historical plans.

# MWAN failover and rollback

Production failover is BGP-based. The agent embeds GoBGP, peers with OPNsense
FRR over iBGP, and announces a default route (`0.0.0.0/0`, `::/0`) when
healthy. OPNsense runs FRR (`os-frr`) with a route-map that prefers the primary
via higher local-pref. The watchdog withdraws routes via the agent's gRPC API
when health degrades; if the agent crashes, the BGP session drops and OPNsense
converges to the backup within the hold timer.

The `[bgp]` config section carries every BGP parameter: ASN, router ID,
neighbors, timers, and prefixes.

Failover decision matrix:

| Primary Internet | Failover LXC Internet | Cause                      | Watchdog action                          |
| ---------------- | --------------------- | -------------------------- | ---------------------------------------- |
| OK               | OK                    | Normal                     | No action                                |
| OK               | DOWN                  | Failover WAN issue         | Alert only                               |
| DOWN             | OK                    | Primary config or WAN down | Withdraw primary routes or force backup  |
| DOWN             | DOWN                  | Upstream outage            | Alert only                               |
| Agent down       | OK                    | Primary agent crash        | BGP session drops; OPNsense converges    |

`mwan watchdog failover` triggers the BGP failover path immediately. The
current failover path uses BGP route control.

## BGP graceful restart

BGP Graceful Restart (RFC 4724) lets the agent restart its BGP process without
flapping its routes in the helper. The helper retains the restarter's prefixes
for `restart_time` seconds and only flushes them if the session does not come
back. The agent restarts on every deploy, so GR is the path to zero-flap
deploys. With GR off, the agent restart briefly drops the WAN route.

When GR is enabled the speaker negotiates the capability globally and per
peer, and allows graceful restart on stop. The agent shutdown path skips the
pre-emptive default-route withdraw when GR is on. An explicit WITHDRAW would
defeat GR: FRR would drop the route immediately. Pre-withdraw only runs when
GR is off.

The `[bgp.graceful_restart]` config section carries the settings, and the
loader bakes in the defaults so an empty block matches documented behaviour.

The OPNsense FRR side has its own toggle,
`OPNsense.quagga.bgp.graceful = '1'` in the router config. Production
operators flip it via the OPNsense GUI under Routing -> BGP -> General. The
testbed has no GUI from the controller, so the operator drives the
`mwan-opnsense` gRPC API to mutate the router config directly, then runs
`configctl quagga reload bgp`. Verify with:

```bash
vtysh -c 'show running-config router bgp' | grep 'bgp graceful-restart'
```

BFD is the natural follow-up. GR is only safe-by-default with BFD when a real
WAN link dies inside the GR window. Without BFD the helper holds stale
routes for the full `restart_time`. OPNsense carries a BFD stanza toward the
neighbor, but the mwan agent's embedded speaker does not participate yet, so no
BFD session forms; fast WAN failure detection relies on the watchdog gRPC
withdraw path.

## Watchdog rollback design

The watchdog runs on the Proxmox host. It bases the rollback decision on
**whether config recently changed**, not on per-interface probes from inside
the VM. If config changed and connectivity then broke, config is the most
probable cause. If config has been stable and connectivity breaks, it is
probably external.

Two signals count as a recent config change:

1. **Deploy timestamp** (`/var/run/mwan-last-deploy`), written by the deploy
   playbook before pushing new config.
2. **Config hash change**, detected by `checkConfigHash` when the composite
   hash reported by `mwan-agent` changes.

Decision matrix:

| Connectivity fails? | Recent deploy timestamp? | Recent hash change? | Stable before? | Action                              |
| ------------------- | ------------------------ | ------------------- | -------------- | ----------------------------------- |
| Yes                 | Yes (within 60s)         | -                   | -              | Grace period; wait for reboot       |
| Yes                 | Yes (past 60s grace)     | -                   | -              | Connectivity timeout, then rollback |
| Yes                 | No                       | Yes (within window) | Yes            | Connectivity timeout, then rollback |
| Yes                 | No                       | No                  | Yes            | Test LXC, then failover or wait     |
| No                  | -                        | -                   | -              | Healthy; normal monitoring          |

Grace period:

- Deploy timestamp detected: 60s grace, then the normal connectivity timeout
  (`CONNECTIVITY_TIMEOUT_SECONDS`, default 30s) begins.
- Hash-only changes get no grace period. They should not cause reboots.

Hash-change recency window: a hash change is "recent" for
`DEPLOY_WINDOW_MINUTES` (default 30). Anything older is treated as external.

### Snapshots

Two snapshot types with different owners:

- **`pre-deploy-*`** snapshots are owned by the deploy playbook. The playbook
  must create `pre-deploy-<unix-timestamp>` before pushing any config to the
  MWAN VM. Without it, a fresh or recently changed VM may have no rollback
  target until a `known-good-*` snapshot is created (which takes many healthy
  probe cycles).
- **`known-good-*`** snapshots are owned by the watchdog and taken
  automatically after the system has been healthy and stable for a sustained
  period.

Rollback target order is: latest `pre-deploy-*`, then most recent
`known-good-*`. If neither exists, the watchdog alerts but does not recover.

`known-good-*` is taken when all are true:

1. Healthy for `SNAPSHOT_HEALTHY_THRESHOLD` consecutive probe cycles
   (default 20).
2. Config hash stable for `DEPLOY_WINDOW_MINUTES`.
3. No recent deploy timestamp (outside the deploy window).
4. At least `MIN_SNAPSHOT_INTERVAL_SECONDS` (default 300s) since the previous
   snapshot.

Pruning keeps at most `MAX_KNOWN_GOOD_SNAPSHOTS` (default 2) and
`MAX_TOTAL_SNAPSHOTS` (default 15), deleting oldest first. The deploy
playbook keeps at most 1 `pre-deploy-*` snapshot, pruning immediately after
it creates the new one.

Proxmox snapshot names are capped at 40 characters and longer names truncate
silently. Put the full intent in `--description` and keep the name short. Do
not save RAM in a snapshot. Rollback then resumes with stale networking and
clock state.
