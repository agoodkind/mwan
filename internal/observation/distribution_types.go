package observation

import (
	"net/netip"
	"time"
)

// ProviderIngress separates current selection policy from the verified capture mapping.
type ProviderIngress struct {
	ConnectionID   string    `json:"connection_id"`
	Family         Family    `json:"family"`
	Bridge         string    `json:"bridge"`
	PortInterface  string    `json:"port_interface"`
	DestinationMAC string    `json:"destination_mac"`
	Tier           uint8     `json:"tier"`
	Weight         int       `json:"weight"`
	Eligible       bool      `json:"eligible"`
	ObservedAt     time.Time `json:"observed_at"`
}

// DistributionPlan requires current provider policy and explicit downstream probes.
type DistributionPlan struct {
	HashMode                  string            `json:"hash_mode"`
	ActiveTier                uint8             `json:"active_tier"`
	ObservedAt                time.Time         `json:"observed_at"`
	RoutingGeneration         uint64            `json:"routing_generation"`
	MinimumSamplesPerProvider int               `json:"minimum_samples_per_provider"`
	Providers                 []ProviderIngress `json:"providers"`
	Requests                  []CheckSpec       `json:"requests"`
}

// TCPIngress records an actual initial TCP packet addressed to a verified provider endpoint.
type TCPIngress struct {
	At              time.Time  `json:"at"`
	Source          netip.Addr `json:"source"`
	Destination     netip.Addr `json:"destination"`
	SourcePort      uint16     `json:"source_port"`
	DestinationPort uint16     `json:"destination_port"`
	DestinationMAC  string     `json:"destination_mac"`
	ConnectionID    string     `json:"connection_id"`
}

// CaptureReady records the verified kernel mapping and successful socket binding before probes.
type CaptureReady struct {
	ConnectionID   string    `json:"connection_id"`
	Interface      string    `json:"interface"`
	PortInterface  string    `json:"port_interface"`
	DestinationMAC string    `json:"destination_mac"`
	At             time.Time `json:"at"`
}

// ProviderShare reports configured weights and actual request counts without a statistical guarantee.
type ProviderShare struct {
	Provider ProviderIngress `json:"provider"`
	Samples  int             `json:"samples"`
}
