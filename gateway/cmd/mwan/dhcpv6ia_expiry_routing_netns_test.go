//go:build linux && firewallnetns

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func dhcpv6ExpiryMissingRouting() string {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Table: dhcpv6ExpiryWANTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return "table route list: " + err.Error()
	}
	tableDefault := false
	for _, route := range routes {
		if route.Dst == nil || route.Dst.String() == "::/0" {
			tableDefault = true
		}
	}
	if !tableDefault {
		return fmt.Sprintf("table %d default route", dhcpv6ExpiryWANTable)
	}
	rules, err := netlink.RuleList(netlink.FAMILY_V6)
	if err != nil {
		return "rule list: " + err.Error()
	}
	sourceRule := false
	markRule := false
	for _, rule := range rules {
		if rule.Table != dhcpv6ExpiryWANTable {
			continue
		}
		if rule.Priority == 55 && rule.Src != nil && rule.Src.String() == "2001:db8:30::/60" {
			sourceRule = true
		}
		if rule.Priority == 100 && rule.Mark == 1 {
			markRule = true
		}
	}
	if !sourceRule {
		return "policy rule 55"
	}
	if !markRule {
		return "policy rule 100"
	}
	return ""
}

func runtimeIPv6AddressFlags(t *testing.T, linkName, prefix string) (int, bool) {
	t.Helper()
	link, err := netlink.LinkByName(linkName)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if address.IPNet.String() == prefix {
			return address.Flags, true
		}
	}
	return 0, false
}
