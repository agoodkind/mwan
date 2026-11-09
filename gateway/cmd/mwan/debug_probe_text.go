package main

import (
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"
	"time"

	"goodkind.io/mwan/internal/netif"
)

// WriteDebugConnectivityText preserves the operator table and reports write failures.
func WriteDebugConnectivityText(
	output io.Writer,
	logger *slog.Logger,
	results []debugConnectivityWANResult,
) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "WAN\tIFACE\tIPv4\tIPv6\tP4\tP6\tNPT\tPD"); err != nil {
		logger.Warn("write connectivity heading failed", "err", err)
		return fmt.Errorf("connectivity: write heading: %w", err)
	}

	for _, result := range results {
		ipv4, ipv6, prefix := debugNoValue, debugNoValue, debugNoValue
		if result.IPv4Present {
			ipv4 = result.IPv4
		}

		if result.IPv6Present {
			ipv6 = result.IPv6
		}

		if result.PrefixPresent {
			prefix = result.Prefix.String()
		}

		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			result.WAN.Name,
			result.WAN.Iface,
			ipv4,
			ipv6,
			result.Ping4Status,
			result.Ping6Status,
			result.NPTStatus,
			prefix,
		); err != nil {
			logger.Warn("write connectivity row failed", "iface", result.WAN.Iface, "err", err)
			return fmt.Errorf("connectivity: write row: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		logger.Warn("flush connectivity table failed", "err", err)
		return fmt.Errorf("connectivity: render table: %w", err)
	}

	for _, result := range results {
		prefix := ""
		if result.Prefix.IsValid() {
			prefix = result.Prefix.String()
		}

		if _, err := fmt.Fprintf(
			output,
			"\nWAN: %s\n  id: %s\n  interface: %s\n  IPv4: %s\n  IPv4 present: %t\n  IPv6: %s\n  IPv6 present: %t\n  prefix: %s\n  prefix present: %t\n  NPT present: %t\n",
			result.WAN.Name,
			result.WAN.ID,
			result.WAN.Iface,
			result.IPv4,
			result.IPv4Present,
			result.IPv6,
			result.IPv6Present,
			prefix,
			result.PrefixPresent,
			result.NPTPresent,
		); err != nil {
			logger.Warn("write connectivity details failed", "err", err)
			return fmt.Errorf("connectivity: write details: %w", err)
		}

		for _, address := range result.Addresses {
			if _, err := fmt.Fprintf(
				output,
				"  address: %s\n    family: %s\n    flags: %d\n    scope: %d\n    preferred lifetime: %d\n    valid lifetime: %d\n    observed at: %s\n    origin: %s\n",
				address.CIDR,
				address.Family,
				address.Flags,
				address.Scope,
				address.PreferredLifetime,
				address.ValidLifetime,
				address.ObservedAt.Format(time.RFC3339Nano),
				address.Origin,
			); err != nil {
				logger.Warn("write connectivity address failed", "err", err)
				return fmt.Errorf("connectivity: write address: %w", err)
			}
		}

		for _, attempts := range [][]netif.PingAttempt{result.Ping4, result.Ping6} {
			for _, attempt := range attempts {
				if err := writeDebugPingAttempt(output, logger, attempt); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func writeDebugPingAttempt(output io.Writer, logger *slog.Logger, attempt netif.PingAttempt) error {
	result := attempt.RTT.String()
	if attempt.Error != "" {
		result = "FAIL: " + attempt.Error
	}

	if _, err := fmt.Fprintf(
		output,
		"  attempt %d: %s\n    interface: %s\n    family: %s\n    target: %s\n    timeout: %s\n",
		attempt.Attempt,
		result,
		attempt.Interface,
		attempt.Family,
		attempt.Target,
		attempt.Timeout,
	); err != nil {
		logger.Warn("write ping details failed", "err", err)
		return fmt.Errorf("write ping details: %w", err)
	}

	if attempt.Error != "" {
		if _, err := fmt.Fprintf(output, "    rtt: %s\n", attempt.RTT); err != nil {
			logger.Warn("write ping RTT failed", "err", err)
			return fmt.Errorf("write ping RTT: %w", err)
		}
	} else if _, err := io.WriteString(output, "    error: \n"); err != nil {
		logger.Warn("write ping error failed", "err", err)
		return fmt.Errorf("write ping error: %w", err)
	}

	return nil
}

// WriteDebugPingText preserves attempt results and reports write failures.
func WriteDebugPingText(
	output io.Writer,
	logger *slog.Logger,
	view debugView,
	attempts []netif.PingAttempt,
) error {
	received := 0

	for _, attempt := range attempts {
		if attempt.Attempt == 1 {
			if _, err := fmt.Fprintf(
				output,
				"%s %s via %s\n",
				debugPingHeading(view),
				attempt.Target,
				attempt.Interface,
			); err != nil {
				logger.Warn("write ping heading failed", "view", view, "err", err)
				return fmt.Errorf("%s: write heading: %w", view, err)
			}

			received = 0
		}

		if attempt.Error == "" {
			received++
		}

		if err := writeDebugPingAttempt(output, logger, attempt); err != nil {
			return err
		}

		if attempt.Attempt == debugPingAttempts {
			if _, err := fmt.Fprintf(
				output,
				"%d transmitted, %d received\n\n",
				debugPingAttempts,
				received,
			); err != nil {
				logger.Warn("write ping counts failed", "view", view, "err", err)
				return fmt.Errorf("%s: write counts: %w", view, err)
			}
		}
	}

	return nil
}

// WriteDebugHTTPText preserves response bodies and reports write failures.
func WriteDebugHTTPText(
	output io.Writer,
	logger *slog.Logger,
	view debugView,
	observations []debugHTTPObservation,
) error {
	for _, observation := range observations {
		var heading string
		switch view {
		case debugViewCurl4, debugViewCurl6:
			heading = fmt.Sprintf("%s %s via %s\n", view, observation.Target, observation.Interface)
		case debugViewLB4Ifaces, debugViewLB6Ifaces:
			heading = fmt.Sprintf(
				"%s iter %d via %s -> %s: ",
				view,
				observation.Iteration,
				observation.Interface,
				observation.Target,
			)
		case debugViewLB4, debugViewLB6:
			heading = fmt.Sprintf(
				"%s iter %d -> %s: ",
				view,
				observation.Iteration,
				observation.Target,
			)
		case debugViewConnectivity, debugViewNPT, debugViewPing4, debugViewPing6,
			debugViewPolicy, debugViewPrefixes, debugViewRoutes, debugViewSim4,
			debugViewSim6, debugViewStats, debugViewStatus, debugViewSystemd,
			debugViewTraceStart, debugViewTraceStop, debugViewTraceTail:
			return fmt.Errorf("debug view %q does not render HTTP probes", view)
		default:
			return fmt.Errorf("debug view %q does not render HTTP probes", view)
		}

		if _, err := fmt.Fprintf(
			output,
			"%s\n  interface: %s\n  family: %s\n  target: %s\n  iteration: %d\n  timeout: %s\n  status code: %d\n  body: %s\n",
			heading,
			observation.Interface,
			observation.Family,
			observation.Target,
			observation.Iteration,
			observation.Timeout,
			observation.StatusCode,
			observation.Body,
		); err != nil {
			logger.Warn(
				"write HTTP result failed",
				"view",
				view,
				"iteration",
				observation.Iteration,
				"err",
				err,
			)
			return fmt.Errorf("%s: write response: %w", view, err)
		}

		if observation.WAN != nil {
			wan := observation.WAN
			if _, err := fmt.Fprintf(
				output,
				"  WAN id: %s\n  WAN name: %s\n  WAN interface: %s\n",
				wan.ID,
				wan.Name,
				wan.Iface,
			); err != nil {
				logger.Warn("write HTTP WAN details failed", "err", err)
				return fmt.Errorf("%s: write WAN details: %w", view, err)
			}
		}

		errorLine := "  error: \n"
		if observation.Error != "" {
			errorLine = "  FAIL: " + observation.Error + "\n"
		}

		if _, err := io.WriteString(output, errorLine); err != nil {
			logger.Warn("write HTTP error failed", "err", err)
			return fmt.Errorf("%s: write error: %w", view, err)
		}
	}

	return nil
}
