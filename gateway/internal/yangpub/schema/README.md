# Update the embedded YANG schema

The gateway and the OpenTofu provider embed the same YANG module files.
The embedded set contains one revision of each module. Libyang selects the
newest revision in a search directory.

The seven standard modules use the source at
[YangModels/yang at commit 6795d9c](https://github.com/YangModels/yang/tree/6795d9c680bc77fe706b3be9557d8adf12901dcc).
Their headers retain the IETF Trust or IANA copyright notices.
`goodkind-mwan-steering` defines the custom steering model.

1. Replace the module file from its upstream path.
2. Update the upstream commit reference above.
3. Update [the module list](schema.go) if the filename, enabled features, or
   installation order changes.
4. Run `make -C gateway yang-validate yang-validate-instances` from the
   repository root.
