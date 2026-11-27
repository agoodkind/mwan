package networkjson

// Connection compilation checks dependencies across interfaces before either
// dataplane uses them. For example, a VLAN needs its parent interface:
//
//	wan.20 (VLAN) -> wan (parent)

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/interfaceintent"
)

const (
	deviceMatchKeyCount = 2
	parentCycleVisiting = 1
	parentCycleComplete = 2
)

var (
	errUndeclaredInterface       = errors.New("is not declared")
	errRejectedInterface         = errors.New("was rejected")
	errMissingRenderedVLANParent = errors.New("needs a rendered networkd link")
	errDuplicateDeviceMatch      = errors.New("match the same device")
	errRejectedParent            = errors.New("requires rejected parent")
	errParentCycle               = errors.New("parent cycle")
)

func compileConnections(
	entries []ifaceEntry,
	ids map[string]connectionid.ID,
	internal string,
	firewall *firewallWire,
) ([]interfaceintent.Connection, []Rejection, error) {
	connections := make([]interfaceintent.Connection, 0, len(entries))
	var rejected []Rejection

	declared := make(map[string]bool, len(entries))
	for _, entry := range entries {
		declared[entry.Name] = true
	}

	if internal != "" && !declared[internal] {
		slog.Error(
			"networkjson: internal interface is missing from declarations",
			"interface",
			internal,
			"err",
			errUndeclaredInterface,
		)

		return nil, nil, fmt.Errorf("internal interface %s %w", internal, errUndeclaredInterface)
	}

	if firewall != nil && firewall.ManagementInterface != "" &&
		!declared[firewall.ManagementInterface] {
		slog.Error(
			"networkjson: management interface is missing from declarations",
			"interface",
			firewall.ManagementInterface,
			"err",
			errUndeclaredInterface,
		)

		return nil, nil, fmt.Errorf(
			"management interface %s %w",
			firewall.ManagementInterface,
			errUndeclaredInterface,
		)
	}

	for _, entry := range entries {
		connection, err := buildConnection(entry, ids[entry.Name])
		if err != nil {
			if entry.WAN == nil || entry.Owner == string(interfaceintent.OwnerMWAN) {
				return nil, nil, err
			}

			rejected = append(rejected, rejectEntry(entry, err))

			continue
		}

		if entry.Name == internal {
			connection.Roles |= interfaceintent.RoleInternal
		}

		if firewall != nil && entry.Name == firewall.ManagementInterface {
			connection.Roles |= interfaceintent.RoleManagement
		}

		connections = append(connections, connection)
	}

	if err := validateConnectionDependencies(
		connections,
		declared,
		internal,
		firewall,
	); err != nil {
		return nil, nil, err
	}

	return connections, rejected, nil
}

func validateConnectionDependencies(
	connections []interfaceintent.Connection,
	declared map[string]bool,
	internal string,
	firewall *firewallWire,
) error {
	indexes := make(map[string]int, len(connections))
	for i, connection := range connections {
		indexes[connection.Name] = i
	}

	if err := validateConnectionDevices(connections); err != nil {
		return err
	}

	if err := validateRequiredRoles(indexes, internal, firewall); err != nil {
		return err
	}

	if err := validateTunnels(connections, declared, indexes); err != nil {
		return err
	}

	for _, connection := range connections {
		if connection.Link == nil {
			continue
		}

		for _, parent := range []string{vlanParent(connection.Link), connection.Link.BridgeMaster} {
			if parent == "" {
				continue
			}

			if !declared[parent] {
				slog.Error(
					"networkjson: interface parent is missing from declarations",
					"interface",
					connection.Name,
					"parent",
					parent,
					"err",
					errUndeclaredInterface,
				)

				return fmt.Errorf(
					"interface %s: parent %s %w",
					connection.Name,
					parent,
					errUndeclaredInterface,
				)
			}

			index, accepted := indexes[parent]
			if !accepted {
				slog.Error(
					"networkjson: interface depends on a rejected parent",
					"interface",
					connection.Name,
					"parent",
					parent,
					"err",
					errRejectedInterface,
				)

				return fmt.Errorf(
					"interface %s: parent %s %w",
					connection.Name,
					parent,
					errRejectedInterface,
				)
			}

			connections[index].Roles |= interfaceintent.RoleParent
		}
	}

	state := make(map[string]uint8, len(connections))
	for _, connection := range connections {
		if err := checkParentCycle(connection.Name, connections, indexes, state); err != nil {
			return err
		}
	}

	for _, connection := range connections {
		if connection.Link == nil || connection.Link.VLAN == nil ||
			connection.Owner != interfaceintent.OwnerNetworkd {
			continue
		}

		parent := connections[indexes[connection.Link.VLAN.Parent]]
		if parent.Owner != interfaceintent.OwnerNetworkd || parent.Link == nil {
			slog.Error(
				"networkjson: VLAN parent lacks a rendered networkd link",
				"interface",
				connection.Name,
				"parent",
				parent.Name,
				"err",
				errMissingRenderedVLANParent,
			)

			return fmt.Errorf(
				"interface %s: vlan parent %s %w",
				connection.Name,
				parent.Name,
				errMissingRenderedVLANParent,
			)
		}
	}

	return nil
}

