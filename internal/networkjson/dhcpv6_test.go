package networkjson_test

import (
	"strings"
	"testing"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

const testDUID = "00:01:2a:5b:3c:4d:02:00:5e:00:53:01"

func mwanDHCPv6Document(family string) string {
	owned := `{ "name": "endhcp6", "type": "iana-if-type:ethernetCsmacd",
        "goodkind-mwan-steering:connection-id": "owned-dhcp6",
        "goodkind-mwan-steering:owner": "mwan",
        "goodkind-mwan-steering:link": { "match": { "hardware-address": "02:00:5e:00:53:77" } },
        "ietf-ip:ipv6": ` + family + ` },`
	return strings.Replace(validDocument, `{ "name": "enmwanbr0", "type": "iana-if-type:other" }`,
		owned+` { "name": "enmwanbr0", "type": "iana-if-type:other" }`, 1)
}

func TestLoadMWANOwnedDHCPv6RequestModes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name           string
		family         string
		wantAddress    bool
		wantDelegation bool
	}{
		{
			name: "address only",
			family: `{ "goodkind-mwan-steering:dhcp": true,
                "goodkind-mwan-steering:dhcpv6-client": {
                  "duid": "` + testDUID + `", "address-iaid": 41,
                  "request-address": true, "request-prefix": false,
                  "without-ra": "solicit", "use-dns": false } }`,
			wantAddress: true,
		},
		{
			name: "prefix only",
			family: `{ "goodkind-mwan-steering:dhcp": true,
                "goodkind-mwan-steering:delegation": {
                  "duid": "` + testDUID + `", "iaid": 41,
                  "hint": "::/60", "without-ra": "solicit" } }`,
			wantDelegation: true,
		},
		{
			name: "combined with equal numeric IAIDs",
			family: `{ "goodkind-mwan-steering:dhcp": true,
                "goodkind-mwan-steering:delegation": {
                  "duid": "` + testDUID + `", "iaid": 41,
                  "hint": "::/60", "without-ra": "solicit" },
                "goodkind-mwan-steering:dhcpv6-client": {
                  "address-iaid": 41, "request-address": true,
                  "request-prefix": true, "use-dns": false } }`,
			wantAddress: true, wantDelegation: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			loaded, err := networkjson.Load(writeDocument(t, mwanDHCPv6Document(testCase.family)), schemaDirForTest(t))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			connection := requireConnection(t, loaded.Connections, "endhcp6")
			if connection.IPv6 == nil || connection.IPv6.DHCP == nil || !*connection.IPv6.DHCP ||
				(connection.IPv6.Delegation != nil) != testCase.wantDelegation {
				t.Fatalf("DHCPv6 intent = %+v", connection.IPv6)
			}
			client := connection.IPv6.DHCPv6
			if testCase.wantAddress && (client == nil || client.RequestAddress == nil || !*client.RequestAddress ||
				client.IANAIAID == nil || *client.IANAIAID != 41) {
				t.Fatalf("IA_NA intent = %+v", client)
			}
			foundClient := false
			foundAddress := false
			for _, claim := range loaded.Claims {
				if claim.ConnectionID != connection.ID || claim.Family != "ipv6" {
					continue
				}
				if claim.Kind == interfaceintent.ResourceDHCPv6 && claim.Writer == interfaceintent.WriterMWANProtocol {
					foundClient = true
				}
				if claim.Kind == interfaceintent.ResourceAcquiredAddress && claim.Writer == interfaceintent.WriterMWANAddress {
					foundAddress = true
				}
			}
			if !foundClient || !foundAddress {
				t.Fatalf("DHCPv6 ownership claims = %+v", loaded.Claims)
			}
		})
	}
}

func TestLoadRejectsInvalidMWANOwnedDHCPv6Requests(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		family string
		want   string
	}{
		{
			name:   "neither association",
			family: `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv6-client": { "duid": "` + testDUID + `", "request-address": false, "request-prefix": false, "without-ra": "solicit" } }`,
			want:   "must request an address or prefix",
		},
		{
			name:   "missing address IAID",
			family: `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv6-client": { "duid": "` + testDUID + `", "request-address": true, "without-ra": "solicit" } }`,
			want:   "requires address IAID",
		},
		{
			name:   "missing prefix IAID",
			family: `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv6-client": { "duid": "` + testDUID + `", "request-prefix": true, "without-ra": "solicit" } }`,
			want:   "requires prefix IAID",
		},
		{
			name:   "DNS installation",
			family: `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv6-client": { "duid": "` + testDUID + `", "address-iaid": 41, "request-address": true, "without-ra": "solicit", "use-dns": true } }`,
			want:   "use-dns requires resolver ownership",
		},
		{
			name:   "information request",
			family: `{ "goodkind-mwan-steering:dhcp": true, "goodkind-mwan-steering:dhcpv6-client": { "duid": "` + testDUID + `", "address-iaid": 41, "request-address": true, "without-ra": "information-request" } }`,
			want:   "information-request cannot acquire",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := networkjson.Load(writeDocument(t, mwanDHCPv6Document(testCase.family)), schemaDirForTest(t))
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Load error = %v, want %q", err, testCase.want)
			}
		})
	}
}
