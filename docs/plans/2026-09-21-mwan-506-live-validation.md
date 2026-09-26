> Read the [repository context](../README.md) before using copied commands or historical plans.

# MWAN-506 Live Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the completed MWAN-506 deployment boundary on the testbed and production, then close the Tack work with live evidence.

**Architecture:** This plan begins after every implementation pull request has merged and the final signed `mwan` release exists. Validation checks and deploys the testbed, proves rejection against the installed testbed binary without changing its configuration, then checks and deploys production after explicit approval.

**Tech Stack:** Configs, Ansible, `configsctl`, the released `mwan` binary, sysrepo, Linux networking, GitHub releases, and Tack.

**Spec:** [../wanconfig/providers.md](../superpowers/wanconfig/providers.md), with the deployment boundary defined by [../wanconfig/config.md](../superpowers/wanconfig/config.md)

## Global Constraints

- Start only after every task in the [MWAN-506 completion plan](2026-09-21-mwan-506-completion.md) has merged.
- Use the same signed `mwan` release in the testbed and production.
- Prove the testbed before changing production.
- The production deployment reboots the gateway and requires explicit approval.
- Record the running commit, provider health, policy rules, link state, firewall structure, and operational rejection state after each deployment.
- Do not rewrite historical Tack evidence comments.

## Review Focus

- A provider-local loader error must fail before any gateway file changes.
- A valid testbed render must report every intended provider accepted.
- The testbed must preserve its provider set, policy rules, links, and firewall structure after deployment.
- Production must run the same signed release that passed the testbed.
- Tack must close only after live evidence exists for both gateways.

---

### Task 1: Validate and deploy the testbed

**Files:**
- Modify: no repository file

**Interfaces:**
- Consumes: the merged Configs deployment and the final signed `mwan` release.
- Produces: live testbed evidence for the deployed release and provider state.

- [ ] **Step 1: Capture the testbed state before deployment**

Run:

```bash
ssh mwan.suburban.goodkind.io 'ip -6 rule show; ip rule show' \
    > /tmp/mwan-506-testbed-before-rules.txt
ssh mwan.suburban.goodkind.io 'nft list ruleset' \
    > /tmp/mwan-506-testbed-before-nft.txt
```

- [ ] **Step 2: Run the testbed check deployment**

Run:

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers --check --diff
```

Expected: the released loader reports every intended provider accepted before the first gateway write that check mode would perform.

- [ ] **Step 3: Deploy the testbed**

Run:

```bash
./configsctl deploy deploy-mwan --limit mwan_suburban_servers
```

- [ ] **Step 4: Verify the deployed testbed**

```bash
ssh mwan.suburban.goodkind.io 'mwan version'
ssh mwan.suburban.goodkind.io 'systemctl is-active mwan-ifmgr@wan mwan-agent'
ssh mwan.suburban.goodkind.io 'ip -brief link show dev enatt0'
ssh mwan.suburban.goodkind.io 'ip -brief link show dev enwebpass0'
ssh mwan.suburban.goodkind.io 'ip -brief link show dev enmbrains0'
ssh mwan.suburban.goodkind.io 'ip -6 rule show; ip rule show' \
    > /tmp/mwan-506-testbed-after-rules.txt
ssh mwan.suburban.goodkind.io 'nft list ruleset' \
    > /tmp/mwan-506-testbed-after-nft.txt
diff /tmp/mwan-506-testbed-before-rules.txt /tmp/mwan-506-testbed-after-rules.txt
diff /tmp/mwan-506-testbed-before-nft.txt /tmp/mwan-506-testbed-after-nft.txt
curl -sS "http://[3d06:bad:b01:210::213]:10080/restconf/data/ietf-interfaces:interfaces" \
    > /tmp/mwan-506-testbed-tree.json
jq '.["ietf-interfaces:interfaces"]["goodkind-mwan-steering:steering-group"].state["rejected-provider"] // []' \
    /tmp/mwan-506-testbed-tree.json
```

Expected: both units are active, the binary reports the release commit, the three configured providers retain their rules and links, the firewall structure matches the accepted testbed configuration, and the query returns an empty array.

---

### Task 2: Prove the rejection boundary without changing gateway configuration

**Files:**
- Modify: no repository file

**Interfaces:**
- Consumes: the released binary and schema installed on the testbed in Task 1.
- Produces: command output proving that one provider-local error rejects an intended deployment without modifying `/etc/mwan/network.json`.

- [ ] **Step 1: Copy the installed document without changing it**

Run:

```bash
test ! -e /tmp/mwan-506-network.json
test ! -e /tmp/mwan-506-rejected-network.json
scp mwan.suburban.goodkind.io:/etc/mwan/network.json /tmp/mwan-506-network.json
```

- [ ] **Step 2: Create one provider-local error in the copy**

```bash
jq '(
  .["ietf-interfaces:interfaces"].interface[]
  | select(.name == "enwebpass0")
  | .["ietf-ip:ipv6"]
) |= del(.["goodkind-mwan-steering:dhcp"])' \
    /tmp/mwan-506-network.json \
    > /tmp/mwan-506-rejected-network.json
