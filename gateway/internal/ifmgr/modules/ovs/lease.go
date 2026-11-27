package ovs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

var (
	errLeaseOperand   = errors.New("expected a complete DHCP address or prefix operand")
	errLeaseMissing   = errors.New("connection has no valid matching DHCP assignment")
	errLeaseAmbiguous = errors.New("connection has multiple distinct valid DHCP assignments")
)

type leaseValues struct {
	connections map[string]wanstate.ConnectionState
	now         time.Time
}

type leaseKind string

const (
	ipv4Lease      leaseKind = "dhcpv4"
	ipv6Delegation leaseKind = "dhcpv6-pd"
)

type unavailableLeaseError struct {
	reference string
	reason    error
}

func (e unavailableLeaseError) Error() string { return e.reference + ": " + e.reason.Error() }

func (v leaseValues) value(reference string) (string, error) {
	kind, id, ok := strings.Cut(reference, ":")
	if !ok || id == "" {
		return "", errLeaseOperand
	}

	prefix, err := v.assignment(leaseKind(kind), id)
	if err != nil {
		return "", err
	}

	if leaseKind(kind) == ipv4Lease {
		return prefix.Addr().String(), nil
	}

	return prefix.String(), nil
}

func (v leaseValues) assignment(kind leaseKind, id string) (netip.Prefix, error) {
	reference := string(kind) + ":" + id
	connection := v.connections[id]
	var assignments []interfaceintent.Assignment
	var required interfaceintent.AssignmentKind
	switch kind {
	case ipv4Lease:
		required = interfaceintent.AssignmentDHCPv4
		assignments = connection.IPv4.Assignments
	case ipv6Delegation:
		required = interfaceintent.AssignmentDHCPv6IAPD
		assignments = connection.IPv6.Assignments
	default:
		return netip.Prefix{}, errLeaseOperand
	}

	var selected netip.Prefix

	for _, assignment := range assignments {
		if assignment.Kind != required || !assignment.Valid || !assignment.Value.IsValid() {
			continue
		}

		if deadline := assignment.ValidUntil; deadline != nil && !deadline.IsZero() &&
			!v.now.Before(*deadline) {
			continue
		}

		value := assignment.Value.Masked()
		if required == interfaceintent.AssignmentDHCPv4 {
			address := assignment.Value.Addr()
			if !address.Is4() {
				continue
			}

			value = netip.PrefixFrom(address, address.BitLen())
		} else if !value.Addr().Is6() || value.Addr().Is4In6() {
			continue
		}

		if selected.IsValid() && selected != value {
			return netip.Prefix{}, unavailableLeaseError{
				reference: reference,
				reason:    errLeaseAmbiguous,
			}
		}

		selected = value
	}

	if !selected.IsValid() {
		return netip.Prefix{}, unavailableLeaseError{reference: reference, reason: errLeaseMissing}
	}

	return selected, nil
}

func (v leaseValues) bind(expression string) (string, error) {
	var result strings.Builder

	for {
		before, rest, found := strings.Cut(expression, "${")
		result.WriteString(before)
		if !found {
			return result.String(), nil
		}

		ref, after, closed := strings.Cut(rest, "}")
		startsOperand := before == "" || strings.HasSuffix(before, "=") ||
			strings.HasSuffix(before, ":")
		endsOperand := after == "" || strings.HasPrefix(after, ",") ||
			strings.HasPrefix(after, ")") ||
			strings.HasPrefix(after, "->")
		if !closed || !startsOperand || !endsOperand {
			return "", errLeaseOperand
		}

		value, err := v.value(ref)
		if err != nil {
			return "", err
		}

		result.WriteString(value)
		expression = after
	}
}

func (v leaseValues) actions(source []ovsconfig.Action) ([]ovsconfig.Action, error) {
	actions := slices.Clone(source)
	for index, action := range actions {
		argument, err := v.bind(action.Argument)
		if err != nil {
			return nil, err
		}

		actions[index].Argument = argument
	}

	return actions, nil
}

func (v leaseValues) bindFlow(flow *ovsconfig.Flow) error {
	flow.Match = slices.Clone(flow.Match)
	for index, entry := range flow.Match {
		if !strings.Contains(entry.Value, "${") {
			continue
		}

		ref, ok := strings.CutPrefix(entry.Value, "${")
		ref, closed := strings.CutSuffix(ref, "}")
		if !ok || !closed || strings.ContainsAny(ref, "{}") {
			return errLeaseOperand
		}

		value, err := v.value(ref)
		if err != nil {
			return err
		}

		flow.Match[index].Value = value
	}

	actions, err := v.actions(flow.Actions)
	flow.Actions = actions

	return err
}

func (v leaseValues) openFlow(
	ctx context.Context,
	cfg ovsconfig.OpenFlowConfig,
) (ovsconfig.OpenFlowConfig, error) {
	cfg.Mappings = v.resolveMappings(ctx, cfg.Mappings)

	flows := make([]ovsconfig.Flow, 0, len(cfg.Flows))
	for _, flow := range cfg.Flows {
		if err := v.bindFlow(&flow); err != nil {
			if errors.Is(err, errLeaseOperand) {
				return cfg, fmt.Errorf("flow %q: %w", flow.Name, err)
			}

			slog.WarnContext(ctx, "Omitting OpenFlow rule", "flow", flow.Name, "err", err)

			continue
		}

		flows = append(flows, flow)
	}

	cfg.Flows = flows

	cfg.Groups = slices.Clone(cfg.Groups)
	for index, group := range cfg.Groups {
		buckets := make([]ovsconfig.Bucket, 0, len(group.Buckets))
		for _, bucket := range group.Buckets {
			var err error
			bucket.Actions, err = v.actions(bucket.Actions)
			if err != nil {
				if errors.Is(err, errLeaseOperand) {
					return cfg, fmt.Errorf("group %d bucket %d: %w", group.ID, bucket.ID, err)
				}

				slog.WarnContext(
					ctx,
					"Omitting OpenFlow bucket",
					"group",
					group.ID,
					"bucket",
					bucket.ID,
					"err",
					err,
				)

				continue
			}

			buckets = append(buckets, bucket)
		}

		cfg.Groups[index].Buckets = buckets
	}

	return cfg, nil
}
