package netif

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"
	internalclock "goodkind.io/mwan/internal/clock"
)

func restartTestConfig() (DHCPv6PDConfig, *net.Interface, DHCPv6PDLease) {
	now := time.Now()
	link := &net.Interface{Name: "test0", Index: 7, HardwareAddr: net.HardwareAddr{2, 3, 4, 5, 6, 7}}
	config := DHCPv6PDConfig{
		Iface: link.Name, DUID: []byte{0, 3, 0, 1, 2, 3, 4, 5, 6, 7},
		IAID: 12, IANAIAID: 13, RequestAddress: true, RequestPrefix: true,
		Clock: internalclock.Real{},
	}
	lease := DHCPv6PDLease{
		LinkName: link.Name, LinkIndex: 3, LinkHardwareAddr: bytes.Clone(link.HardwareAddr),
		DUID: bytes.Clone(config.DUID), IAID: config.IAID, IANAIAID: config.IANAIAID,
		ServerID: []byte{0, 3, 0, 1, 2, 3, 4, 5, 6, 8}, AcquiredAt: now.Add(-time.Minute),
		RenewAt: now.Add(time.Minute), RebindAt: now.Add(2 * time.Minute),
		IANARenewAt: now.Add(time.Minute), IANARebindAt: now.Add(2 * time.Minute),
		RequestAddress: true, RequestPrefix: true,
		Prefixes:  []DelegatedPrefix{{Prefix: netip.MustParsePrefix("2001:db8:1::/56"), PreferredUntil: now.Add(3 * time.Minute), ValidUntil: now.Add(4 * time.Minute)}},
		Addresses: []DelegatedAddress{{Address: netip.MustParseAddr("2001:db8::1"), PreferredUntil: now.Add(3 * time.Minute), ValidUntil: now.Add(4 * time.Minute)}},
	}
	return config, link, lease
}

func TestDHCPv6RestartCompatibility(t *testing.T) {
	config, link, lease := restartTestConfig()
	if !compatibleDHCPv6Lease(config, link, lease) {
		t.Fatal("matching cached lease was rejected after interface index changed")
	}
	changed := lease
	changed.DUID = bytes.Clone(lease.DUID)
	changed.DUID[len(changed.DUID)-1]++
	if compatibleDHCPv6Lease(config, link, changed) {
		t.Fatal("changed DUID was accepted")
	}
	changed = lease
	changed.RequestPrefix = false
	if compatibleDHCPv6Lease(config, link, changed) {
		t.Fatal("changed request settings were accepted")
	}
	changed = lease
	changed.Prefixes = nil
	changed.Addresses = nil
	if compatibleDHCPv6Lease(config, link, changed) {
		t.Fatal("lease without valid assignments was accepted")
	}
}

func TestDHCPv6RestartMessages(t *testing.T) {
	config, _, lease := restartTestConfig()
	duid, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		t.Fatal(err)
	}
	confirm, err := dhcpv6.NewMessage(dhcpv6.WithClientID(duid))
	if err != nil {
		t.Fatal(err)
	}
	addressOnly := config
	addressOnly.RequestPrefix = false
	configureDHCPv6Message(confirm, addressOnly, dhcpv6.MessageTypeConfirm, &lease)
	if confirm.MessageType != dhcpv6.MessageTypeConfirm || len(confirm.Options.IANA()) != 1 || len(confirm.Options.IAPD()) != 0 || confirm.Options.ServerID() != nil {
		t.Fatalf("invalid Confirm options: %s", confirm)
	}
	if got := confirm.Options.IANA()[0].Options.Addresses(); len(got) != 1 || !got[0].IPv6Addr.Equal(net.IP(lease.Addresses[0].Address.AsSlice())) {
		t.Fatalf("Confirm omitted cached address: %s", confirm)
	}
	rebind, err := dhcpv6.NewMessage(dhcpv6.WithClientID(duid))
	if err != nil {
		t.Fatal(err)
	}
	configureDHCPv6Message(rebind, config, dhcpv6.MessageTypeRebind, &lease)
	if rebind.MessageType != dhcpv6.MessageTypeRebind || len(rebind.Options.IANA()) != 1 || len(rebind.Options.IAPD()) != 1 || rebind.Options.ServerID() != nil {
		t.Fatalf("invalid Rebind options: %s", rebind)
	}
	if len(rebind.Options.IAPD()[0].Options.Prefixes()) != 1 {
		t.Fatalf("Rebind omitted cached prefix: %s", rebind)
	}
}

func TestDHCPv6RestartReplyDoesNotValidateOmittedOrUnboundIA(t *testing.T) {
	config, link, lease := restartTestConfig()
	client, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := dhcpv6.DUIDFromBytes(lease.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := dhcpv6.NewMessage(dhcpv6.WithClientID(client), dhcpv6.WithServerID(server))
	if err != nil {
		t.Fatal(err)
	}
	reply.MessageType = dhcpv6.MessageTypeReply
	var prefixIAID [4]byte
	binary.BigEndian.PutUint32(prefixIAID[:], config.IAID)
	prefix := &dhcpv6.OptIAPD{IaId: prefixIAID, T1: time.Minute, T2: 2 * time.Minute}
	prefix.Options.Add(&dhcpv6.OptIAPrefix{Prefix: prefixToIPNet(lease.Prefixes[0].Prefix), PreferredLifetime: 3 * time.Minute, ValidLifetime: 4 * time.Minute})
	reply.AddOption(prefix)
	validated, err := parseDHCPv6(reply, config, nil, link, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.Prefixes) != 1 || len(validated.Addresses) != 0 || !validated.IANARenewAt.IsZero() {
		t.Fatalf("omitted IA_NA was validated: %+v", validated)
	}
	var addressIAID [4]byte
	binary.BigEndian.PutUint32(addressIAID[:], config.IANAIAID)
	address := &dhcpv6.OptIANA{IaId: addressIAID}
	address.Options.Add(&dhcpv6.OptStatusCode{StatusCode: iana.StatusNoBinding})
	reply.AddOption(address)
	validated, err = parseDHCPv6(reply, config, nil, link, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.Addresses) != 0 || !validated.IANARenewAt.IsZero() {
		t.Fatalf("NoBinding IA_NA was validated: %+v", validated)
	}
}
