package moduleconfig

import (
	"net/netip"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
)

func mappedExternals(translation *config.IPv4Translation) []netip.Addr {
	if translation == nil || len(translation.StaticMappings) == 0 {
		return nil
	}

	externals := make([]netip.Addr, 0, len(translation.StaticMappings))
	for _, mapping := range translation.StaticMappings {
		if mapping.Delivery == "" {
			externals = append(externals, mapping.External)
		}
	}

	return externals
}

func localMappedExternals(translation *config.IPv4Translation) []netip.Addr {
	if translation == nil {
		return nil
	}

	var externals []netip.Addr

	for _, mapping := range translation.StaticMappings {
		if mapping.Delivery == interfaceintent.DeliveryLocal {
			externals = append(externals, mapping.External)
		}
	}

	return externals
}
