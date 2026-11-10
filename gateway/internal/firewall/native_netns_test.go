//go:build linux && netns

package firewall_test

import (
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/firewall"
)

func TestInspectNativeEstablishedPolicy(t *testing.T) {
	const childEnvironment = "MWAN_NATIVE_POLICY_CHILD"
	if os.Getenv(childEnvironment) != "1" {
		if os.Geteuid() != 0 {
			t.Fatal("native policy inspection requires a privileged network namespace")
		}
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInspectNativeEstablishedPolicy$", "-test.v")
		if deadline, present := t.Deadline(); present {
			remaining := deadline.Sub(clock.Real{}.Now())
			if remaining <= 0 {
				t.Fatal("native policy child started after the test deadline")
			}
			command.Args = append(command.Args, "-test.timeout="+remaining.String())
		}
		command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		command.Env = append(os.Environ(), childEnvironment+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated native policy inspection: %v: %s", err, output)
		}
		t.Log(string(output))
		return
	}
	desired, err := firewall.CompileBaseline(firewall.BaselineConfig{
		ManagementPolicy: firewall.ManagementOptional, ManagementInterface: "", ManagementServices: nil,
		InternalInterface: "native-input", ProviderInterfaces: nil, LocalPermits: nil,
	})
	if err != nil {
		t.Fatalf("compile the owned firewall: %v", err)
	}
	if err := firewall.Apply(t.Context(), desired); err != nil {
		t.Fatalf("install the owned firewall: %v", err)
	}
	if err := firewall.InspectNative(t.Context(), desired); err != nil {
		t.Fatalf("owned firewall was ineligible: %v", err)
	}
	for _, family := range []string{"ip", "ip6"} {
		t.Run(family, func(t *testing.T) {
			ecnDrop := family + " ecn ect0 drop"
			cases := []struct {
				name       string
				rules      []string
				compatible bool
			}{
				{name: "established-first", rules: []string{"ct state established,related accept", ecnDrop}, compatible: true},
				{name: "ecn-first", rules: []string{ecnDrop, "ct state established,related accept"}, compatible: false},
				{name: "header-first", rules: []string{family + " length 128 drop", "ct state established,related accept"}, compatible: false},
				{name: "restored-established", rules: []string{"ct state established accept", ecnDrop}, compatible: true},
			}
			for _, entry := range cases {
				t.Run(entry.name, func(t *testing.T) {
					extra := nativeExtraPolicy(entry.rules)
					if err := firewall.Apply(t.Context(), extra); err != nil {
						t.Fatalf("install additional policy: %v", err)
					}
					inspection := firewall.InspectNative(t.Context(), desired)
					if (inspection == nil) != entry.compatible {
						t.Fatalf("native compatibility=%v, expected %v: %v", inspection == nil, entry.compatible, inspection)
					}
					if _, err := firewall.Inspect(t.Context(), extra); err != nil {
						t.Fatalf("native inspection changed the additional Linux policy: %v", err)
					}
				})
			}
		})
	}
	checkNativeNATPolicy(t, desired)
}

func nativeExtraPolicy(expressions []string) firewall.Ruleset {
	chain := firewall.Chain{
		Table: firewall.Table{Family: "inet", Name: "native-extra"},
		Name:  "forward", Type: "filter", Hook: "forward", Priority: "filter", Policy: "drop", Rules: nil,
	}
	for _, expression := range expressions {
		chain.Rules = append(chain.Rules, firewall.Rule{
			Purpose: "", Scope: "native-proof", Action: "", Expression: expression,
			Interface: "", OutputInterface: "", Source: netip.Prefix{}, Destination: netip.Prefix{}, Mark: nil,
		})
	}
	return firewall.Ruleset{Chains: []firewall.Chain{chain}, Sets: nil}
}

func checkNativeNATPolicy(t *testing.T, desired firewall.Ruleset) {
	t.Helper()
	for _, policy := range []struct {
		family   string
		external string
		internal string
	}{
		{family: "ip", external: "198.51.100.2", internal: "192.0.2.2"},
		{family: "ip6", external: "2001:db8:2::2", internal: "2001:db8:1::2"},
	} {
		address := netip.MustParseAddr(policy.external)
		chain := firewall.Chain{
			Table: firewall.Table{Family: policy.family, Name: "native-existing-nat"},
			Name:  "prerouting", Type: "nat", Hook: "prerouting", Priority: "dstnat", Policy: "accept",
			Rules: []firewall.Rule{{
				Purpose: firewall.PurposeStaticMappingDNAT, Scope: "native-proof", Action: firewall.ActionDNAT,
				Expression: policy.family + " daddr " + policy.external + " dnat to " + policy.internal,
				Interface:  "", OutputInterface: "", Source: netip.Prefix{}, Destination: netip.PrefixFrom(address, address.BitLen()), Mark: nil,
			}},
		}
		if err := firewall.Apply(t.Context(), firewall.Ruleset{Chains: []firewall.Chain{chain}, Sets: nil}); err != nil {
			t.Fatalf("install existing %s NAT policy: %v", policy.family, err)
		}
		if err := firewall.InspectNative(t.Context(), desired); err != nil {
			t.Fatalf("existing %s NAT policy was ineligible: %v", policy.family, err)
		}
	}
}
