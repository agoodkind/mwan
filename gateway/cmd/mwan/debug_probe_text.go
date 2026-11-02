package main

import (
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"

	"goodkind.io/mwan/internal/netif"
)

// WriteDebugConnectivityText preserves the operator table and reports write failures.
func WriteDebugConnectivityText(output io.Writer, logger *slog.Logger, results []debugConnectivityWANResult) error {
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
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", result.WAN.Name, result.WAN.Iface, ipv4, ipv6, result.Ping4Status, result.Ping6Status, result.NPTStatus, prefix); err != nil {
			logger.Warn("write connectivity row failed", "iface", result.WAN.Iface, "err", err)
			return fmt.Errorf("connectivity: write row: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		logger.Warn("flush connectivity table failed", "err", err)
		return fmt.Errorf("connectivity: render table: %w", err)
	}
	return nil
}

// WriteDebugPingText preserves attempt results and reports write failures.
func WriteDebugPingText(output io.Writer, logger *slog.Logger, view debugView, attempts []netif.PingAttempt) error {
	received := 0
	for _, attempt := range attempts {
		if attempt.Attempt == 1 {
			if _, err := fmt.Fprintf(output, "%s %s via %s\n", debugPingHeading(view), attempt.Target, attempt.Interface); err != nil {
				logger.Warn("write ping heading failed", "view", view, "err", err)
				return fmt.Errorf("%s: write heading: %w", view, err)
			}
			received = 0
		}
		if attempt.Error != "" {
			if _, err := fmt.Fprintf(output, "attempt %d: FAIL: %s\n", attempt.Attempt, attempt.Error); err != nil {
				logger.Warn("write ping attempt failed", "view", view, "attempt", attempt.Attempt, "err", err)
				return fmt.Errorf("%s: write attempt: %w", view, err)
			}
		} else {
			received++
			if _, err := fmt.Fprintf(output, "attempt %d: %s\n", attempt.Attempt, attempt.RTT); err != nil {
				logger.Warn("write ping attempt failed", "view", view, "attempt", attempt.Attempt, "err", err)
				return fmt.Errorf("%s: write attempt: %w", view, err)
			}
		}
		if attempt.Attempt == debugPingAttempts {
			if _, err := fmt.Fprintf(output, "%d transmitted, %d received\n\n", debugPingAttempts, received); err != nil {
				logger.Warn("write ping counts failed", "view", view, "err", err)
				return fmt.Errorf("%s: write counts: %w", view, err)
			}
		}
	}
	return nil
}

// WriteDebugHTTPText preserves response bodies and reports write failures.
func WriteDebugHTTPText(output io.Writer, logger *slog.Logger, view debugView, observations []debugHTTPObservation) error {
	for _, observation := range observations {
		var heading string
		switch view {
		case debugViewCurl4, debugViewCurl6:
			heading = fmt.Sprintf("%s %s via %s\n", view, observation.Target, observation.Interface)
		case debugViewLB4Ifaces, debugViewLB6Ifaces:
			heading = fmt.Sprintf("%s iter %d via %s -> %s: ", view, observation.Iteration, observation.Interface, observation.Target)
		case debugViewLB4, debugViewLB6:
			heading = fmt.Sprintf("%s iter %d -> %s: ", view, observation.Iteration, observation.Target)
		case debugViewConnectivity, debugViewNPT, debugViewPing4, debugViewPing6,
			debugViewPolicy, debugViewPrefixes, debugViewRoutes, debugViewSim4,
			debugViewSim6, debugViewStats, debugViewStatus, debugViewSystemd,
			debugViewTraceStart, debugViewTraceStop, debugViewTraceTail:
			return fmt.Errorf("debug view %q does not render HTTP probes", view)
		default:
			return fmt.Errorf("debug view %q does not render HTTP probes", view)
		}
		body := observation.Body
		if observation.Error != "" {
			body = "FAIL: " + observation.Error
		}
		ending := "\n"
		if view == debugViewCurl4 || view == debugViewCurl6 {
			ending = "\n\n"
		}
		if _, err := fmt.Fprint(output, heading, body, ending); err != nil {
			logger.Warn("write HTTP result failed", "view", view, "iteration", observation.Iteration, "err", err)
			return fmt.Errorf("%s: write response: %w", view, err)
		}
	}
	return nil
}
