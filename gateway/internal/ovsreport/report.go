// Package ovsreport reads forwarding counters for OpenFlow rules.
package ovsreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Rule identifies an installed OpenFlow rule and its forwarding counters.
type Rule struct {
	Definition string  `json:"definition"`
	Forwarding string  `json:"forwarding"`
	Packets    *uint64 `json:"packets"`
	Offloaded  *uint64 `json:"offloaded_packets"`
}

var counters = regexp.MustCompile(`(?:^|, )(n_packets|n_offload_packets)=([0-9]+),`)

var (
	errDeadline = errors.New("set a deadline for reading OVS rule counters")
	errRejected = errors.New("ovs-vswitchd rejected bridge/dump-flows")
	errResponse = errors.New("invalid ovs-vswitchd rule counter response")
	errActions  = errors.New("missing actions in ovs-vswitchd rule")
	errCounts   = errors.New("offloaded packets exceed total packets")
)

// Read requests logical rule counters from ovs-vswitchd.
//
// Set a context deadline to bound the Unix control socket request.
//
//	rules, err := ovsreport.Read(ctx, socket, bridge)
func Read(ctx context.Context, socket, bridge string) ([]Rule, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errDeadline
	}

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		slog.ErrorContext(ctx, "OVS control socket connection failed", "err", err)

		return nil, fmt.Errorf("connect to ovs-vswitchd: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		slog.ErrorContext(ctx, "OVS control socket deadline failed", "err", err)

		return nil, fmt.Errorf("set control socket deadline: %w", err)
	}

	request := struct {
		Method string   `json:"method"`
		Params []string `json:"params"`
		ID     int      `json:"id"`
	}{
		"bridge/dump-flows",
		[]string{"--offload-stats", bridge},
		1,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		slog.ErrorContext(ctx, "OVS rule counter request failed", "err", err)

		return nil, fmt.Errorf("request OVS rule counters: %w", err)
	}

	var response struct {
		Result *string         `json:"result"`
		Error  json.RawMessage `json:"error"`
		ID     int             `json:"id"`
	}
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		slog.ErrorContext(ctx, "OVS rule counter response failed", "err", err)

		return nil, fmt.Errorf("read OVS rule counters: %w", err)
	}

	if len(response.Error) > 0 && string(response.Error) != "null" {
		slog.ErrorContext(ctx, "OVS rule counter request rejected", "err", response.Error)

		return nil, fmt.Errorf("%w: %s", errRejected, response.Error)
	}

	if response.ID != request.ID || response.Result == nil {
		return nil, errResponse
	}

	return unmarshalRules(*response.Result)
}

func unmarshalRules(dump string) ([]Rule, error) {
	rules := []Rule{}

	for line := range strings.Lines(dump) {
		definition := strings.TrimSpace(line)
		if definition == "" {
			continue
		}

		before, _, found := strings.Cut(definition, "actions=")
		if !found {
			return nil, fmt.Errorf("%w: %q", errActions, definition)
		}

		rule := Rule{
			Definition: definition,
			Forwarding: "unavailable",
			Packets:    nil,
			Offloaded:  nil,
		}

		for _, field := range counters.FindAllStringSubmatch(before, -1) {
			value, err := strconv.ParseUint(field[2], 10, 64)
			if err != nil {
				slog.Error("Invalid OVS packet counter", "field", field[1], "err", err)

				return nil, fmt.Errorf("parse %s: %w", field[1], err)
			}

			if value == math.MaxUint64 {
				continue
			}

			if field[1] == "n_packets" {
				rule.Packets = &value
			} else {
				rule.Offloaded = &value
			}
		}

		if rule.Packets != nil && rule.Offloaded != nil {
			total, offload := *rule.Packets, *rule.Offloaded
			switch {
			case offload > total:
				return nil, fmt.Errorf("%w: %q", errCounts, definition)
			case total == 0:
				rule.Forwarding = "no-traffic"
			case offload == 0:
				rule.Forwarding = "software"
			case offload == total:
				rule.Forwarding = "hardware"
			default:
				rule.Forwarding = "mixed"
			}
		}

		rules = append(rules, rule)
	}

	return rules, nil
}
