package netif

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const (
	// DefaultHealthStatePath is the shell health daemon's runtime state file.
	DefaultHealthStatePath = "/var/run/mwan-health.state"
	// HealthStateHealthy is the state written for a WAN that passed probes.
	HealthStateHealthy = "healthy"
	// HealthStateUnhealthy is the state written for a WAN that failed probes.
	HealthStateUnhealthy = "unhealthy"
	// HealthStateUnknown is the fallback when no state is recorded for a WAN.
	HealthStateUnknown = "unknown"
)

// HealthStates maps WAN names to the state strings from the health state file.
type HealthStates map[string]string

// ReadHealthState reads and parses a health state file.
func ReadHealthState(path string) (HealthStates, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return HealthStates{}, nil
		}
		slog.Error("netif: ReadHealthState open failed", "path", path, "err", err)
		return nil, fmt.Errorf("open health state %q: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()

	states := HealthStates{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		wanName, state, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		states[strings.TrimSpace(wanName)] = strings.TrimSpace(state)
	}
	if err := scanner.Err(); err != nil {
		slog.Error("netif: ReadHealthState scan failed", "path", path, "err", err)
		return nil, fmt.Errorf("scan health state %q: %w", path, err)
	}
	return states, nil
}

// State returns the recorded state for wanName, or unknown when it is absent.
func (s HealthStates) State(wanName string) string {
	state, ok := s[wanName]
	if !ok {
		return HealthStateUnknown
	}
	return state
}

// HealthIsHealthy reports whether a health state should be treated as usable.
func HealthIsHealthy(state string) bool {
	return state != HealthStateUnhealthy
}
