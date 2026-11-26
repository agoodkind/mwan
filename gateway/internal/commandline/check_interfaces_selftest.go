package commandline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"goodkind.io/mwan/gateway/internal/yangpub"
)

func unmarshalObject(
	log *slog.Logger,
	raw json.RawMessage,
	what string,
) (map[string]json.RawMessage, error) {
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, failStep(log, "decode "+what, err)
	}

	return object, nil
}

func unmarshalArray(log *slog.Logger, raw json.RawMessage, what string) ([]json.RawMessage, error) {
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err != nil {
		return nil, failStep(log, "decode "+what, err)
	}

	return array, nil
}

func expectLeaf(object map[string]json.RawMessage, name string, want string, where string) error {
	got, present := object[name]
	if !present {
		return selftestInvariant(fmt.Sprintf("%s: %s is absent", where, name))
	}

	if string(got) != want {
		return selftestInvariant(fmt.Sprintf("%s: %s = %s, want %s", where, name, got, want))
	}

	return nil
}

func checkSelftestTree(ctx context.Context, log *slog.Logger, reader yangpub.Publisher) error {
	interfacesJSON, found, err := reader.ExportJSON(
		ctx,
		yangpub.DatastoreOperational,
		"/ietf-interfaces:*",
	)
	if err != nil {
		return failStep(log, "operational interfaces read", err)
	}

	if !found {
		return selftestInvariant("operational interfaces read: nothing served")
	}

	if err := checkSelftestInterfaces(log, json.RawMessage(interfacesJSON)); err != nil {
		return failStep(log, "interfaces tree: "+interfacesJSON, err)
	}

	natJSON, found, err := reader.ExportJSON(ctx, yangpub.DatastoreOperational, "/ietf-nat:*")
	if err != nil {
		return failStep(log, "operational nat read", err)
	}

	if !found {
		return selftestInvariant("operational nat read: nothing served")
	}

	if err := checkSelftestNAT(log, json.RawMessage(natJSON)); err != nil {
		return failStep(log, "nat tree: "+natJSON, err)
	}

	return nil
}

func checkSelftestInterfaces(log *slog.Logger, tree json.RawMessage) error {
	interfaces, member, err := selftestInterfaceMember(log, tree)
	if err != nil {
		return err
	}

	return checkSelftestInterfaceState(log, interfaces, member)
}

func selftestInterfaceMember(
	log *slog.Logger,
	tree json.RawMessage,
) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	root, err := unmarshalObject(log, tree, "interfaces tree")
	if err != nil {
		return nil, nil, err
	}

	interfaces, err := unmarshalObject(
		log,
		root["ietf-interfaces:interfaces"],
		"interfaces container",
	)
	if err != nil {
		return nil, nil, err
	}

	entries, err := unmarshalArray(log, interfaces["interface"], "interface list")
	if err != nil {
		return nil, nil, err
	}

	var member map[string]json.RawMessage

	for _, entry := range entries {
		decoded, err := unmarshalObject(log, entry, "interface entry")
		if err != nil {
			return nil, nil, err
		}

		if string(decoded["name"]) == strconv.Quote(selftestProviderIface) {
			member = decoded
		}
	}

	if member == nil {
		return nil, nil, selftestInvariant("interface enexample0 is absent")
	}

	return interfaces, member, nil
}

func checkSelftestInterfaceState(
	log *slog.Logger,
	interfaces map[string]json.RawMessage,
	member map[string]json.RawMessage,
) error {
	steering, err := unmarshalObject(
		log,
		member["goodkind-mwan-steering:steering"],
		"steering container",
	)
	if err != nil {
		return err
	}

	if err := expectLeaf(steering, "tier", "0", "configuration"); err != nil {
		return err
	}

	if err := expectLeaf(
		steering,
		"probe-policy",
		strconv.Quote(selftestProviderName),
		"configuration",
	); err != nil {
		return err
	}

	state, err := unmarshalObject(log, steering["state"], "steering state")
	if err != nil {
		return err
	}

	if err := expectLeaf(state, "health", `"healthy"`, "live state"); err != nil {
		return err
	}

	if err := expectLeaf(state, "carrying", "true", "live state"); err != nil {
		return err
	}

	if err := checkSelftestTranslation(log, member); err != nil {
		return err
	}

	if err := checkSelftestOwnedAddresses(log, member); err != nil {
		return err
	}

	group, err := unmarshalObject(
		log,
		interfaces["goodkind-mwan-steering:steering-group"],
		"steering group",
	)
	if err != nil {
		return err
	}

	groupState, err := unmarshalObject(log, group["state"], "steering group state")
	if err != nil {
		return err
	}

	if err := expectLeaf(groupState, "active-tier", "0", "live state"); err != nil {
		return err
	}

	if _, present := groupState["intended-ruleset"]; !present {
		return selftestInvariant("live state: intended-ruleset is absent")
	}

	if err := checkSelftestRejectedProviders(log, groupState); err != nil {
		return err
	}

	return nil
}

