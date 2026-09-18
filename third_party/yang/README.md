# Vendored YANG modules

These are the IETF and IANA modules the gateway's own model imports, copied
unchanged from [YangModels/yang](https://github.com/YangModels/yang) at commit
`6795d9c680bc77fe706b3be9557d8adf12901dcc`. Each file keeps its upstream path
under `standard/` and its upstream name, which carries the revision date. Each
module states its own IETF Trust or IANA copyright in its header.

They are copied rather than fetched because every consumer names an exact
revision: the `yang-validate` and `yang-validate-instances` gates, the
gateway's schema loader, and the tests that assemble a model directory. A
checkout of the upstream repository also carries later revisions of the same
modules, and libyang prefers the newest revision it finds in a search
directory, so vendoring only the pinned files removes that ambiguity.

Two revisions of the interface-type registry are here because two consumers
need different ones. `standard/ietf/RFC/iana-if-type@2014-05-08.yang` is what
the instance gate and the network schema tests parse.
`standard/iana/iana-if-type@2026-03-17.yang` is what the gateway installs and
what the wanconfig selftest exercises against real sysrepo.

To move a module to a newer revision, copy the new file from the same upstream
path, update the commit above, and update every consumer that names the old
revision.
