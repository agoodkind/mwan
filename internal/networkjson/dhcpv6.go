package networkjson

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	dhcpv6packet "github.com/insomniacslk/dhcp/dhcpv6"
	"goodkind.io/mwan/internal/interfaceintent"
)

// ValidateOwnedDHCPv6 checks the client settings shared by the loader and served tree.
func ValidateOwnedDHCPv6(name string, intent *interfaceintent.IPv6) error {
	if intent == nil || intent.DHCP == nil || !*intent.DHCP {
		return fmt.Errorf("interface %s: DHCPv6 acquisition requires ipv6/dhcp true", name)
	}
	delegation := intent.Delegation
	requestAddress := false
	requestPrefix := delegation != nil
	withoutRA := dhcpv6WithoutRA(intent)
	duid := ""
	var prefixIAID *uint32
	if delegation != nil {
		duid = delegation.DUID
		prefixIAID = delegation.IAID
	}
	client := intent.DHCPv6
	if client == nil {
		var emptyClient interfaceintent.DHCPv6
		client = &emptyClient
	}
	if client.RequestAddress != nil {
		requestAddress = *client.RequestAddress
	}
	if client.RequestPrefix != nil {
		requestPrefix = *client.RequestPrefix
	}
	if client.DUID != "" {
		duid = client.DUID
	}
	addressIAID := client.IANAIAID
	if client.IAPDIAID != nil {
		prefixIAID = client.IAPDIAID
	}
	if !requestAddress && !requestPrefix {
		return fmt.Errorf("interface %s: DHCPv6 client must request an address or prefix", name)
	}
	if delegation != nil && !requestPrefix {
		return fmt.Errorf("interface %s: DHCPv6 delegation requires a prefix request", name)
	}
	if withoutRA == "information-request" {
		return fmt.Errorf("interface %s: without-ra information-request cannot acquire an IA_NA address or IA_PD prefix", name)
	}
	if withoutRA != "solicit" && withoutRA != "no" {
		return fmt.Errorf("interface %s: DHCPv6 acquisition requires without-ra no or solicit", name)
	}
	if duid == "" {
		return fmt.Errorf("interface %s: DHCPv6 acquisition requires a configured DUID", name)
	}
	if requestAddress && addressIAID == nil {
		return fmt.Errorf("interface %s: DHCPv6 address request requires address IAID", name)
	}
	if requestPrefix && prefixIAID == nil {
		return fmt.Errorf("interface %s: DHCPv6 prefix request requires prefix IAID", name)
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
	if intent.Delegation != nil {
		return intent.Delegation.WithoutRA
	}
	return ""
}
