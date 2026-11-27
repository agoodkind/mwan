//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"strings"
	"testing"
)

func AssertDebugInspectionFields(t *testing.T, text string, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(text, field) {
			t.Fatalf("text omitted source fields %q: %s", field, text)
		}
	}
}