func validateRequiredRoles(indexes map[string]int, internal string, firewall *firewallWire) error {
	if _, ok := indexes[internal]; internal != "" && !ok {
		slog.Error(
			"networkjson: internal interface failed validation",
			"interface",
			internal,
			"err",
			errRejectedInterface,
		)

		return fmt.Errorf("internal interface %s %w", internal, errRejectedInterface)
	}

	if firewall != nil && firewall.ManagementInterface != "" {
		if _, ok := indexes[firewall.ManagementInterface]; !ok {
			slog.Error(
				"networkjson: management interface failed validation",
				"interface",
				firewall.ManagementInterface,
				"err",
				errRejectedInterface,
			)

			return fmt.Errorf(
				"management interface %s %w",
				firewall.ManagementInterface,
				errRejectedInterface,
			)
		}
	}

	return nil
}

func validateConnectionDevices(connections []interfaceintent.Connection) error {
	devices := make(map[string]string, len(connections))
	for _, connection := range connections {
		if connection.Link == nil {
			continue
		}

		keys := make([]string, 0, deviceMatchKeyCount)
		if connection.Link.Match.Driver != "" {
			keys = append(keys, "driver:"+connection.Link.Match.Driver)
		}

		if connection.Link.Match.HardwareAddress != "" {
			keys = append(keys, "mac:"+strings.ToLower(connection.Link.Match.HardwareAddress))
		}

		for _, key := range keys {
			if prior, taken := devices[key]; taken {
				slog.Error(
					"networkjson: interface device match is already in use",
					"interface",
					connection.Name,
					"other_interface",
					prior,
					"device",
					key,
					"err",
					errDuplicateDeviceMatch,
				)

				return fmt.Errorf(
					"interfaces %s and %s %w %q",
					prior,
					connection.Name,
					errDuplicateDeviceMatch,
					key,
				)
			}

			devices[key] = connection.Name
		}
	}

	return nil
}

func vlanParent(link *interfaceintent.Link) string {
	if link.VLAN == nil {
		return ""
	}

	return link.VLAN.Parent
}

func validateActiveDependencies(connections []interfaceintent.Connection) error {
	available := make(map[string]bool, len(connections))
	for _, connection := range connections {
		available[connection.Name] = true
	}

	for _, connection := range connections {
		if connection.Link == nil {
			continue
		}

		for _, parent := range linkDependencies(connection.Link) {
			if parent != "" && !available[parent] {
				slog.Error(
					"networkjson: active interface requires an unavailable parent",
					"interface",
					connection.Name,
					"parent",
					parent,
					"err",
					errRejectedParent,
				)

				return fmt.Errorf("interface %s %w %s", connection.Name, errRejectedParent, parent)
			}
		}
	}

	return nil
}

func checkParentCycle(
	name string,
	connections []interfaceintent.Connection,
	indexes map[string]int,
	state map[string]uint8,
) error {
	if state[name] == parentCycleComplete {
		return nil
	}

	if state[name] == parentCycleVisiting {
		slog.Error(
			"networkjson: interface parent dependency forms a cycle",
			"interface",
			name,
			"err",
			errParentCycle,
		)

		return fmt.Errorf("interface %s: %w", name, errParentCycle)
	}

	state[name] = parentCycleVisiting
	connection := connections[indexes[name]]
	if connection.Link != nil {
		for _, parent := range linkDependencies(connection.Link) {
			if parent == "" {
				continue
			}

			if err := checkParentCycle(parent, connections, indexes, state); err != nil {
				return err
			}
		}
	}

	state[name] = parentCycleComplete

	return nil
}
