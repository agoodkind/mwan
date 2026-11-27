package networkjson

// Linux tunnel declarations use the shared WANCONFIG option list. These
// checks turn VXLAN options into values the Linux link driver can use and
// reject settings that the selected tunnel protocol cannot apply.

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"goodkind.io/mwan/gateway/interfaceintent"
)

type tunnelOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type vxlanOptionName string

const (
	vniBits  = 24
	portBits = 16
	tosBits  = 8

	vxlanVNI             vxlanOptionName = "vni"
	vxlanSourcePortMin   vxlanOptionName = "source-port-min"
	vxlanSourcePortMax   vxlanOptionName = "source-port-max"
	vxlanDestinationPort vxlanOptionName = "destination-port"
	vxlanTOS             vxlanOptionName = "tos"
)

var (
	errUnsupportedSITOption = errors.New("6in4 does not support tunnel options")
	errLinuxTunnelProtocol  = errors.New("is not supported by the Linux link driver")
	errDuplicateVXLANOption = errors.New("duplicate VXLAN option")
	errUnknownVXLANOption   = errors.New("unsupported Linux VXLAN option")
	errMissingVXLANOption   = errors.New("missing Linux VXLAN option")
	errVXLANPortRange       = errors.New(
		"VXLAN requires nonzero UDP ports and source-port-min less than source-port-max",
	)
)

type vxlanReceiver struct {
	vni  uint32
	port uint16
}

func validateVXLANReceiver(
	name string,
	settings *interfaceintent.VXLAN,
	receivers map[vxlanReceiver]string,
) error {
	if settings == nil {
		return nil
	}

	// Fixed IPv4 VXLAN receivers share the namespace's VNI and UDP port lookup.
	key := vxlanReceiver{vni: settings.VNI, port: settings.DestinationPort}
	if prior, found := receivers[key]; found {
		return fmt.Errorf(
			"interfaces %s and %s share VXLAN receiver VNI %d and destination UDP port %d",
			prior,
			name,
			key.vni,
			key.port,
		)
	}

	receivers[key] = name

	return nil
}

func buildTunnelVXLAN(name string, wire *linkTunnel) (*interfaceintent.VXLAN, error) {
	options := wire.Options
	if wire.LegacyVXLAN != nil {
		if options != nil {
			return nil, fmt.Errorf("interface %s: %w", name, errMixedTunnelOptions)
		}

		options = wire.LegacyVXLAN.options()
	}

	switch interfaceintent.TunnelProtocol(wire.Protocol) {
	case interfaceintent.TunnelProtocol6in4:
		if len(options) != 0 {
			return nil, fmt.Errorf(
				"interface %s: %w: %q",
				name,
				errUnsupportedSITOption,
				options[0].Name,
			)
		}

		return nil, nil
	case interfaceintent.TunnelProtocolVXLAN:
		settings, err := unmarshalVXLANOptions(options)
		if err != nil {
			slog.Error("Linux tunnel options are invalid", "interface", name, "err", err)

			return nil, fmt.Errorf("interface %s: link/tunnel/options: %w", name, err)
		}

		return settings, nil
	default:
		return nil, fmt.Errorf(
			"interface %s: link/tunnel/protocol %q %w",
			name,
			wire.Protocol,
			errLinuxTunnelProtocol,
		)
	}
}

func unmarshalVXLANOptions(options []tunnelOption) (*interfaceintent.VXLAN, error) {
	var vxlan interfaceintent.VXLAN

	seen := make(map[string]bool, len(options))
	for _, option := range options {
		if seen[option.Name] {
			slog.Error(
				"Linux tunnel repeats a VXLAN option",
				"name",
				option.Name,
				"err",
				errDuplicateVXLANOption,
			)

			return nil, fmt.Errorf("%w %q", errDuplicateVXLANOption, option.Name)
		}

		seen[option.Name] = true

		var err error
		switch vxlanOptionName(option.Name) {
		case vxlanVNI:
			err = setTunnelNumber(option.Value, &vxlan.VNI, vniBits)
		case vxlanSourcePortMin:
			err = setTunnelNumber(option.Value, &vxlan.SourcePortMin, portBits)
		case vxlanSourcePortMax:
			err = setTunnelNumber(option.Value, &vxlan.SourcePortMax, portBits)
		case vxlanDestinationPort:
			err = setTunnelNumber(option.Value, &vxlan.DestinationPort, portBits)
		case vxlanTOS:
			err = setTunnelNumber(option.Value, &vxlan.TOS, tosBits)
		default:
			return nil, fmt.Errorf("%w %q", errUnknownVXLANOption, option.Name)
		}

		if err != nil {
			return nil, fmt.Errorf("VXLAN option %q: %w", option.Name, err)
		}
	}

	for _, name := range []vxlanOptionName{
		vxlanVNI,
		vxlanSourcePortMin,
		vxlanSourcePortMax,
		vxlanDestinationPort,
		vxlanTOS,
	} {
		if !seen[string(name)] {
			return nil, fmt.Errorf("%w %q", errMissingVXLANOption, name)
		}
	}

	if vxlan.SourcePortMin == 0 || vxlan.DestinationPort == 0 ||
		vxlan.SourcePortMin >= vxlan.SourcePortMax {
		return nil, errVXLANPortRange
	}

	return &vxlan, nil
}

func setTunnelNumber[T ~uint8 | ~uint16 | ~uint32](value string, target *T, bits int) error {
	number, err := strconv.ParseUint(value, 10, bits)
	if err != nil {
		slog.Error("Cannot read numeric tunnel option", "bits", bits, "err", err)

		return fmt.Errorf("read %d-bit unsigned integer: %w", bits, err)
	}

	*target = T(number)

	return nil
}

func tunnelOverhead(protocol interfaceintent.TunnelProtocol) uint64 {
	if protocol == interfaceintent.TunnelProtocolVXLAN {
		return interfaceintent.TunnelVXLANOverhead
	}

	return interfaceintent.Tunnel6in4Overhead
}
