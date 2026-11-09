package netif

import "github.com/insomniacslk/dhcp/dhcpv4"

func (c *DHCPClient) requestModifiers() []dhcpv4.Modifier {
	modifiers := []dhcpv4.Modifier{requestClasslessRouteFirst}
	if len(c.cfg.ClientID) > 0 {
		modifiers = append(modifiers, dhcpv4.WithOption(dhcpv4.OptClientIdentifier(c.cfg.ClientID)))
	}

	return modifiers
}

func requestClasslessRouteFirst(packet *dhcpv4.DHCPv4) {
	requested := dhcpv4.OptionCodeList{dhcpv4.OptionClasslessStaticRoute}
	requested.Add(dhcpv4.OptionDomainNameServer)

	for _, code := range packet.ParameterRequestList() {
		requested.Add(code)
	}

	packet.UpdateOption(dhcpv4.OptParameterRequestList(requested...))
}
