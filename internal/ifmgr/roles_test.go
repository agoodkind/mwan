package ifmgr

import (
	"reflect"
	"sort"
	"testing"
)

// TestModulesForRoleOOB pins the modules list and order for the unified
// oob role so accidental reorderings are caught at CI time. Order matters
// because Reconcile fires modules in this sequence and several modules
// (oobv6 in particular) rely on policy_rules having run first.
func TestModulesForRoleOOB(t *testing.T) {
	t.Parallel()

	got, err := modulesForRole("oob")
	if err != nil {
		t.Fatalf("modulesForRole(\"oob\") returned err: %v", err)
	}
	want := []string{
		"policy_rules",
		"host_ipv6_policy",
		"oobv6",
		"oobv4",
		"ra_lost",
		"cloudflared_tap",
		"wg",
	}
	if len(got) != len(want) {
		t.Fatalf("oob role module count = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("oob[%d] = %q, want %q (got=%v)", i, got[i], want[i], got)
		}
	}
}

// TestModulesForRoleFailover pins the modules list and order for the
// renamed failover role (formerly lxc-failover-backup).
func TestModulesForRoleFailover(t *testing.T) {
	t.Parallel()

	got, err := modulesForRole("failover")
	if err != nil {
		t.Fatalf("modulesForRole(\"failover\") returned err: %v", err)
	}
	want := []string{
		"slaac_health",
		"bridge_probe",
		"connectivity_probe",
		"ra_lost",
		"mainv4",
	}
	if len(got) != len(want) {
		t.Fatalf("failover role module count = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("failover[%d] = %q, want %q (got=%v)", i, got[i], want[i], got)
		}
	}
}

// TestModulesForRoleWAN pins the module list for the MWAN VM policy-routing
// role.
func TestModulesForRoleWAN(t *testing.T) {
	t.Parallel()

	got, err := modulesForRole("wan")
	if err != nil {
		t.Fatalf("modulesForRole(\"wan\") returned err: %v", err)
	}
	want := []string{
		"health",
		"wan.routes",
		"steering",
		"npt",
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
	for _, role := range known {
		if role == "wan" {
			return
		}
	}
	t.Fatalf("KnownRoles does not contain wan (known=%v)", known)
}

// TestKnownRolesDropsLegacyNames protects against accidental
// reintroduction of any role that slice 1 collapsed or renamed away.
func TestKnownRolesDropsLegacyNames(t *testing.T) {
	t.Parallel()

	known := KnownRoles()
	sort.Strings(known)
	legacy := []string{
		"vault-oob",
		"suburban-oob",
		"suburban-wg",
		"lxc-failover-backup",
	}
	for _, role := range legacy {
		for _, k := range known {
			if k == role {
				t.Errorf("KnownRoles still contains legacy role %q (known=%v)", role, known)
			}
		}
	}
	_, err := modulesForRole("vault-oob")
	if err == nil {
		t.Error("modulesForRole(\"vault-oob\") returned nil err; legacy role should be gone")
	}
	_, err = modulesForRole("lxc-failover-backup")
	if err == nil {
		t.Error("modulesForRole(\"lxc-failover-backup\") returned nil err; legacy role should be gone")
	}
}

// TestLookupDoesNotResolveLegacyWGHealthName confirms the wg_health
// module-name string from the pre-slice-1 registry is gone. The wg
// module is registered by its package's init() function which only fires
// when the wg package is imported (cmd/mwan does that via side-effect
// import); here we only assert the negative case to avoid an import
// cycle from internal/ifmgr to internal/ifmgr/modules/wg.
func TestLookupDoesNotResolveLegacyWGHealthName(t *testing.T) {
	t.Parallel()

	if _, ok := Lookup("wg_health"); ok {
		t.Errorf("Lookup(\"wg_health\") resolved; legacy module name should not be registered")
	}
}

// TestModulesForRoleExported confirms the exported ModulesForRole accessor
// (used by package main to build only the active role's module configs)
// mirrors the unexported modulesForRole.
func TestModulesForRoleExported(t *testing.T) {
	t.Parallel()

	got, err := ModulesForRole("wan")
	if err != nil {
		t.Fatalf("ModulesForRole(\"wan\") returned err: %v", err)
	}
	want := []string{"health", "wan.routes", "steering", "npt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModulesForRole(\"wan\") = %v, want %v", got, want)
	}

	if _, err := ModulesForRole("bogus"); err == nil {
		t.Error("ModulesForRole(\"bogus\") returned nil err; unknown role should error")
	}
}
