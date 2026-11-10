package vpp

import (
	"fmt"
	"strings"

	"go.fd.io/govpp/binapi/interface_types"

	"goodkind.io/mwan/internal/interfaceintent"
)

type resourceConnections struct {
	byID    map[string]interfaceintent.Connection
	byName  map[string]interfaceintent.Connection
	managed map[string]bool
	hosts   map[string]bool
}

// Validate requires complete policy and TAP resource mappings before VPP writes begin.
func (controller *Controller) Validate(policy Policy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	connections := indexResourceConnections(policy.Connections, len(controller.settings.TAPs))
	for _, resource := range controller.settings.TAPs {
		if err := connections.addTAP(resource); err != nil {
			return err
		}
	}
	for _, connection := range policy.Connections {
		if err := connections.validateConnectionJoin(connection); err != nil {
			return err
		}
	}
	for _, neighbor := range policy.Neighbors {
		if !connections.managed[neighbor.Interface] {
			return fmt.Errorf("VPP neighbor %s requires a managed VPP interface", neighbor.Address)
		}
	}
	return controller.validateNAT64Resources(policy.NAT64, connections.managed)
}

func indexResourceConnections(connections []interfaceintent.Connection, resourceCount int) resourceConnections {
	index := resourceConnections{
		byID:    make(map[string]interfaceintent.Connection, len(connections)),
		byName:  make(map[string]interfaceintent.Connection, len(connections)),
		managed: make(map[string]bool, resourceCount),
		hosts:   make(map[string]bool, resourceCount),
	}
	for _, connection := range connections {
		index.byID[connection.ID.String()] = connection
		index.byName[connection.Name] = connection
	}
	return index
}

func (connections resourceConnections) addTAP(resource TAP) error {
	connection, declared := connections.byID[resource.ConnectionID]
	name := fmt.Sprintf("tap%d", resource.Create.ID)
	if !declared || connection.Name != name || connections.managed[name] {
		return fmt.Errorf("VPP TAP %d requires a unique declared VPP connection", resource.Create.ID)
	}
	if err := validateTAPHostName(resource); err != nil {
		return err
	}
	hostName := resource.Create.HostIfName
	if _, declared := connections.byName[hostName]; !declared || !resource.Create.HostIfNameSet || connections.hosts[hostName] || hostName == name {
		return fmt.Errorf("VPP TAP %d requires a distinct declared host connection", resource.Create.ID)
	}
	if err := validateTAPQueues(resource); err != nil {
		return err
	}
	connections.managed[name] = true
	connections.hosts[hostName] = true
	return nil
}

func validateTAPHostName(resource TAP) error {
	name := resource.Create.HostIfName
	if len(name) == 0 || len(name) > 15 || name == "." || name == ".." || strings.ContainsAny(name, "/: \t\n\v\f\r") {
		return fmt.Errorf("VPP TAP %d requires a Linux host interface name of 1 to 15 bytes", resource.Create.ID)
	}
	return nil
}

func validateTAPQueues(resource TAP) error {
	if resource.Create.NumRxQueues == 0 || resource.Create.NumTxQueues == 0 || resource.Create.RxRingSz == 0 || resource.Create.TxRingSz == 0 {
		return fmt.Errorf("VPP TAP %d requires positive ring sizes and queue counts", resource.Create.ID)
	}
	if resource.RXMode != interface_types.RX_MODE_API_POLLING && resource.RXMode != interface_types.RX_MODE_API_INTERRUPT && resource.RXMode != interface_types.RX_MODE_API_ADAPTIVE {
		return fmt.Errorf("VPP TAP %d requires an explicit RX mode", resource.Create.ID)
	}
	return nil
}

func (connections resourceConnections) validateConnectionJoin(connection interfaceintent.Connection) error {
	if !connections.managed[connection.Name] && !connections.hosts[connection.Name] {
		return fmt.Errorf("VPP connection %s requires a TAP resource mapping", connection.Name)
	}
	if !connections.hosts[connection.Name] {
		return nil
	}
	if connections.managed[connection.Name] {
		return fmt.Errorf("VPP connection %s cannot serve as both TAP and host interface", connection.Name)
	}
	for _, family := range []*interfaceintent.Family{ipv4Family(connection.IPv4), ipv6Family(connection.IPv6)} {
		if family != nil && len(family.Routes) != 0 {
			return fmt.Errorf("VPP host connection %s cannot configure VPP routes", connection.Name)
		}
	}
	return nil
}

func (controller *Controller) validateNAT64Resources(policy *NAT64Policy, managed map[string]bool) error {
	if policy == nil {
		return nil
	}
	if !managed[policy.Inside] || !managed[policy.Outside] {
		return fmt.Errorf("NAT64 inside and outside interfaces require TAP resource mappings")
	}
	resources := controller.settings.NAT64
	if resources == nil || resources.BibBuckets == 0 || resources.BibMemorySize == 0 || resources.StBuckets == 0 || resources.StMemorySize == 0 {
		return fmt.Errorf("NAT64 policy requires positive BIB and session table resource sizes")
	}
	return nil
}
