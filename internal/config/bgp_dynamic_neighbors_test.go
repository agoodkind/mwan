package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBGPDynamicNeighbors(t *testing.T) {
	valid := minimalBGPSection()
	valid.DynamicNeighbors = []string{"10.250.250.0/29", "3d06:bad:b01:fe::/64"}
	valid.LearnedRouteIface = "enmwanbr0"
	if err := validateBGP(&valid); err != nil {
		t.Fatalf("dynamic CIDR prefixes must pass validation: %v", err)
	}

	invalid := minimalBGPSection()
	invalid.DynamicNeighbors = []string{"not-a-prefix"}
	if err := validateBGP(&invalid); err == nil {
		t.Fatal("non-CIDR dynamic neighbor must fail validation")
	}
}

func TestValidateBGPRequiresLearnedRouteIfaceForDynamicNeighbors(t *testing.T) {
	missingIface := minimalBGPSection()
	missingIface.DynamicNeighbors = []string{"10.250.250.0/29"}
	if err := validateBGP(&missingIface); err == nil {
		t.Fatal("dynamic neighbors without learned_route_iface must fail validation")
	}
}

func TestValidateBGPDynamicNeighborsRejectsDefaultRoutes(t *testing.T) {
	testCases := []struct {
		name   string
		prefix string
	}{
		{name: "IPv4", prefix: "0.0.0.0/0"},
		{name: "IPv6", prefix: "::/0"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			invalid := minimalBGPSection()
			invalid.DynamicNeighbors = []string{testCase.prefix}
			invalid.LearnedRouteIface = "enmwanbr0"
			err := validateBGP(&invalid)
			if err == nil {
				t.Fatal("default-route dynamic neighbor must fail validation")
			}
			if !strings.Contains(err.Error(), "must not be a default route") {
				t.Fatalf("validation error = %q, want default-route rejection", err)
			}
		})
	}
}

func TestLoadRequiresLearnedRouteIfaceForDynamicNeighbors(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configText := "[bgp]\ndynamic_neighbors = [\"10.250.250.0/29\"]\n"
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MWAN_CONFIG", configPath)

	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded without learned_route_iface")
	}
	if !strings.Contains(err.Error(), "learned_route_iface is required") {
		t.Fatalf("Load error = %q, want learned_route_iface requirement", err)
	}
}

func minimalBGPSection() BGPSection {
	return BGPSection{
		ASN:              4200000001,
		RouterID:         "10.250.250.3",
		KeepaliveSeconds: 10,
		HoldSeconds:      30,
		ListenPort:       179,
		Announce: BGPAnnounce{
			IPv6: []string{"::/0"},
		},
	}
}
