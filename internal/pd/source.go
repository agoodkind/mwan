package pd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
)

const defaultStateDir = "/var/lib/mwan"

// Source reads the current DHCPv6-PD delegated prefix for an interface.
type Source interface {
	// Prefix returns the current DHCPv6-PD delegated prefix for iface.
	// ok is false when no source can discover a prefix.
	Prefix(ctx context.Context, iface string) (prefix netip.Prefix, ok bool, err error)
}

type commandRunner func(
	ctx context.Context,
	log *slog.Logger,
	name string,
	args []string,
) ([]byte, error)

type prefixProbe func(ctx context.Context, iface string) (netip.Prefix, bool, error)

// DefaultSource reads systemd-networkd, kernel, journal, and cached PD state.
type DefaultSource struct {
	log        *slog.Logger
	stateDir   string
	runCommand commandRunner
	readOnly   bool

	mu    sync.Mutex
	cache map[string]netip.Prefix
}

// Option configures a DefaultSource at construction.
type Option func(*DefaultSource)

// ReadOnly makes the source skip the persistent state-file write after a live
// discovery, so inspection callers do not mutate /var/lib/mwan/pd-<iface>.
func ReadOnly() Option {
	return func(s *DefaultSource) {
		s.readOnly = true
	}
}

// New returns the default delegated-prefix source.
func New(log *slog.Logger, opts ...Option) *DefaultSource {
	if log == nil {
		log = slog.Default()
	}
	source := &DefaultSource{
		log:        log,
		stateDir:   defaultStateDir,
		runCommand: runCommandOutput,
		readOnly:   false,
		mu:         sync.Mutex{},
		cache:      make(map[string]netip.Prefix),
	}
	for _, opt := range opts {
		opt(source)
	}
	return source
}

// Prefix returns the first delegated prefix found for iface.
func (s *DefaultSource) Prefix(
	ctx context.Context,
	iface string,
) (netip.Prefix, bool, error) {
	log := s.log
	if log == nil {
		log = slog.Default()
	}
	if err := validateIface(iface); err != nil {
		log.WarnContext(ctx, "pd: invalid iface", "iface", iface, "err", err)
		return netip.Prefix{}, false, err
	}

	probes := []struct {
		name string
		run  prefixProbe
	}{
		{name: "networkd_dbus", run: s.prefixFromNetworkdDBus},
		{name: "networkctl", run: s.prefixFromNetworkctl},
		{name: "kernel_route", run: s.prefixFromKernelRoutes},
		{name: "journal", run: s.prefixFromJournal},
	}

	sourceErrors := make([]error, 0, len(probes)+1)
	for _, probe := range probes {
		prefix, ok, err := probe.run(ctx, iface)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Errorf("%s: %w", probe.name, err))
			continue
		}
		if !ok {
			continue
		}
		s.setCachedPrefix(iface, prefix)
		if !s.readOnly {
			s.writeStateFile(ctx, iface, prefix)
		}
		return prefix, true, nil
	}

	if prefix, ok := s.cachedPrefix(iface); ok {
		return prefix, true, nil
	}

	prefix, ok, err := s.prefixFromStateFile(ctx, iface)
	if err != nil {
		sourceErrors = append(sourceErrors, fmt.Errorf("state_file: %w", err))
	}
	if ok {
		s.setCachedPrefix(iface, prefix)
		return prefix, true, nil
	}
	if len(sourceErrors) == 0 {
		return netip.Prefix{}, false, nil
	}

	err = fmt.Errorf("pd prefix sources failed for %s: %w", iface, errors.Join(sourceErrors...))
	log.WarnContext(ctx, "pd: prefix sources failed", "iface", iface, "err", err)
	return netip.Prefix{}, false, err
}

func validateIface(iface string) error {
	if iface == "" {
		return errors.New("iface is empty")
	}
	if strings.Contains(iface, "/") {
		return fmt.Errorf("iface %q contains slash", iface)
	}
	if strings.ContainsRune(iface, '\x00') {
		return fmt.Errorf("iface %q contains NUL", iface)
	}
	return nil
}

func (s *DefaultSource) cachedPrefix(iface string) (netip.Prefix, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prefix, ok := s.cache[iface]
	return prefix, ok
}

func (s *DefaultSource) setCachedPrefix(iface string, prefix netip.Prefix) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cache == nil {
		s.cache = make(map[string]netip.Prefix)
	}
	s.cache[iface] = prefix
}
