package networkjson

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	dhcpv6packet "github.com/insomniacslk/dhcp/dhcpv6"
	"goodkind.io/mwan/internal/interfaceintent"
)

func validateMWANDHCPv6(name string, wire *familyV6, intent *interfaceintent.IPv6) error {
	if wire.DHCP == nil || !*wire.DHCP || wire.Delegation == nil {
		return fmt.Errorf("interface %s: DHCPv6 delegation requires ipv6/dhcp true and delegation", name)
	}
	delegation := intent.Delegation
	withoutRA := dhcpv6WithoutRA(intent)
	duid := delegation.DUID
	iaid := delegation.IAID
	if client := intent.DHCPv6; client != nil {
		if client.RequestAddress != nil && *client.RequestAddress || client.RequestPrefix != nil && !*client.RequestPrefix {
			return fmt.Errorf("interface %s: DHCPv6 address request and disabled prefix request are not supported", name)
		}
		if client.UseDNS != nil && *client.UseDNS {
			return fmt.Errorf("interface %s: dhcpv6 use-dns requires resolver ownership", name)
		}
		if client.DUID != "" {
			duid = client.DUID
		}
		if client.IAPDIAID != nil {
			iaid = client.IAPDIAID
		}
	}
	if withoutRA == "information-request" {
		return fmt.Errorf("interface %s: without-ra information-request cannot acquire an IA_PD prefix", name)
	}
	if withoutRA != "solicit" && withoutRA != "no" {
		return fmt.Errorf("interface %s: DHCPv6 delegation requires without-ra no or solicit", name)
	}
	if duid == "" || iaid == nil {
		return fmt.Errorf("interface %s: DHCPv6 delegation requires configured DUID and prefix IAID", name)
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(duid, ":", ""))
	if err != nil {
		slog.Warn("networkjson: DHCPv6 DUID invalid", "interface", name, "err", err)
		return fmt.Errorf("interface %s: invalid DHCPv6 DUID: %w", name, err)
	}
	if _, err := dhcpv6packet.DUIDFromBytes(decoded); err != nil {
		slog.Warn("networkjson: DHCPv6 DUID invalid", "interface", name, "err", err)
		return fmt.Errorf("interface %s: invalid DHCPv6 DUID: %w", name, err)
	}
	return nil
}

func dhcpv6WithoutRA(intent *interfaceintent.IPv6) string {
	if intent.DHCPv6 != nil && intent.DHCPv6.WithoutRA != "" {
		return intent.DHCPv6.WithoutRA
	}
	return intent.Delegation.WithoutRA
}