```

Expected: the copy still declares IPv6 router advertisements for `enwebpass0` but omits that entry's DHCP value. `/etc/mwan/network.json` remains unchanged.

- [ ] **Step 3: Run the installed loader against the copy**

Run:

```bash
scp /tmp/mwan-506-rejected-network.json \
    mwan.suburban.goodkind.io:/tmp/mwan-506-rejected-network.json
ssh mwan.suburban.goodkind.io \
    'mwan deploy-gate check-network /tmp/mwan-506-rejected-network.json /usr/local/share/wanconfig/yang'
```

Expected: the command returns 1, identifies `enwebpass0`, reports one rejected provider and two accepted providers, and writes no gateway configuration.

- [ ] **Step 4: Remove the temporary copies**

Run:

```bash
unlink /tmp/mwan-506-network.json
unlink /tmp/mwan-506-rejected-network.json
ssh mwan.suburban.goodkind.io 'unlink /tmp/mwan-506-rejected-network.json'
```

Expected: every ticket-specific temporary file is absent. `/etc/mwan/network.json` remains unchanged.

---

### Task 3: Validate and deploy production

**Files:**
- Modify: no repository file

**Interfaces:**
- Consumes: the testbed evidence from Task 2 and the exact production deployment command.
- Produces: live production evidence for the same signed release.

- [ ] **Step 1: Request production deployment approval**

Present the testbed evidence and this exact command:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers
```

Wait for explicit approval because the deployment reboots the production gateway.

- [ ] **Step 2: Capture the production state before deployment**

```bash
ssh mwan.home.goodkind.io 'ip -6 rule show; ip rule show' \
    > /tmp/mwan-506-production-before-rules.txt
ssh mwan.home.goodkind.io 'nft list ruleset' \
    > /tmp/mwan-506-production-before-nft.txt
```

- [ ] **Step 3: Run the production check deployment**

Run:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers --check --diff
```

Expected: the released loader reports every intended provider accepted before the first gateway write that check mode would perform.

- [ ] **Step 4: Deploy production**

Run the approved command:

```bash
./configsctl deploy deploy-mwan --limit mwan_servers
```

- [ ] **Step 5: Verify the deployed production gateway**

Run:

```bash
ssh mwan.home.goodkind.io 'mwan version'
ssh mwan.home.goodkind.io 'systemctl is-active mwan-ifmgr@wan mwan-agent'
ssh mwan.home.goodkind.io 'ip -brief link show dev enatt0'
ssh mwan.home.goodkind.io 'ip -brief link show dev enwebpass0'
ssh mwan.home.goodkind.io 'ip -brief link show dev enmbrains0'
ssh mwan.home.goodkind.io 'ip -6 rule show; ip rule show' \
    > /tmp/mwan-506-production-after-rules.txt
ssh mwan.home.goodkind.io 'nft list ruleset' \
    > /tmp/mwan-506-production-after-nft.txt
diff /tmp/mwan-506-production-before-rules.txt /tmp/mwan-506-production-after-rules.txt
diff /tmp/mwan-506-production-before-nft.txt /tmp/mwan-506-production-after-nft.txt
curl -sS "http://[3d06:bad:b01::113]:10080/restconf/data/ietf-interfaces:interfaces" \
    > /tmp/mwan-506-production-tree.json
jq '.["ietf-interfaces:interfaces"]["goodkind-mwan-steering:steering-group"].state["rejected-provider"] // []' \
    /tmp/mwan-506-production-tree.json
```

Expected: both units are active, the binary reports the same release commit as the testbed, all three provider links are present, the policy and firewall diffs are empty, and the query returns an empty array.

---

### Task 4: Close MWAN-506 with live evidence

**Files:**
- Modify: no repository file
- Update: MWAN-506 and MWAN-324 in Tack

**Interfaces:**
- Consumes: merged pull requests, the release tag, and the testbed and production evidence.
- Produces: final Tack state for MWAN-506 and its MWAN-324 follow-up.

- [ ] **Step 1: Update MWAN-506 with one canonical evidence comment**

Include the three merged pull requests, release tag, testbed evidence, production evidence, and this final contract:

```text
The deploy validates each rendered network document with the released production loader before changing a gateway. Schema errors, group-wide errors, provider-set collisions, and any provider rejection stop the deploy. Runtime startup preserves accepted providers when one schema-valid provider entry fails a provider-local loader rule, and the served steering state reports every omitted provider with its interface, provider name, and reason.
```

- [ ] **Step 2: Complete the Tack work**

Set MWAN-506 to Done. Add a short MWAN-324 comment that its follow-up is complete. Preserve the existing historical evidence comments.
