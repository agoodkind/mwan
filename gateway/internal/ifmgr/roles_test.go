package ifmgr

// The wan role lists the modules that the interface manager reconciles. These
// tests check the role lookup and ensure that OVS follows health in the
// reconciliation order.

import (
	"reflect"
	"slices"
	"testing"
)

func TestModulesForRoleWAN(t *testing.T) {
	t.Parallel()

	got, err := modulesForRole("wan")
	if err != nil {
		t.Fatalf("modulesForRole(\"wan\") returned err: %v", err)
	}

	want := []string{
		"links",
		"autoconfiguration",
		"addresses",
		"resolver",
		"health",
		"firewall",
		"npt",
		"wan.routes",
		"bgp_sessions",
		"steering",
	}
	if len(got) != len(want) {
		t.Fatalf("wan role module count = %d, want %d (got=%v)", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("wan[%d] = %q, want %q (got=%v)", i, got[i], want[i], got)
		}
	}

	known := KnownRoles()
	if !reflect.DeepEqual(known, []string{"wan"}) {
		t.Fatalf("KnownRoles = %v, want [wan]", known)
	}
}

func TestModulesForRoleExported(t *testing.T) {
	t.Parallel()

	got, err := ModulesForRole("wan")
	if err != nil {
		t.Fatalf("ModulesForRole(\"wan\") returned err: %v", err)
	}

	want := []string{
		"links",
		"autoconfiguration",
		"addresses",
		"resolver",
		"health",
		"firewall",
		"npt",
		"wan.routes",
		"bgp_sessions",
		"steering",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModulesForRole(\"wan\") = %v, want %v", got, want)
	}

	if _, err := ModulesForRole("bogus"); err == nil {
		t.Error("ModulesForRole(\"bogus\") returned nil err; unknown role should error")
	}
}

func TestOVSReconcilesAfterHealth(t *testing.T) {
	t.Parallel()

	modules, err := ModulesForOVS("wan")
	if err != nil {
		t.Fatal(err)
	}

	health := slices.Index(modules, "health")
	ovs := slices.Index(modules, "ovs")
	if health < 0 || ovs <= health {
		t.Fatalf("OVS must reconcile after health, got %v", modules)
	}
}
