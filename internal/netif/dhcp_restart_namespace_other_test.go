//go:build !linux

package netif

import "testing"

func dhcpRestartNamespace(t *testing.T) bool {
	t.Helper()
	t.Skip("DHCP restart transport requires Linux network namespaces")
	return false
}
