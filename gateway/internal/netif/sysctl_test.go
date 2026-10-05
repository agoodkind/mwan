package netif

import "testing"

func TestKeyToPath(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{
			key:  "net.ipv6.conf.eth0.disable_ipv6",
			want: "/proc/sys/net/ipv6/conf/eth0/disable_ipv6",
		},
		{
			key:  "net.ipv6.conf.eth0.accept_ra",
			want: "/proc/sys/net/ipv6/conf/eth0/accept_ra",
		},
		{
			key:  "net.ipv4.conf.all.forwarding",
			want: "/proc/sys/net/ipv4/conf/all/forwarding",
		},
		{
			// VLAN-style NIC name.
			key:  "net.ipv6.conf.enatt0.3242.accept_ra",
			want: "/proc/sys/net/ipv6/conf/enatt0.3242/accept_ra",
		},
		{
			// VLAN-style NIC name.
			key:  "net.ipv6.conf.enatt0.3242.disable_ipv6",
			want: "/proc/sys/net/ipv6/conf/enatt0.3242/disable_ipv6",
		},
		{
			// Non-conf path: every dot becomes a slash.
			key:  "net.core.somaxconn",
			want: "/proc/sys/net/core/somaxconn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := keyToPath(tc.key); got != tc.want {
				t.Errorf("keyToPath(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}
