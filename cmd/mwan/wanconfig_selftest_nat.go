package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"goodkind.io/mwan/internal/wanconfig"
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
		`"example/ipv4"`: {family: "ipv4", mode: "napt44"},
		`"example/ipv6"`: {family: "ipv6", mode: "nptv6"},
	}
	if len(entries) != len(expected) {
		return fmt.Errorf("nat instances = %d, want %d", len(entries), len(expected))
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
			return fmt.Errorf("unexpected or duplicate NAT instance %s", name)
		}
		seen[name] = true
		id := wanconfig.TranslationInstanceID("example", want.family)
		if err := expectLeaf(instance, "id", strconv.FormatUint(uint64(id), 10), "stable identity"); err != nil {
			return err
		}
		if err := expectLeaf(instance, "type", `"`+want.mode+`"`, "configuration"); err != nil {
			return err
		}
		if err := expectLeaf(instance, "goodkind-mwan-steering:kernel-present", "true", "live state"); err != nil {
			return err
		}
		if want.family == "ipv6" {
			if _, present := instance["policy"]; present {
				return errors.New("delegated NPT published a configured prefix policy")
			}
		}
	}
	return nil
}

func checkSelftestTranslation(log *slog.Logger, member map[string]json.RawMessage) error {
	ipv6, err := unmarshalObject(log, member["ietf-ip:ipv6"], "IPv6 container")
	if err != nil {
		return err
	}
	translation, err := unmarshalObject(log, ipv6["goodkind-mwan-steering:translation"], "IPv6 translation")
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
	if err := expectLeaf(npt, "expected-prefix", "\"2001:db8:a::/60\"", "configuration"); err != nil {
		return err
	}
	if _, present := npt["external-prefix"]; present {
		return errors.New("delegated intent includes an external prefix")
	}
	state, err := unmarshalObject(log, translation["state"], "translation state")
	if err != nil {
		return err
	}
	if err := expectLeaf(state, "resolved-external-prefix", "\"2001:db8:b::/60\"", "live state"); err != nil {
		return err
	}
	return expectLeaf(state, "ready", "true", "live state")
}
