package config

import (
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"time"
)

// IfMgrNativeOffloadSection configures process resources without duplicating network policy.
type IfMgrNativeOffloadSection struct {
	Enabled             bool    `toml:"enabled"`
	CPU                 *uint32 `toml:"cpu"`
	MemoryMiB           uint32  `toml:"memory_mib"`
	HugepageDirectory   string  `toml:"hugepage_directory"`
	FilePrefix          string  `toml:"file_prefix"`
	PairCapacity        *uint32 `toml:"pair_capacity"`
	MbufCount           *uint32 `toml:"mbuf_count"`
	FlowCapacity        *uint32 `toml:"flow_capacity"`
	EventCapacity       *uint32 `toml:"event_capacity"`
	ObservationCapacity *uint32 `toml:"observation_capacity"`
	SocketBufferBytes   *uint32 `toml:"socket_buffer_bytes"`
	ObservationMaxAge   string  `toml:"observation_max_age"`
	ResyncInterval      string  `toml:"resync_interval"`
	CounterInterval     string  `toml:"counter_interval"`
	RequestTimeout      string  `toml:"request_timeout"`
	ResyncTimeout       string  `toml:"resync_timeout"`
	ShutdownTimeout     string  `toml:"shutdown_timeout"`
	IdleTimeout         string  `toml:"idle_timeout"`
	IdleWait            string  `toml:"idle_wait"`
}

// NativeOffloadProcessSettings contains checked values shared by the loader and module.
type NativeOffloadProcessSettings struct {
	CPU                 uint32
	MemoryMiB           uint32
	HugepageDirectory   string
	FilePrefix          string
	PairCapacity        uint32
	MbufCount           uint32
	FlowCapacity        uint32
	EventCapacity       uint32
	ObservationCapacity uint32
	SocketBufferBytes   uint32
	ObservationMaxAge   time.Duration
	ResyncInterval      time.Duration
	CounterInterval     time.Duration
	RequestTimeout      time.Duration
	ResyncTimeout       time.Duration
	ShutdownTimeout     time.Duration
	IdleTimeout         time.Duration
	IdleWait            time.Duration
}

const (
	nativeDefaultPairCapacity        uint32 = 1024
	nativeDefaultMbufCount           uint32 = 8192
	nativeDefaultFlowCapacity        uint32 = 4096
	nativeDefaultEventCapacity       uint32 = 1024
	nativeDefaultObservationCapacity uint32 = 4096
	nativeDefaultSocketBufferBytes   uint32 = 1048576
	nativeMaxPairCapacity            uint32 = 65536
	nativeMaxMbufCount               uint32 = 1048576
	nativeMaxFilePrefix                     = 63
	nativeMaxHugepageDirectory              = 255
	nativeDefaultObservationMaxAge          = 500 * time.Millisecond
	nativeDefaultRequestTimeout             = 500 * time.Millisecond
	nativeDefaultResyncInterval             = 30 * time.Second
	nativeDefaultCounterInterval            = time.Second
	nativeDefaultResyncTimeout              = 3 * time.Second
	nativeDefaultShutdownTimeout            = 10 * time.Second
	nativeDefaultIdleTimeout                = 30 * time.Second
	nativeDefaultIdleWait                   = 10 * time.Microsecond
)

// Resolve rejects enabled settings before the command can change kernel state.
func (section *IfMgrNativeOffloadSection) Resolve() (NativeOffloadProcessSettings, error) {
	var settings NativeOffloadProcessSettings
	if section == nil || !section.Enabled {
		return settings, nil
	}
	if section.CPU == nil || section.MemoryMiB == 0 {
		return settings, fmt.Errorf("nativeoffload requires an explicit cpu and positive memory_mib")
	}
	if !filepath.IsAbs(section.HugepageDirectory) || len(section.HugepageDirectory) > nativeMaxHugepageDirectory {
		return settings, fmt.Errorf("nativeoffload requires an absolute hugepage_directory of at most %d bytes", nativeMaxHugepageDirectory)
	}
	if section.FilePrefix == "" || len(section.FilePrefix) > nativeMaxFilePrefix {
		return settings, fmt.Errorf("nativeoffload requires file_prefix with 1 to %d bytes", nativeMaxFilePrefix)
	}
	for _, value := range section.FilePrefix {
		if value != '-' && value != '_' && (value < '0' || value > '9') && (value < 'A' || value > 'Z') && (value < 'a' || value > 'z') {
			return settings, fmt.Errorf("nativeoffload file_prefix permits only ASCII letters, digits, hyphen and underscore")
		}
	}
	settings.CPU, settings.MemoryMiB = *section.CPU, section.MemoryMiB
	settings.HugepageDirectory, settings.FilePrefix = section.HugepageDirectory, section.FilePrefix
	if err := section.resolveLimits(&settings); err != nil {
		return NativeOffloadProcessSettings{}, err
	}
	if err := section.resolveDurations(&settings); err != nil {
		return NativeOffloadProcessSettings{}, err
	}
	return settings, nil
}

