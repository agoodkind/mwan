package failovercheck_test

import (
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/failovercheck"
)

const (
	validLXCID    = "203"
	validVMID     = "301"
	validAgent    = "10.0.0.2:50052"
	validPrimary  = "10.0.0.1:50052"
	validVsockCID = 42
)

func validConfig() *config.Config {
	cfg := &config.Config{}
	cfg.MwanVMID = validVMID
	cfg.Failover.LXCID = validLXCID
	cfg.Failover.AgentTCPAddr = validAgent
	cfg.Watchdog.MwanAgentTCPAddr = validPrimary
	return cfg
}

func TestCheck(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mutate func(*config.Config)
		want   []string
	}{
		"valid configuration without bgp section": {
			mutate: func(*config.Config) {},
			want:   nil,
		},
		"primary endpoint from vsock only": {
			mutate: func(cfg *config.Config) {
				cfg.Watchdog.MwanAgentTCPAddr = ""
				cfg.Watchdog.VsockCID = validVsockCID
			},
			want: nil,
		},
		"missing lxc_id": {
			mutate: func(cfg *config.Config) { cfg.Failover.LXCID = "" },
			want:   []string{"lxc_id"},
		},
		"missing agent_tcp_addr": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = "" },
			want:   []string{"agent_tcp_addr is required"},
		},
		"agent_tcp_addr without port": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = "10.0.0.2" },
			want:   []string{"agent_tcp_addr"},
		},
		"agent_tcp_addr with non-numeric port": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = "10.0.0.2:agent" },
			want:   []string{"agent_tcp_addr"},
		},
		"agent_tcp_addr without host": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = ":50052" },
			want:   []string{"agent_tcp_addr"},
		},
		"agent_tcp_addr with whitespace host": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = " :50052" },
			want:   []string{"host is neither an ip address nor a dns hostname"},
		},
		"agent_tcp_addr with underscore in host": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = "failover_agent:50052" },
			want:   []string{"host is neither an ip address nor a dns hostname"},
		},
		"agent_tcp_addr with hostname": {
			mutate: func(cfg *config.Config) {
				cfg.Failover.AgentTCPAddr = "failover-agent.example.net:50052"
			},
			want: nil,
		},
		"agent_tcp_addr with ipv4 address": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = "192.0.2.10:50052" },
			want:   nil,
		},
		"agent_tcp_addr with bracketed ipv6 address": {
			mutate: func(cfg *config.Config) { cfg.Failover.AgentTCPAddr = "[2001:db8::10]:50052" },
			want:   nil,
		},
		"missing mwan_vmid": {
			mutate: func(cfg *config.Config) { cfg.MwanVMID = "" },
			want:   []string{"mwan_vmid is required"},
		},
		"equal guest identifiers": {
			mutate: func(cfg *config.Config) { cfg.Failover.LXCID = cfg.MwanVMID },
			want:   []string{"must differ"},
		},
		"no primary endpoint": {
			mutate: func(cfg *config.Config) { cfg.Watchdog.MwanAgentTCPAddr = "" },
			want:   []string{"no primary endpoint"},
		},
		"every precondition fails": {
			mutate: func(cfg *config.Config) {
				cfg.Failover.LXCID = ""
				cfg.Failover.AgentTCPAddr = ""
				cfg.MwanVMID = ""
				cfg.Watchdog.MwanAgentTCPAddr = ""
			},
			want: []string{
				"lxc_id is required",
				"mwan_vmid is required",
				"agent_tcp_addr is required",
				"no primary endpoint",
			},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			testCase.mutate(cfg)

			failures := failovercheck.Check(cfg)

			if len(failures) != len(testCase.want) {
				t.Fatalf("got %d failures %v, want %d", len(failures), failures, len(testCase.want))
			}
			unmatched := slices.Clone(failures)
			for _, want := range testCase.want {
				matched := slices.IndexFunc(unmatched, func(failure error) bool {
					return strings.Contains(failure.Error(), want)
				})
				if matched < 0 {
					t.Fatalf("failures %v, want one to contain %q", unmatched, want)
				}
				unmatched = slices.Delete(unmatched, matched, matched+1)
			}
		})
	}
}
