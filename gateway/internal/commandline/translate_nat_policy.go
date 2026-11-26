package commandline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"goodkind.io/mwan/gateway/internal/wanconfig"
)

func checkSelftestNAT(log *slog.Logger, tree json.RawMessage) error {
	root, err := unmarshalObject(log, tree, "nat tree")
	if err != nil {
		return err
	}

	nat, err := unmarshalObject(log, root["ietf-nat:nat"], "nat container")
	if err != nil {
		return err
	}

	instances, err := unmarshalObject(log, nat["instances"], "instances container")
	if err != nil {
		return err
	}

	entries, err := unmarshalArray(log, instances["instance"], "instance list")
	if err != nil {
		return err
	}

	expected := map[string]struct{ family, mode string }{
		strconv.Quote(selftestProviderName + "/ipv4"): {
			family: "ipv4",
			mode:   "napt44",
		},
		strconv.Quote(selftestProviderName + "/" + selftestIPv6Family): {
			family: selftestIPv6Family,
			mode:   "nptv6",
		},
	}
	if len(entries) != len(expected) {
		return selftestInvariant(
			"nat instances = " + strconv.Itoa(len(entries)) + ", want " +
				strconv.Itoa(len(expected)),
		)
	}

	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		instance, err := unmarshalObject(log, entry, "nat instance")
		if err != nil {
			return err
		}

		name := string(instance["name"])
		want, found := expected[name]
		if !found || seen[name] {
			return selftestInvariant("unexpected or duplicate NAT instance " + name)
		}

		seen[name] = true
		id := wanconfig.TranslationInstanceID(selftestProviderName, want.family)
		if err := expectLeaf(
			instance,
			"id",
			strconv.FormatUint(uint64(id), 10),
			"stable identity",
		); err != nil {
			return err
		}

		if err := expectLeaf(instance, "type", `"`+want.mode+`"`, "configuration"); err != nil {
			return err
		}

		if err := expectLeaf(
			instance,
			"goodkind-mwan-steering:kernel-present",
			"true",
			"live state",
		); err != nil {
			return err
		}

		if want.family == "ipv4" {
			if err := checkSelftestNATMapping(log, instance); err != nil {
				return err
			}
		} else if err := checkSelftestNPTPolicy(log, instance); err != nil {
			return err
		}
	}

	return nil
}

func checkSelftestNATMapping(log *slog.Logger, instance map[string]json.RawMessage) error {
	table, err := unmarshalObject(log, instance["mapping-table"], "NAT mapping table")
	if err != nil {
		return err
	}

	entries, err := unmarshalArray(log, table["mapping-entry"], "NAT mapping entries")
	if err != nil {
		return err
	}

	if len(entries) != 1 {
		return selftestInvariant(fmt.Sprintf("NAT mapping entries = %d, want 1", len(entries)))
	}

	mapping, err := unmarshalObject(log, entries[0], "NAT mapping entry")
	if err != nil {
		return err
	}

	if err := expectLeaf(mapping, "index", "1", "NAT mapping"); err != nil {
		return err
	}

	if err := expectLeaf(mapping, "type", `"static"`, "NAT mapping"); err != nil {
		return err
	}

	if err := expectLeaf(
		mapping,
		"internal-src-address",
		`"192.0.2.3/32"`,
		"NAT mapping",
	); err != nil {
		return err
	}

	return expectLeaf(mapping, "external-src-address", `"203.0.113.3/32"`, "NAT mapping")
}

func checkSelftestNPTPolicy(log *slog.Logger, instance map[string]json.RawMessage) error {
	policies, err := unmarshalArray(log, instance["policy"], "NPT policies")
	if err != nil {
		return err
	}

	if len(policies) != 1 {
		return selftestInvariant(fmt.Sprintf("NPT policies = %d, want 1", len(policies)))
	}

	policy, err := unmarshalObject(log, policies[0], "NPT policy")
	if err != nil {
		return err
	}

	if err := expectLeaf(policy, "id", "1", "NPT policy"); err != nil {
		return err
	}

	prefixes, err := unmarshalArray(log, policy["nptv6-prefixes"], "NPT prefix pairs")
	if err != nil {
		return err
	}

	if len(prefixes) != 1 {
		return selftestInvariant(fmt.Sprintf("NPT prefix pairs = %d, want 1", len(prefixes)))
	}

	prefix, err := unmarshalObject(log, prefixes[0], "NPT prefix pair")
	if err != nil {
		return err
	}

	if err := expectLeaf(
		prefix,
		"internal-ipv6-prefix",
		`"3d06:bad:b01:210::/60"`,
		"NPT prefix pair",
	); err != nil {
		return err
	}

	return expectLeaf(prefix, "external-ipv6-prefix", `"2001:db8:b::/60"`, "NPT prefix pair")
}

func checkSelftestTranslation(log *slog.Logger, member map[string]json.RawMessage) error {
	ipv6, err := unmarshalObject(log, member["ietf-ip:ipv6"], "IPv6 container")
	if err != nil {
		return err
	}

	translation, err := unmarshalObject(
		log,
		ipv6["goodkind-mwan-steering:translation"],
		"IPv6 translation",
	)
	if err != nil {
		return err
	}

	npt, err := unmarshalObject(log, translation["nptv6"], "NPT intent")
	if err != nil {
		return err
	}

	if err := expectLeaf(npt, "external-source", "\"delegated\"", "configuration"); err != nil {
		return err
	}

	if err := expectLeaf(
		npt,
		"expected-prefix",
		"\"2001:db8:a::/60\"",
		"configuration",
	); err != nil {
		return err
	}

	if _, present := npt["external-prefix"]; present {
		return selftestInvariant("delegated intent includes an external prefix")
	}

	state, err := unmarshalObject(log, translation["state"], "translation state")
	if err != nil {
		return err
	}

	if err := expectLeaf(
		state,
		"resolved-external-prefix",
		"\"2001:db8:b::/60\"",
		"live state",
	); err != nil {
		return err
	}

	return expectLeaf(state, "ready", "true", "live state")
}