func (section *IfMgrNativeOffloadSection) resolveLimits(settings *NativeOffloadProcessSettings) error {
	for _, field := range []struct {
		name     string
		input    *uint32
		fallback uint32
		maximum  uint32
		output   *uint32
	}{
		{"pair_capacity", section.PairCapacity, nativeDefaultPairCapacity, nativeMaxPairCapacity, &settings.PairCapacity},
		{"mbuf_count", section.MbufCount, nativeDefaultMbufCount, nativeMaxMbufCount, &settings.MbufCount},
		{"flow_capacity", section.FlowCapacity, nativeDefaultFlowCapacity, math.MaxInt32, &settings.FlowCapacity},
		{"event_capacity", section.EventCapacity, nativeDefaultEventCapacity, math.MaxInt32, &settings.EventCapacity},
		{"observation_capacity", section.ObservationCapacity, nativeDefaultObservationCapacity, math.MaxInt32, &settings.ObservationCapacity},
		{"socket_buffer_bytes", section.SocketBufferBytes, nativeDefaultSocketBufferBytes, math.MaxInt32, &settings.SocketBufferBytes},
	} {
		value := field.fallback
		if field.input != nil {
			value = *field.input
		}
		if value == 0 || value > field.maximum {
			return fmt.Errorf("nativeoffload %s must be between 1 and %d", field.name, field.maximum)
		}
		*field.output = value
	}
	if settings.FlowCapacity < settings.PairCapacity {
		return fmt.Errorf("nativeoffload flow_capacity must be at least pair_capacity")
	}
	return nil
}

func (section *IfMgrNativeOffloadSection) resolveDurations(settings *NativeOffloadProcessSettings) error {
	for _, field := range []struct {
		name      string
		input     string
		fallback  time.Duration
		allowZero bool
		output    *time.Duration
	}{
		{"observation_max_age", section.ObservationMaxAge, nativeDefaultObservationMaxAge, false, &settings.ObservationMaxAge},
		{"resync_interval", section.ResyncInterval, nativeDefaultResyncInterval, false, &settings.ResyncInterval},
		{"counter_interval", section.CounterInterval, nativeDefaultCounterInterval, false, &settings.CounterInterval},
		{"request_timeout", section.RequestTimeout, nativeDefaultRequestTimeout, false, &settings.RequestTimeout},
		{"resync_timeout", section.ResyncTimeout, nativeDefaultResyncTimeout, false, &settings.ResyncTimeout},
		{"shutdown_timeout", section.ShutdownTimeout, nativeDefaultShutdownTimeout, false, &settings.ShutdownTimeout},
		{"idle_timeout", section.IdleTimeout, nativeDefaultIdleTimeout, false, &settings.IdleTimeout},
		{"idle_wait", section.IdleWait, nativeDefaultIdleWait, true, &settings.IdleWait},
	} {
		value := field.fallback
		if field.input != "" {
			parsed, err := time.ParseDuration(field.input)
			if err != nil {
				slog.Warn("native offload duration invalid", "field", field.name, "err", err)
				return fmt.Errorf("nativeoffload %s: %w", field.name, err)
			}
			value = parsed
		}
		if value < 0 || value == 0 && !field.allowZero {
			if field.allowZero {
				return fmt.Errorf("nativeoffload %s must not be negative", field.name)
			}
			return fmt.Errorf("nativeoffload %s must be positive", field.name)
		}
		*field.output = value
	}
	if settings.IdleTimeout <= settings.CounterInterval {
		return fmt.Errorf("nativeoffload idle_timeout must exceed counter_interval")
	}
	return nil
}
