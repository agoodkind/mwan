package moduleconfig

import (
	"errors"
	"fmt"
	"log/slog"

	"go.fd.io/govpp/binapi/ethernet_types"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/ip_types"
	"go.fd.io/govpp/binapi/nat64"
	"go.fd.io/govpp/binapi/tapv2"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	vppmodule "goodkind.io/mwan/internal/ifmgr/modules/vpp"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/vpp"
)

var (
	errVPPMetadata    = errors.New("router VPP requires network metadata")
	errTAPConnections = errors.New("requires declared VPP and host connections")
	errTAPName        = errors.New("requires interface name")
	errRXMode         = errors.New("has unsupported RX mode")
	errTAPIPv4        = errors.New("requires one IPv4 address when IPv4 is present")
	errTAPIPv6        = errors.New("requires one IPv6 address when IPv6 is present")
)

func buildVPPConfig(settings config.IfMgrSection) (vppmodule.Config, error) {
	section := settings.Modules.VPP
	if section == nil {
		return vppmodule.Config{Settings: nil, Policy: nil}, nil
	}

	if settings.VPPPolicy == nil {
		return vppmodule.Config{
			Settings: nil,
			Policy:   nil,
		}, errVPPMetadata
	}

	endpoints, err := section.Endpoints()
	if err != nil {
		slog.Warn("VPP transport settings are invalid.", "err", err)

		return vppmodule.Config{
			Settings: nil,
			Policy:   nil,
		}, fmt.Errorf("VPP module transport settings: %w", err)
	}

	runtime := &vpp.Settings{
		Endpoints: endpoints,
		StateFile: section.StateFile,
		TAPs:      nil,
		NAT64:     nil,
	}

	connections := make(map[string]interfaceintent.Connection, len(settings.VPPPolicy.Connections))
	for _, connection := range settings.VPPPolicy.Connections {
		connections[connection.ID.String()] = connection
	}

	for _, resource := range section.Interface {
		connection, found := connections[resource.ConnectionID]
		host, hostFound := connections[resource.HostConnectionID]
		if !found || !hostFound {
			return vppmodule.Config{
				Settings: nil,
				Policy:   nil,
			}, fmt.Errorf("VPP TAP %d %w", resource.ID, errTAPConnections)
		}

		if connection.Name != fmt.Sprintf("tap%d", resource.ID) {
			return vppmodule.Config{
				Settings: nil,
				Policy:   nil,
			}, fmt.Errorf("VPP TAP %d %w tap%d", resource.ID, errTAPName, resource.ID)
		}

		request, err := buildTAPRequest(resource, connection, host)
		if err != nil {
			return vppmodule.Config{Settings: nil, Policy: nil}, err
		}

		var mode interface_types.RxMode
		switch resource.RXMode {
		case config.VPPRXPolling:
			mode = interface_types.RX_MODE_API_POLLING
		case config.VPPRXInterrupt:
			mode = interface_types.RX_MODE_API_INTERRUPT
		case config.VPPRXAdaptive:
			mode = interface_types.RX_MODE_API_ADAPTIVE
		default:
			return vppmodule.Config{
				Settings: nil,
				Policy:   nil,
			}, fmt.Errorf("VPP TAP %d %w %q", resource.ID, errRXMode, resource.RXMode)
		}

		runtime.TAPs = append(
			runtime.TAPs,
			vpp.TAP{
				ConnectionID: resource.ConnectionID,
				Create:       request,
				RXMode:       mode,
				Worker:       resource.Worker,
			},
		)
	}

	if section.NAT64 != nil {
		resources := section.NAT64
		runtime.NAT64 = &nat64.Nat64PluginEnableDisable{
			BibBuckets:    resources.BIBBuckets,
			BibMemorySize: resources.BIBMemorySize,
			StBuckets:     resources.STBuckets,
			StMemorySize:  resources.STMemorySize,
			Enable:        true,
		}
	}

	return vppmodule.Config{Settings: runtime, Policy: settings.VPPPolicy}, nil
}

func addVPPModuleConfig(
	configs ifmgr.ModuleConfigSet,
	enabled bool,
	settings config.IfMgrSection,
) error {
	if !enabled {
		return nil
	}

	moduleConfig, err := buildVPPConfig(settings)
	if err != nil {
		return err
	}

	configs["vpp"] = moduleConfig

	return nil
}

func buildTAPRequest(
	resource config.VPPTAPSection,
	connection interfaceintent.Connection,
	host interfaceintent.Connection,
) (tapv2.TapCreateV3, error) {
	request := tapv2.TapCreateV3{
		ID:            resource.ID,
		UseRandomMac:  true,
		HostIfNameSet: true,
		HostIfName:    host.Name,
		NumRxQueues:   resource.RXQueues,
		NumTxQueues:   resource.TXQueues,
		RxRingSz:      resource.RXRingSize,
		TxRingSz:      resource.TXRingSize,
	}
	if connection.Link != nil && connection.Link.HardwareAddress != "" {
		mac, err := ethernet_types.ParseMacAddress(connection.Link.HardwareAddress)
		if err != nil {
			slog.Warn("The VPP TAP has an invalid MAC address.", "tap_id", resource.ID, "err", err)

			return request, fmt.Errorf("VPP TAP %d MAC: %w", resource.ID, err)
		}

		request.UseRandomMac, request.MacAddress = false, mac
	}

	if host.Link != nil {
		if host.Link.MTU != nil {
			request.HostMtuSet, request.HostMtuSize = true, *host.Link.MTU
		}

		if host.Link.HardwareAddress != "" {
			mac, err := ethernet_types.ParseMacAddress(host.Link.HardwareAddress)
			if err != nil {
				slog.Warn(
					"The VPP TAP host has an invalid MAC address.",
					"tap_id",
					resource.ID,
					"err",
					err,
				)

				return request, fmt.Errorf("VPP TAP %d host MAC: %w", resource.ID, err)
			}

			request.HostMacAddrSet, request.HostMacAddr = true, mac
		}
	}

	if host.IPv4 != nil {
		if len(host.IPv4.Addresses) != 1 {
			return request, fmt.Errorf("VPP TAP %d host %s %w", resource.ID, host.Name, errTAPIPv4)
		}

		prefix, err := ip_types.ParseIP4Prefix(host.IPv4.Addresses[0].Prefix.String())
		if err != nil {
			slog.Warn(
				"The VPP TAP host has an invalid IPv4 prefix.",
				"tap_id",
				resource.ID,
				"err",
				err,
			)

			return request, fmt.Errorf("VPP TAP %d host IPv4: %w", resource.ID, err)
		}

		request.HostIP4PrefixSet, request.HostIP4Prefix = true, ip_types.IP4AddressWithPrefix(
			prefix,
		)
	}

	if host.IPv6 != nil {
		if len(host.IPv6.Addresses) != 1 {
			return request, fmt.Errorf("VPP TAP %d host %s %w", resource.ID, host.Name, errTAPIPv6)
		}

		prefix, err := ip_types.ParseIP6Prefix(host.IPv6.Addresses[0].Prefix.String())
		if err != nil {
			slog.Warn(
				"The VPP TAP host has an invalid IPv6 prefix.",
				"tap_id",
				resource.ID,
				"err",
				err,
			)

			return request, fmt.Errorf(
				"VPP TAP %d has an invalid host IPv6 prefix: %w",
				resource.ID,
				err,
			)
		}

		request.HostIP6PrefixSet, request.HostIP6Prefix = true, ip_types.IP6AddressWithPrefix(
			prefix,
		)
	}

	return request, nil
}
