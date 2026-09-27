> Read the [repository context](../../README.md) before using copied commands or historical plans.

# MWAN and OPNsense script style

Working rules for MWAN and OPNsense shell scripts and the Ansible templates
that deploy them.

Prefer non-disruptive actions on the MWAN VM. Avoid
`systemctl restart systemd-networkd` unless necessary.

## Deterministic convergence (no races)

- Scripts and services must be idempotent and tolerate partial boot ordering.
- Any script that mutates shared kernel state must be serialized with a lock:
  use `flock` against a lock file in `/run/...` for `ip rule`, `ip route`, and
  `nft` writers.
- When a service can run before prerequisites are ready, gate it with an
  `ExecStartPre` wait script and add `Restart=on-failure` with retries.

## Jinja and templating strategy

- Do not keep a `.j2` file if it contains no Jinja (no `{{`, no `{%`, no
  `{#}`). Use a normal extension and deploy with `ansible.builtin.copy`.
- If a shell script, hook, or service only uses templating to inject
  variables, prefer one templated env file (for example `/etc/mwan/mwan.env`)
  and make the script static. Source the env via `. /etc/mwan/mwan.env`. For
  systemd units, prefer `EnvironmentFile=/etc/mwan/mwan.env`.
- A TOML template reads each secret as its `vault_` name rather than as a
  literal value; the vault contract is in [secrets.md](https://github.com/agoodkind/configs/blob/main/docs/ops/ansible/secrets.md).

## WAN state terminology

Use **healthy / unhealthy / unknown** for WAN health. Do not use **up / down**
for health, because that conflicts with `ip link` state semantics.

## Logging and JSON

- Prefer structured JSON logs to `/var/log/mwan-debug.log`.
- Use `jq -cn` for JSON generation; avoid `printf`-built JSON.
- Avoid embedded Python for JSON generation or parsing.
- Include `traceId` in logs when available (`MWAN_TRACE_ID`).

## Parsing and tooling preferences

- Prefer machine-readable outputs:
  - `ip -j ... | jq` over `ip ... | awk/sed`.
  - `networkctl ... --json=short | jq` over text parsing.
  - `ipcalc-ng --all-info -j ... | jq` over bespoke IPv6 math.
  - `nft -j` for inspection where feasible.
- Avoid long pipe trains like `... | grep ... | tail -1 | awk ... | awk ...`.
  Prefer a single-pass `awk` program (track `last=...` and print in `END`) or
  capture output to local variables.
- Keep scripts portable across Debian base utilities (avoid GNU-only flags
  unless already assumed or installed).

## jq usage

- Use `jq` for selection, extraction, and light reshaping.
- Avoid algorithmic formatting in `jq` (for example, converting a byte array
  into an IPv6 literal). Move that to a small shell helper.

## ipcalc-ng helper patterns

If you find yourself repeating `ipcalc-ng ... | jq -r '.NETWORK'`, factor it
into a helper such as `ipcalc_field <cidr> <jq_field>` and
`ipcalc6_field <cidr> <jq_field>`. Keep long command substitutions readable
with multi-line `$( ... )`.

## Shell style and safety

- Use `set -euo pipefail` for scripts that mutate state.
- Use `|| true` only at expected failure points (best-effort probes or
  cleanup).
- Source constant paths directly (`. /etc/mwan/mwan.env`) to keep shellcheck
  happy; avoid unused `ENV_FILE` variables.
- Keep lines reasonably short; wrap long `{ ... }` blocks with newlines after
  `{`.
- For every MWAN script or hook, include a top comment block describing what
  it does (symptoms plus technical effect) and where it sits in the dependency
  graph.

## Console auto-login

- Physical console access (keyboard plus VGA, or serial) auto-logs in as root
  without a password.
- SSH access requires normal authentication (password or key).
- Configuration uses systemd getty service overrides:
  - `getty@tty1`: `--autologin root --noclear %I $TERM`.
  - `serial-getty@ttyS0`: `--autologin root --keep-baud 115200,57600,38400,9600 - ${TERM}`.
- Idempotent deployment: Ansible handles directory creation; systemd reloads on
  reboot.

## Gateway nftables rules

The gateway daemon installs and repairs its firewall, translation, and
steering rules. The gateway no longer installs a rendered `nftables.conf` or
uses `nftables.service` to load policy. Change the network configuration and
deploy it for a durable firewall change. Do not use `nft -f` to install a
durable gateway rule or reload the entire ruleset.

The destination refresher updates addresses in configured destination sets.
The daemon creates missing sets and preserves existing elements during
reconciliation. Separate failover-container rules retain their own service.

## Documentation constraints

- Do not add new docs unless asked.
- When updating existing docs, avoid giant pasted code blocks; prefer short
  excerpts and direct pointers to files.
- When you encounter and fix issues, document them in
  [operations.md](../opnsense/operations.md) using the established format.
