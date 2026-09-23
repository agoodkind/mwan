package pd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
)

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

// DefaultSource reads the current systemd-networkd and kernel state.
type DefaultSource struct {
	log        *slog.Logger
	runCommand commandRunner
}

// New returns the default delegated-prefix source.
func New(log *slog.Logger) *DefaultSource {
	if log == nil {
		log = slog.Default()
	}
	return &DefaultSource{
		log:        log,
		runCommand: runCommandOutput,
	}
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
	}

	sourceErrors := make([]error, 0, len(probes))
	for _, probe := range probes {
		prefix, ok, err := probe.run(ctx, iface)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Errorf("%s: %w", probe.name, err))
			continue
		}
		if !ok {
			continue
		}
		return prefix, true, nil
	}
	if len(sourceErrors) == 0 {
		return netip.Prefix{}, false, nil
	}

	err := fmt.Errorf("pd prefix sources failed for %s: %w", iface, errors.Join(sourceErrors...))
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
