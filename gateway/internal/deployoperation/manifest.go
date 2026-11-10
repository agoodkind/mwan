package deployoperation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goodkind.io/mwan/internal/observation"
)

// Manifest requires explicit identities, bounded execution and positive application replies.
type Manifest struct {
	OperationID               string                    `json:"operation_id"`
	Generation                string                    `json:"generation"`
	VMID                      string                    `json:"vmid"`
	Snapshot                  string                    `json:"snapshot"`
	Baseline                  Identity                  `json:"baseline"`
	Target                    Identity                  `json:"target"`
	Paths                     Paths                     `json:"paths"`
	Deadline                  time.Time                 `json:"deadline"`
	WatchUnit                 string                    `json:"watch_unit"`
	PollSeconds               int                       `json:"poll_seconds"`
	ObservationTimeoutSeconds int                       `json:"observation_timeout_seconds"`
	RecoveryTimeoutSeconds    int                       `json:"recovery_timeout_seconds"`
	FailureThreshold          int                       `json:"failure_threshold"`
	RequiredFamilies          []observation.Family      `json:"required_families"`
	RequiredChecks            []observation.CheckSpec   `json:"required_checks"`
	ExpectedInterruptions     []ExpectedInterruption    `json:"expected_interruptions,omitempty"`
	RestoredRequiredChecks    []observation.CheckSpec   `json:"restored_required_checks"`
	Observation               observation.RuntimeConfig `json:"observation"`
}

// ExpectedInterruption authorizes bounded failures without changing their health verdict.
type ExpectedInterruption struct {
	Phase      string   `json:"phase"`
	CheckIDs   []string `json:"check_ids"`
	MaxSeconds int      `json:"max_seconds"`
}

// ReadManifest rejects unknown fields and trailing JSON before any runtime operation.
func ReadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open deploy manifest: %w", err)
	}
	defer func() { _ = file.Close() }()
	var manifest Manifest
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		slog.Warn("deploy manifest decoding failed")
		return Manifest{}, fmt.Errorf("decode deploy manifest: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("deploy manifest contains trailing JSON")
	}
	if err := manifest.validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (manifest Manifest) validate() error {
	if !validName(manifest.OperationID) || !validName(manifest.Generation) || !validName(manifest.Snapshot) {
		return fmt.Errorf("deploy manifest requires exact operation, generation and snapshot identities")
	}
	if !strings.HasSuffix(manifest.WatchUnit, ".service") || !validName(strings.TrimSuffix(manifest.WatchUnit, ".service")) {
		return fmt.Errorf("deploy watch requires an exact systemd service unit")
	}
	if manifest.PollSeconds <= 0 || manifest.ObservationTimeoutSeconds <= 0 || manifest.RecoveryTimeoutSeconds <= 0 || manifest.FailureThreshold <= 0 || manifest.Deadline.IsZero() {
		return fmt.Errorf("deploy manifest requires positive polling, observation, recovery and failure limits")
	}
	if err := manifest.Baseline.validate(); err != nil {
		return err
	}
	if err := manifest.Target.validate(); err != nil {
		return err
	}
	if manifest.Target.MachineID != manifest.Baseline.MachineID {
		return fmt.Errorf("deploy target machine must match the baseline machine")
	}
	for _, path := range []string{manifest.Paths.Executable, manifest.Paths.Network, manifest.Paths.Runtime, manifest.Observation.MachineIDPath, manifest.Observation.ProbeBinary} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("deploy manifest requires absolute identity and observation paths")
		}
	}
	for _, checks := range [][]observation.CheckSpec{manifest.RequiredChecks, manifest.RestoredRequiredChecks} {
		if err := manifest.validateChecks(checks); err != nil {
			return err
		}
	}
	checks := make(map[string]observation.CheckSpec, len(manifest.RequiredChecks))
	for _, check := range manifest.RequiredChecks {
		checks[check.ID] = check
	}
	phases := make(map[string]bool, len(manifest.ExpectedInterruptions))
	for _, interruption := range manifest.ExpectedInterruptions {
		if strings.TrimSpace(interruption.Phase) == "" || phases[interruption.Phase] || interruption.MaxSeconds <= 0 || len(interruption.CheckIDs) == 0 {
			return fmt.Errorf("expected interruptions require unique nonempty phases, positive bounds and check IDs")
		}
		phases[interruption.Phase] = true
		seen := make(map[string]bool, len(interruption.CheckIDs))
		for _, id := range interruption.CheckIDs {
			check, exists := checks[id]
			if !exists || seen[id] || check.Dimension != observation.DimensionInboundApplication {
				return fmt.Errorf("expected interruption check %s must identify a unique required inbound application check", id)
			}
			seen[id] = true
		}
	}
	return nil
}

func (manifest Manifest) validateChecks(checks []observation.CheckSpec) error {
	families := make(map[observation.Family]bool)
	for _, family := range manifest.RequiredFamilies {
		if (family != observation.FamilyIPv4 && family != observation.FamilyIPv6) || families[family] {
			return fmt.Errorf("deploy required families must contain unique IPv4 or IPv6 values")
		}
		families[family] = true
	}
	if len(families) == 0 {
		return fmt.Errorf("deploy manifest requires configured IP families")
	}
	if len(checks) == 0 {
		return fmt.Errorf("deploy manifest requires active and restored application checks")
	}
	seen := make(map[string]bool)
	inbound := make(map[observation.Family]bool)
	downstream := make(map[observation.Family]bool)
	for _, check := range checks {
		if err := observation.Validate(check); err != nil {
			slog.Warn("deploy application check validation failed")
			return fmt.Errorf("validate deploy check %s: %w", check.ID, err)
		}
		if seen[check.ID] {
			return fmt.Errorf("deploy check ID %s is duplicated", check.ID)
		}
		seen[check.ID] = true
		if check.Operation == observation.OperationHTTP || check.Operation == observation.OperationSSHBanner {
			if check.Dimension == observation.DimensionInboundApplication {
				inbound[check.Family] = true
			}
			if check.Dimension == observation.DimensionDownstreamApplication {
				downstream[check.Family] = true
			}
		}
	}
	for family := range families {
		if !inbound[family] || !downstream[family] {
			return fmt.Errorf("deploy checks require inbound and downstream application responses for %s", family)
		}
	}
	return nil
}
