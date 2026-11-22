//go:build linux && (protocol || firewallnetns || systemd)

package commandline_test

import (
	"strings"
	"testing"
)

func assertDebugInspectionFields(t *testing.T, text string, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(text, field) {
			t.Fatalf("text omitted source fields %q: %s", field, text)
		}
	}
}
