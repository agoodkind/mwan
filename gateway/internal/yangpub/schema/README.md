# The gateway's schema

These eight modules are the gateway's data model. The binary embeds them, so
the model a gateway serves is the model the release carries and a deploy
cannot pair a binary with a different schema.

`goodkind-mwan-steering` is this repository's own. The other seven are copied
unchanged from [YangModels/yang](https://github.com/YangModels/yang) at commit
`6795d9c680bc77fe706b3be9557d8adf12901dcc`, keeping their upstream names, which
carry the revision date. Each states its own IETF Trust or IANA copyright in
its header.

This directory holds exactly one revision of each module, which is why the
schema gates and the loader can name a directory rather than a file list:
libyang prefers the newest revision it finds in a search directory, and here
there is only one.

To move a module to a newer revision, replace the file from the same upstream
path, update the commit above, and update `SchemaModules` in
[schema.go](../schema.go) if the file name changed.
