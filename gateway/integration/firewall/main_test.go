//go:build linux && firewall

package firewall_test

import (
	"fmt"
	"os"
	"testing"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := testenv.RemoveTestBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "remove test binary: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func TestBGPUpstreamRouterProcess(t *testing.T) {
	testenv.TestBGPUpstreamRouterProcess(t)
}
