package oobv6

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func testLog(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

// TestNew_DefaultsApplied confirms New's registry-default path (cfg ==
// nil) enables SLAAC rule management with priority 7. The production path
// applies the same defaults in buildOOBV6Config, which distinguishes an
// unset field from an explicit zero via its pointer-typed TOML section;
// New itself receives a fully-formed Config and does not re-default a
// plain-bool field, since an unset bool and an explicit false are
// indistinguishable.
func TestNew_DefaultsApplied(t *testing.T) {
	mod, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	m := mod.(*Module)
	if !m.cfg.ManageSLAACRule {
		t.Errorf("ManageSLAACRule default: got false, want true")
	}
	if m.cfg.SLAACRulePriority != 7 {
		t.Errorf("SLAACRulePriority default: got %d, want 7", m.cfg.SLAACRulePriority)
	}
}

// TestNew_ExplicitDisable confirms a fully-formed config with
// manage_slaac_source_rule=false is honored (off-switch for problematic
// deploys). buildOOBV6Config produces a complete Config, so New receives
// the priority already set.
func TestNew_ExplicitDisable(t *testing.T) {
	mod, err := New(Config{
		Iface:             "mbrains",
		OOBAddr:           "3d06:bad:b01:ff::1/128",
		OOBTableID:        500,
		ManageSLAACRule:   false,
		SLAACRulePriority: 7,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m := mod.(*Module)
	if m.cfg.ManageSLAACRule {
		t.Errorf("ManageSLAACRule: got true, want false")
	}
}

// TestNew_CustomPriority confirms slaac_rule_priority overrides the
// default.
func TestNew_CustomPriority(t *testing.T) {
	for _, priority := range []int{42} {
		mod, err := New(Config{
			Iface:             "mbrains",
			OOBAddr:           "3d06:bad:b01:ff::1/128",
			OOBTableID:        500,
			SLAACRulePriority: priority,
		})
		if err != nil {
			t.Fatalf("New(%d): %v", priority, err)
		}
		m := mod.(*Module)
		if m.cfg.SLAACRulePriority != 42 {
			t.Errorf("SLAACRulePriority(%d): got %d, want 42", priority, m.cfg.SLAACRulePriority)
		}
	}
}

// TestNew_PriorityOutOfRange rejects priorities outside the kernel-valid
// range so we fail fast in Init rather than at netlink call time.
func TestNew_PriorityOutOfRange(t *testing.T) {
	cases := []int{0, -1, 32766, 100000}
	for _, p := range cases {
		_, err := New(Config{
			Iface:             "mbrains",
			OOBAddr:           "3d06:bad:b01:ff::1/128",
			OOBTableID:        500,
			SLAACRulePriority: p,
		})
		if err == nil {
			t.Errorf("priority=%d: expected error, got nil", p)
		}
	}
}

// TestReconcileSLAACSrcRule_Disabled verifies the no-op path so disabling
// the feature is truly inert (no netlink calls, no installedSLAACAddr
// state mutation).
func TestReconcileSLAACSrcRule_Disabled(t *testing.T) {
	m := &Module{
		cfg: Config{
			Iface:             "mbrains",
			ManageSLAACRule:   false,
			SLAACRulePriority: 7,
		},
		installedSLAACAddr: "previous-value",
	}
	// reconcileSLAACSrcRule short-circuits before netlink. We only call
	// it via the no-op early return; if it ever reached netif.ListAddrs
	// against iface "mbrains" in a test process we'd see ENODEV.
	if err := m.reconcileSLAACSrcRule(testCtx(t), testLog(t)); err != nil {
		t.Fatalf("disabled path returned error: %v", err)
	}
	if m.installedSLAACAddr != "previous-value" {
		t.Errorf("installedSLAACAddr changed despite disabled: %q",
			m.installedSLAACAddr)
	}
}
