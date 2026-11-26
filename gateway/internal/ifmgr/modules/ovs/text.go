package ovs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

type expressionType int

const (
	matchExpression expressionType = iota
	instructionExpression
	bucketExpression
)

var (
	errTextNUL       = errors.New("OpenFlow expressions and port names cannot contain NUL")
	errMatchConflict = errors.New("use either a match expression or individual match fields")
	errExpression    = errors.New("invalid OpenFlow expression entry")
)

func validateExpressions(ctx context.Context, config ovsconfig.Config) error {
	if config.OpenFlow == nil {
		return nil
	}

	// Before bridge creation, OVS has not assigned the configured ports their numbers.
	numbers := make(map[string]uint32, len(config.Ports))
	for index, port := range config.Ports {
		numbers[port.Name] = uint32(index + 1)
	}

	for _, flow := range config.OpenFlow.Flows {
		if len(flow.Match) == 0 && len(flow.Actions) == 0 {
			continue
		}

		if _, err := marshalFlow(ctx, flow, numbers); err != nil {
			slog.ErrorContext(ctx, "Invalid OpenFlow rule", "rule", flow.Name, "err", err)

			return fmt.Errorf("compile OpenFlow rule %q: %w", flow.Name, err)
		}
	}

	for _, group := range config.OpenFlow.Groups {
		for _, bucket := range group.Buckets {
			if len(bucket.Actions) == 0 {
				continue
			}

			if _, err := compileBucket(ctx, bucket, numbers); err != nil {
				slog.ErrorContext(
					ctx,
					"Invalid OpenFlow bucket",
					"group",
					group.ID,
					"bucket",
					bucket.ID,
					"err",
					err,
				)

				return fmt.Errorf(
					"compile OpenFlow group %d bucket %d: %w",
					group.ID,
					bucket.ID,
					err,
				)
			}
		}
	}

	return nil
}

func marshalEntry(name, value string) (string, error) {
	if name == "" || strings.ContainsAny(name, "():=, \t\r\n\x00") {
		slog.Error("Invalid OpenFlow entry name", "name", name, "err", errExpression)

		return "", fmt.Errorf("%w: invalid name %q", errExpression, name)
	}

	if value == "" {
		return name, nil
	}

	depth := 0

	for _, char := range value {
		switch char {
		case '(':
			depth++
		case ')':
			depth--
		case 0:
			return "", errTextNUL
		}

		if depth < 0 {
			return "", fmt.Errorf("%w: unmatched ')' in %s", errExpression, name)
		}
	}

	if depth != 0 {
		return "", fmt.Errorf("%w: unmatched '(' in %s", errExpression, name)
	}

	return name + "(" + value + ")", nil
}

func marshalMatchFields(matches []ovsconfig.Match, numbers map[string]uint32) ([][]byte, error) {
	entries := make([]string, 0, len(matches))
	for _, field := range matches {
		text, err := marshalEntry(field.Field, field.Value)
		if err != nil {
			return nil, err
		}

		entries = append(entries, text)
	}

	return marshalTextFields(matchExpression, strings.Join(entries, ","), numbers)
}

func marshalActionFields(
	kind expressionType,
	actions []ovsconfig.Action,
	numbers map[string]uint32,
) (
	[][]byte,
	error,
) {
	entries := make([]string, 0, len(actions))
	for _, action := range actions {
		text, err := marshalEntry(action.Name, action.Argument)
		if err != nil {
			return nil, err
		}

		entries = append(entries, text)
	}

	return marshalTextFields(kind, strings.Join(entries, ","), numbers)
}

func marshalTextFields(
	kind expressionType,
	expression string,
	numbers map[string]uint32,
) ([][]byte, error) {
	data, err := marshalText(kind, expression, numbers)
	if err != nil {
		return nil, err
	}

	var fields [][]byte

	for len(data) > 0 {
		if len(data) < oxmHeaderSize {
			return nil, errFlowSize
		}

		length := int(binary.BigEndian.Uint16(data[2:4]))
		if kind == matchExpression {
			length = oxmHeaderSize + int(data[3])
		}

		if length < oxmHeaderSize || length > len(data) {
			return nil, errFlowSize
		}

		fields = append(fields, data[:length])
		data = data[length:]
	}

	return fields, nil
}