func checkSelftestRejectedProviders(
	log *slog.Logger,
	groupState map[string]json.RawMessage,
) error {
	entries, err := unmarshalArray(log, groupState["rejected-provider"], "rejected provider list")
	if err != nil {
		return err
	}

	if len(entries) != 1 {
		return selftestInvariant(
			fmt.Sprintf(
				"live state: rejected-provider = %s, want one entry",
				groupState["rejected-provider"],
			),
		)
	}

	rejected, err := unmarshalObject(log, entries[0], "rejected provider entry")
	if err != nil {
		return err
	}

	if err := expectLeaf(rejected, "interface", `"enrejected0"`, "rejected provider"); err != nil {
		return err
	}

	if err := expectLeaf(
		rejected,
		"provider",
		`"rejected-example"`,
		"rejected provider",
	); err != nil {
		return err
	}

	return expectLeaf(rejected, "reason", `"ipv6/dhcp is required"`, "rejected provider")
}

func checkSelftestOwnedAddresses(log *slog.Logger, member map[string]json.RawMessage) error {
	wan, err := unmarshalObject(log, member["goodkind-mwan-steering:wan"], "wan container")
	if err != nil {
		return err
	}

	if err := expectLeaf(
		wan,
		"name",
		strconv.Quote(selftestProviderName),
		"live state",
	); err != nil {
		return err
	}

	ipv4, err := unmarshalObject(log, member["ietf-ip:ipv4"], "IPv4 container")
	if err != nil {
		return err
	}

	translation, err := unmarshalObject(
		log,
		ipv4["goodkind-mwan-steering:translation"],
		"IPv4 translation",
	)
	if err != nil {
		return err
	}

	mappings, err := unmarshalArray(log, translation["static-mapping"], "static-mapping list")
	if err != nil {
		return err
	}

	if len(mappings) != 1 {
		return selftestInvariant(
			fmt.Sprintf(
				"configuration: static-mapping = %s, want one entry",
				translation["static-mapping"],
			),
		)
	}

	mapping, err := unmarshalObject(log, mappings[0], "static-mapping entry")
	if err != nil {
		return err
	}

	if err := expectLeaf(
		mapping,
		"external",
		`"`+selftestOwnedAddress+`"`,
		"configuration",
	); err != nil {
		return err
	}

	owned, err := unmarshalArray(log, wan["owned-address"], "owned-address leaf-list")
	if err != nil {
		return err
	}

	want := `"` + selftestOwnedAddress + `"`
	if len(owned) != 1 || string(owned[0]) != want {
		return selftestInvariant(
			fmt.Sprintf("live state: owned-address = %s, want [%s]", wan["owned-address"], want),
		)
	}

	return nil
}

func checkSelftestInterfacesBare(log *slog.Logger, tree json.RawMessage) error {
	root, err := unmarshalObject(log, tree, "interfaces tree")
	if err != nil {
		return err
	}

	raw, present := root["ietf-interfaces:interfaces"]
	if !present {
		return nil
	}

	interfaces, err := unmarshalObject(log, raw, "interfaces container")
	if err != nil {
		return err
	}

	rawList, present := interfaces["interface"]
	if !present {
		return nil
	}

	entries, err := unmarshalArray(log, rawList, "interface list")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		decoded, err := unmarshalObject(log, entry, "interface entry")
		if err != nil {
			return err
		}

		rawSteering, present := decoded["goodkind-mwan-steering:steering"]
		if !present {
			continue
		}

		steering, err := unmarshalObject(log, rawSteering, "steering container")
		if err != nil {
			return err
		}

		if _, present := steering["tier"]; present {
			return selftestInvariant("configuration still enabled after close")
		}

		if _, present := steering["state"]; present {
			return selftestInvariant("live state still served after close")
		}
	}

	return nil
}
