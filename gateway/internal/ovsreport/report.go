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

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

// Rule identifies an installed OpenFlow rule and its forwarding counters.
type Rule struct {
	Name       string  `json:"name"`
	Definition string  `json:"definition"`
	Forwarding string  `json:"forwarding"`
	Packets    *uint64 `json:"packets"`
	Offloaded  *uint64 `json:"offloaded_packets"`
}

var counters = regexp.MustCompile(`(?:^|, )(n_packets|n_offload_packets)=([0-9]+),`)

var (
	errOffloadStatsUnsupported = errors.New("ovs-vswitchd supports only basic rule counters")
	errDeadline                = errors.New("set a deadline for reading OVS rule counters")
	errRejected                = errors.New("ovs-vswitchd rejected bridge/dump-flows")
	errResponse                = errors.New("invalid ovs-vswitchd rule counter response")
	errActions                 = errors.New("missing actions in ovs-vswitchd rule")
	errCounts                  = errors.New("offloaded packets exceed total packets")
)

// Read returns packet counters for configured rules in configuration order.
//
// Set a context deadline for the control socket request. Check Offloaded
// before displaying the number of packets OVS counted in hardware.
//
//	rules, err := ovsreport.Read(ctx, socket, settings)
//	if err != nil {
//	    return err
//	}
//	for _, rule := range rules {
//	    if rule.Offloaded == nil {
//	        fmt.Printf("%s: hardware counters unavailable\n", rule.Name)
//	        continue
//	    }
//	    fmt.Printf("%s: %d hardware packets\n", rule.Name, *rule.Offloaded)
//	}
func Read(ctx context.Context, socket string, settings ovsconfig.Config) ([]Rule, error) {
	if settings.OpenFlow == nil {
		return []Rule{}, nil
	}

	names, err := ovsconfig.RuleCookies(settings.OpenFlow.Flows)
	if err != nil {
		slog.ErrorContext(ctx, "OpenFlow rule identification failed", "err", err)

		return nil, fmt.Errorf("identify configured OpenFlow rules: %w", err)
	}

	rules, err := dumpRules(ctx, socket, []string{"--offload-stats", settings.Bridge})
	if errors.Is(err, errOffloadStatsUnsupported) {
		rules, err = dumpRules(ctx, socket, []string{settings.Bridge})
	}

	if err != nil {
		return nil, err
	}

	byName := make(map[string]Rule, len(names))

	for _, rule := range rules {
		first, _, _ := strings.Cut(rule.Definition, ",")
		encoded, ok := strings.CutPrefix(first, "cookie=")
		if !ok {
			continue
		}

		cookie, err := strconv.ParseUint(encoded, 0, 64)
		if err != nil {
			slog.ErrorContext(ctx, "Invalid OVS rule cookie", "err", err)

			return nil, fmt.Errorf("parse OVS rule cookie: %w", err)
		}

		name, found := names[cookie]
		if !found {
			continue
		}

		if _, exists := byName[name]; exists {
			slog.ErrorContext(ctx, "Duplicate OVS rule cookie", "name", name, "err", errResponse)

			return nil, fmt.Errorf("%w: duplicate cookie for %q", errResponse, name)
		}

		rule.Name = name
		byName[name] = rule
	}

	result := make([]Rule, 0, len(names))

	for _, flow := range settings.OpenFlow.Flows {
		rule, ok := byName[flow.Name]
		if !ok {
			rule.Name = flow.Name
			rule.Forwarding = "unavailable"
		}

		result = append(result, rule)
	}

	return result, nil
}

func dumpRules(ctx context.Context, socket string, params []string) ([]Rule, error) {
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
		params,
		1,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		slog.ErrorContext(ctx, "OVS rule counter request failed", "err", err)

		return nil, fmt.Errorf("request OVS rule counters: %w", err)
	}

	var response struct {
		Result *string `json:"result"`
		Error  *string `json:"error"`
		ID     int     `json:"id"`
	}
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		slog.ErrorContext(ctx, "OVS rule counter response failed", "err", err)

		return nil, fmt.Errorf("read OVS rule counters: %w", err)
	}

	if response.ID != request.ID {
		return nil, errResponse
	}

	if response.Error != nil {
		if *response.Error == `"bridge/dump-flows" command takes at most 1 arguments` {
			return nil, errOffloadStatsUnsupported
		}

		slog.ErrorContext(ctx, "OVS rule counter request rejected", "err", *response.Error)

		return nil, fmt.Errorf("%w: %s", errRejected, *response.Error)
	}

	if response.Result == nil {
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
			Name:       "",
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
				rule.Forwarding = "unobserved"
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
