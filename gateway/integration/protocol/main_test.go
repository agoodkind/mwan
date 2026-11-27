//go:build linux && protocol

package protocol_test

import (
	"goodkind.io/mwan/gateway/integration/internal/testenv"
	"fmt"
	"os"
	"testing"

)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := testenv.RemoveTestBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "remove test binary: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
