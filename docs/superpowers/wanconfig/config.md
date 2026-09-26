> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Two: the configuration format

The gateway's network configuration becomes the model's own JSON
encoding, so one representation runs from inventory through to the
served tree for everything the management surface describes. Ansible
renders it as `/etc/mwan/network.json`; the daemon validates it against
the schema at startup; the deploy validates it with the same schema
before it ever reaches the gateway. The TOML configuration file stays
beside it and keeps every non-network section.

Depends on the model existing and being served, because the encoding is
the model's and the served tree is how the change is verified.

## Scope

`network.json` carries exactly the gateway daemon's network tree: the
provider inventory, each provider's routing numbers, translation prefix,
IPv4 source pin, health probe settings, and link identity, and the
group-wide translation, internal-link, and probe-timeout values. Each
provider's configuration hangs off the interface that carries it, and
health settings live inside the provider they probe, one to one, with no
shared policy object. Link identity is what the network manager needs to
bring the link up: how the device is matched, its name and hardware
address, each family's addressing, the delegation client's identity and
hint, and any free-form unit-file sections the provider needs. The
delegation client identity is a public identifier, not a secret.

Everything else stays in `config.toml` with no schema node: alert mail,
notify cadence, the agent, the watchdog, the Proxmox API, the OPNsense
sections, the BGP speaker, the failover section, the publish gate, the
host identity leaves, the interface manager's plumbing scalars, the
standalone policy rules, and every credential. No secret ever enters the
JSON. The daemon reads both files, and exactly one file owns each
section.

The inventory remains the source of truth: group_vars render the file,
and a provider change is an inventory edit plus a deploy. No value
conversion exists anywhere, because the inventory stores bare integers
and the schema's time spans are integers that name their unit. Writing
configuration over the management API, with `network.json` becoming the
source of truth, is deferred work (MWAN-440).

## Routing extension requirements

The completed JSON migration leaves the BGP speaker in TOML.
MWAN-507 must represent new tunnel and routing configuration through the
shared model. Its implementation plan must assign one authoritative source
to every setting and define any migration of existing speaker settings.
Do not configure one session independently in both JSON and TOML. Preserve
existing internal sessions and keep secret values outside the served JSON.

## Why the format changes

Two representations of the same thing is the problem this work exists to
remove. A model that describes the daemon while the daemon loads a
differently shaped file recreates it one layer up, and the two drift the
moment someone edits one of them.

There is a second gain. The released production loader checks a file in the
model's encoding before the deploy changes the gateway. It checks the schema
first, then applies the relationships the schema cannot express. The piece
that moves firewall ownership into the daemon deletes the only pre-flight
validation that exists today, a check-mode parse of the rendered ruleset on
the target before installation. Schema validation of the rendered
configuration and the loader's additional rules replace it.

## What changes

Inventory renders `network.json` in the model's encoding. The daemon
loads it at startup and validates it against the schema before acting on
any of it. The deploy runs that same loader against the rendered bytes before
it changes the gateway. A file that does not satisfy the schema stops the
daemon before it programs anything, which is the existing failure contract.
The TOML loader stops reading a network section in the same change the JSON
loader starts owning it, so no state exists where both files feed one
setting.

The provider values stay in the two gateway groups the TOML template
already reads, so the JSON template and the TOML template render from one
set of variables. Moving the catalogue into a group the hypervisor can
read is separate work that belongs with the provider-set epic.

The environment file stays. The pinned-destination refresher and the
whole 802.1X chain read it, and it hardcodes both the variable names and
the address set names, so retiring it is separate work.

## What must not change

The daemon's behavior, given equivalent input. This piece changes how
the network configuration is written and read, not what any value means.

Validation must remain strict in the same places it is strict today. A
missing value is a load-time failure, not a defaulted one. The model
makes that easier to enforce, since a leaf is either present or it is
not.

The failover container and the hypervisor keep their TOML-only
configuration untouched; their roles run none of the sections the JSON
carries.

## Acceptance

Both environments render a `network.json` that validates against the
schema.

Rendering an invalid configuration fails in the deploy, before the file
reaches the gateway, and the gateway is untouched.

On the same host, the served tree and the programmed rules, routes, and
policy rules are unchanged across the cutover. File equivalence is not
the test, because the file is a different format by design. Behavioral
equivalence is the test, proven by a traffic and routing matrix run
before and after the cutover on the testbed: forced egress per provider
and per address family with the translated source observed at the
provider simulator's ingress, reply symmetry, observed balancer
distribution, a fallback drill in both families, pinned destinations,
and the inbound translation paths.

The network tree is deleted from the TOML render and load paths;
`config.toml` keeps only the non-network sections.

## Failure modes

A file that validates but means something different from its TOML
predecessor is the risk this piece carries, and the served tree plus the
traffic matrix are what catch it. Compare both, before and after, on the
same host.

Schema validation at deploy time and at load time must use the same
schema files. Two copies of a schema is the same duplication failure in
a new place. The released binary carries the modules, so the deploy
validates with the modules that binary prints and the daemon validates
with the copies that binary installed.
