package config

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"goodkind.io/mwan/internal/vpp"
)

// VPPSection requires explicit process resources instead of host defaults.
type VPPSection struct {
	APISocket         string           `toml:"api_socket"`
	StatsSocket       string           `toml:"stats_socket"`
	ExpectedVersion   string           `toml:"expected_version"`
	RequestTimeout    string           `toml:"request_timeout"`
	StateFile         string           `toml:"state_file"`
	Profile           bool             `toml:"profile"`
	ProfileExecutable string           `toml:"profile_executable"`
	ProfileDuration   string           `toml:"profile_duration"`
	Interface         []VPPTAPSection  `toml:"interface"`
	NAT64             *VPPNAT64Section `toml:"nat64"`
}

// VPPRXMode preserves the receive mode stored in TOML until the VPP API adapter runs.
type VPPRXMode string

const (
	// VPPRXPolling checks receive queues continuously for packets.
	VPPRXPolling VPPRXMode = "polling"
	// VPPRXInterrupt waits for receive queue notifications.
	VPPRXInterrupt VPPRXMode = "interrupt"
	// VPPRXAdaptive switches receive queues between polling and notification modes.
	VPPRXAdaptive VPPRXMode = "adaptive"
)

// VPPTAPSection joins VPP and host connection IDs without repeating network policy.
type VPPTAPSection struct {
	ConnectionID     string    `toml:"connection_id"`
	HostConnectionID string    `toml:"host_connection_id"`
	ID               uint32    `toml:"id"`
	RXRingSize       uint16    `toml:"rx_ring_size"`
	TXRingSize       uint16    `toml:"tx_ring_size"`
	RXQueues         uint16    `toml:"rx_queues"`
	TXQueues         uint16    `toml:"tx_queues"`
	Worker           uint32    `toml:"worker"`
	RXMode           VPPRXMode `toml:"rx_mode"`
}

// VPPNAT64Section limits the hash tables that the VPP process allocates.
type VPPNAT64Section struct {
	BIBBuckets    uint32 `toml:"bib_buckets"`
	BIBMemorySize uint32 `toml:"bib_memory_size"`
	STBuckets     uint32 `toml:"st_buckets"`
	STMemorySize  uint32 `toml:"st_memory_size"`
}

// Endpoints rejects invalid transport settings before any socket connection.
func (section VPPSection) Endpoints() (settings vpp.Endpoints, failure error) {
	defer func() {
		if failure != nil {
			slog.Error("read VPP transport settings failed", "error", failure)
		}
	}()
	timeout, err := time.ParseDuration(section.RequestTimeout)
	if err != nil {
		return vpp.Endpoints{}, fmt.Errorf("VPP request_timeout: %w", err)
	}
	settings = vpp.Endpoints{APISocket: section.APISocket, StatsSocket: section.StatsSocket, ExpectedVersion: section.ExpectedVersion, Timeout: timeout}
	if err := settings.Validate(); err != nil {
		return settings, fmt.Errorf("validate VPP transport settings: %w", err)
	}
	return settings, nil
}

// Validate rejects incomplete process resources before a daemon can apply them.
func (section VPPSection) Validate() (failure error) {
	defer func() {
		if failure != nil {
			slog.Error("validate VPP process resources failed", "error", failure)
		}
	}()
	if _, err := section.Endpoints(); err != nil {
		return fmt.Errorf("validate VPP process transport: %w", err)
	}
	if !filepath.IsAbs(section.StateFile) {
		return fmt.Errorf("VPP state_file must be an absolute path")
	}
	if section.Profile {
		if !filepath.IsAbs(section.ProfileExecutable) {
			return fmt.Errorf("VPP profile_executable must be an absolute path when profiling is enabled")
		}
		duration, err := time.ParseDuration(section.ProfileDuration)
		if err != nil || duration <= 0 {
			return fmt.Errorf("VPP profile_duration must be a positive duration when profiling is enabled")
		}
	}
	ids := make(map[uint32]bool, len(section.Interface))
	connections := make(map[string]bool, len(section.Interface))
	for _, resource := range section.Interface {
		if resource.ConnectionID == "" || resource.HostConnectionID == "" || resource.ConnectionID == resource.HostConnectionID {
			return fmt.Errorf("VPP TAP requires distinct connection_id and host_connection_id")
		}
		if ids[resource.ID] || connections[resource.ConnectionID] {
			return fmt.Errorf("VPP TAP IDs and connection IDs must be unique")
		}
		ids[resource.ID] = true
		connections[resource.ConnectionID] = true
		if resource.RXRingSize == 0 || resource.TXRingSize == 0 || resource.RXQueues == 0 || resource.TXQueues == 0 {
			return fmt.Errorf("VPP TAP %d requires positive ring sizes and queue counts", resource.ID)
		}
		switch resource.RXMode {
		case VPPRXPolling, VPPRXInterrupt, VPPRXAdaptive:
		default:
			return fmt.Errorf("VPP TAP %d rx_mode must be polling, interrupt, or adaptive", resource.ID)
		}
	}
	if section.NAT64 != nil {
		resource := section.NAT64
		if resource.BIBBuckets == 0 || resource.BIBMemorySize == 0 || resource.STBuckets == 0 || resource.STMemorySize == 0 {
			return fmt.Errorf("VPP NAT64 requires positive BIB and session table resource sizes")
		}
	}
	return nil
}
