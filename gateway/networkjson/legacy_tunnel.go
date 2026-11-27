package networkjson

// WANCONFIG loading can convert a legacy Linux tunnel document when the
// current YANG schema needs named options.
//
//	vxlan.vni = 42 -> options entry {name: "vni", value: "42"}

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	interfaceList = "ietf-interfaces:interfaces.interface"
	linuxTunnel   = "goodkind-mwan-steering:link.tunnel"
)

var (
	errMixedTunnelOptions = errors.New(
		"configure tunnel options only once; remove the legacy vxlan block",
	)
	errLegacyVXLANObject  = errors.New("the vxlan object requires protocol vxlan")
	errLegacyOptionNumber = errors.New("legacy VXLAN option requires an integer")
)

// UnmarshalTunnelOptions converts legacy VXLAN members to named options.
//
// The loader uses the converted document when an older file requires the new
// YANG schema. Other members retain their original JSON representation.
//
// A loader can validate the current representation with the installed schema:
//
//	updated, _, err := networkjson.UnmarshalTunnelOptions(data)
//	if err != nil {
//		return err
//	}
//	return schema.ValidateConfigJSON(updated)
func UnmarshalTunnelOptions(data []byte) ([]byte, bool, error) {
	changed := false

	for index, entry := range gjson.GetBytes(data, interfaceList).Array() {
		tunnel := entry.Get(linuxTunnel)
		legacy := tunnel.Get("vxlan")
		if legacy.Type == gjson.Null {
			continue
		}

		options, err := unmarshalVXLANMembers(tunnel, legacy)
		if err != nil {
			slog.Error(
				"Cannot read legacy tunnel settings",
				"interface",
				entry.Get("name").String(),
				"err",
				err,
			)

			return nil, false, fmt.Errorf("interface %s: %w", entry.Get("name").String(), err)
		}

		path := interfaceList + "." + strconv.Itoa(index) + "." + linuxTunnel
		data, err = sjson.DeleteBytes(data, path+".vxlan")
		if err != nil {
			return nil, false, fmt.Errorf("remove legacy VXLAN members: %w", err)
		}

		data, err = sjson.SetBytes(data, path+".options", options)
		if err != nil {
			return nil, false, fmt.Errorf("encode VXLAN options: %w", err)
		}

		changed = true
	}

	return data, changed, nil
}

func unmarshalVXLANMembers(tunnel, legacy gjson.Result) ([]tunnelOption, error) {
	if tunnel.Get("options").Exists() {
		return nil, errMixedTunnelOptions
	}

	if !legacy.IsObject() || tunnel.Get("protocol").String() != "vxlan" {
		return nil, errLegacyVXLANObject
	}

	values := legacy.Map()
	options := make([]tunnelOption, 0, len(values))

	for _, name := range slices.Sorted(maps.Keys(values)) {
		value := values[name]
		if value.Type != gjson.Number {
			slog.Error(
				"Legacy tunnel option requires a number",
				"name",
				name,
				"err",
				errLegacyOptionNumber,
			)

			return nil, fmt.Errorf("%w: %s", errLegacyOptionNumber, name)
		}

		options = append(options, tunnelOption{Name: name, Value: value.Raw})
	}

	return options, nil
}

type legacyVXLAN struct {
	VNI             *uint32 `json:"vni"`
	SourcePortMin   *uint16 `json:"source-port-min"`
	SourcePortMax   *uint16 `json:"source-port-max"`
	DestinationPort *uint16 `json:"destination-port"`
	TOS             *uint8  `json:"tos"`
}

func legacyOption[T ~uint8 | ~uint16 | ~uint32](name vxlanOptionName, value *T) tunnelOption {
	option := tunnelOption{Name: string(name), Value: ""}
	if value != nil {
		option.Value = strconv.FormatUint(uint64(*value), 10)
	}

	return option
}

func (legacy legacyVXLAN) options() []tunnelOption {
	return []tunnelOption{
		legacyOption(vxlanVNI, legacy.VNI),
		legacyOption(vxlanSourcePortMin, legacy.SourcePortMin),
		legacyOption(vxlanSourcePortMax, legacy.SourcePortMax),
		legacyOption(vxlanDestinationPort, legacy.DestinationPort),
		legacyOption(vxlanTOS, legacy.TOS),
	}
}
