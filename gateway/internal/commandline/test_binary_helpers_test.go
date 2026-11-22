package commandline_test

import (
	"testing"

	"goodkind.io/mwan/internal/commandline"
)

func testMwanBinary(t *testing.T) string {
	t.Helper()

	return commandline.BuildTestBinary(t)
}
