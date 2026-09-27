> Read the [repository context](../../README.md) before using copied commands or historical plans.

# A standards-modeled MWAN gateway

The MWAN gateway VM terminates several internet provider links and steers
traffic across them. It has no model. Each behavior was added where it was
needed, so IPv4 and IPv6 reach the same goal by different mechanisms, owned
by different components, sharing no vocabulary. Adding a provider takes about
forty scattered inventory edits, four hand-written link files, new rules in a
240-line firewall template, and Go changes, and two validators reject a
fourth provider outright.

This work adopts an existing standard model rather than inventing one. Every
provider becomes an interface with a container per address family, carrying
its own addressing, forwarding, and translation. Translation becomes an
instance with a declared type, so prefix translation, address masquerade,
one-to-one mapping, and no translation at all are values instead of code
paths. Steering becomes members with a tier, a weight, and a probe policy.
The daemon then serves that model, so an operator can answer why traffic is
leaving a given provider without logging in.

The model is defined once in [model.md](model.md). Everything below is
expressed in its terms.

OPNsense at `router.home.goodkind.io` sits behind the gateway and is not
modified by any part of this work.

## Decisions that bind every piece

**Read-only.** The served model exposes configuration as loaded and live
state. It accepts no writes, so the deploy path and its rollback keep their
role and configuration still applies at daemon restart. The stack enforces
this at the protocol layer: its access control (RFC 8341) denies every write
by default, so a RESTCONF write method or a NETCONF edit receives the
standard protocol error, and live state is served from the operational
datastore, which accepts no writes at all. Opening writes is a later
decision with its own safety work, not part of this epic.

**The mature management stack serves the model.** libyang reads the
description files and its validator gates the build, sysrepo holds the
datastore, and the stack's own servers speak the protocols: RESTCONF, with
NETCONF available from the same stack at no extra work. None of the protocol
work is ours. The stack ships as Debian packages the mwan release builds
and proves, installed on the gateway with apt. The subscription
transport is settled when the streaming piece lands; the model is identical
either way.

**The model's own encoding is the configuration format.** Inventory renders
the model as JSON rather than as TOML, so one representation runs from
inventory through to the served tree.

**The daemon is the only thing that writes the firewall.** The ruleset file
is deleted and the firewall service is masked.

**We write exactly one small piece: the publishing binding.** The daemon
publishes its state into the datastore through a Go-to-C binding of ours. It
stays small because this work only publishes and never accepts edits:
connect, open a session, set values by path, apply. Nothing protocol-shaped
is written from scratch.

## Language and scope of the extension

Use plain terms in every related ticket, specification, implementation plan,
and operator message. Explain the behavior before the protocol term. Define
an unfamiliar provider term on first use, and use the shared model's provider
terms consistently. Do not require a provider topology choice when the shared
implementation can support the alternatives through configuration.

MWAN-507 extends the WAN configuration work with direct and tunneled IPv6
routing. Its shared requirements apply to the remaining translation and
firewall designs. The model specifies the supported arrangements and provider
vocabulary.

"Drop in" means provider-specific facts are configuration, not code. The
implementation and testbed use representative interface names, addresses,
prefixes, ASNs, peers, and tunnel endpoints before production values exist.
Adding a connection that uses a supported link and routing type requires a
configuration change and deploy, not a binary change. A production circuit,
prefix authorization, provider credentials, and final endpoints gate only
production activation. A new tunnel protocol that the implementation does
not support still requires its own implementation and acceptance work.

Generic provider onboarding passed its testbed exercise. MWAN-507 requires
implementation and testbed verification before production activation. New ISP
production exercises cannot occur until after October 2026. Their results
require separate acceptance evidence.

The completed migration deferred separate IPv4 and IPv6 health decisions.
That deferral does not apply to MWAN-507: its shared model requires separate
family eligibility before mixed direct and tunneled paths are enabled.

## The five pieces

Each has its own specification. Translation has an implementation plan.
Firewall requires an implementation plan before work begins. The pieces are
listed in dependency order.

**One, the model and the read-only surface.** Define the model for the whole
daemon, bind it, and serve it against today's configuration. Changes no
behavior. Every later piece is verified against it.
[surface.md](surface.md)

**Two, the configuration format.** Inventory renders the model's JSON
encoding, the daemon loads it, and TOML retires. The rendered file becomes
checkable against the schema before it reaches the gateway.
[config.md](config.md)

**Three, the provider set becomes data.** Inventory takes the model's shape,
the daemon checks routing numbers instead of knowing them, each provider
carries a tier and a weight that the daemon turns into the balancing rule, and
the watchdog stops holding a provider list, and the daemon renders the
network manager's unit files from the provider entry. Adding a provider
becomes one inventory entry and a config deploy, with the binary unchanged
and no file authored by hand.
[providers.md](providers.md)

**Four, translation becomes typed instances.** Each family of each provider
declares its translation type, one-to-one mapping works in both families, and
prefixes of differing length follow the standard's rule rather than a
hardcoded length. [translation.md](translation.md)

**Five, the daemon owns the firewall.** The ruleset file is deleted, the
daemon takes the pre-network slot and programs a closed baseline before
anything else, and the kernel backends it needs are declared for load. The
only piece that touches live traffic. [firewall.md](../../firewall.md)

## Out of scope

Writing configuration over the management interface, and the hot reload that
would come with it. Both need a staged-change workflow the library does not
provide.

Quality-based steering, meaning selection on latency, jitter, or loss.

Interface ownership and delegation clients are separate work under MWAN-305.
The [interface ownership specification](../../interfaces.md) defines the
complete migration and its shared contracts with MWAN-507. This provider
configuration specification covers the intermediate networkd rendering stage.
