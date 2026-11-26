package ovs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

const (
	groupNamespace = 0x6d770000
	groupMask      = 0xffff0000
	maxGroupID     = 65535
	maxBucketID    = 0xfffffeff
)

var (
	errGroupID        = errors.New("group ID must be between 1 and 65535")
	errGroupType      = errors.New("group type must be all, select, indirect, or fast-failover")
	errGroupMethod    = errors.New("selection method requires a select group and at most 15 bytes")
	errDuplicateGroup = errors.New("group IDs must be unique")
	errBucketID       = errors.New("bucket ID must not use OpenFlow reserved values")
	errRawAction      = errors.New("raw action must contain one complete OpenFlow action")
	errRawProperty    = errors.New(
		"raw property must contain one complete padded OpenFlow property",
	)
)

func managedGroupID(id uint32) (uint32, error) {
	if id == 0 || id > maxGroupID {
		return 0, errGroupID
	}

	return groupNamespace | id, nil
}

func compileGroups(
	ctx context.Context,
	groups []ovsconfig.Group,
	numbers map[string]uint32,
) ([]*openflow15.GroupMod, error) {
	result := make([]*openflow15.GroupMod, 0, len(groups))

	seen := make(map[uint32]bool, len(groups))
	for _, group := range groups {
		if seen[group.ID] {
			slog.ErrorContext(
				ctx,
				"Duplicate OpenFlow group ID",
				"id",
				group.ID,
				"err",
				errDuplicateGroup,
			)

			return nil, fmt.Errorf("%w: %d", errDuplicateGroup, group.ID)
		}

		seen[group.ID] = true
		compiled, err := compileGroup(ctx, group, numbers)
		if err != nil {
			return nil, err
		}

		result = append(result, compiled)
	}

	return result, nil
}

func compileGroup(
	ctx context.Context,
	group ovsconfig.Group,
	numbers map[string]uint32,
) (*openflow15.GroupMod, error) {
	groupID, err := managedGroupID(group.ID)
	if err != nil {
		return nil, err
	}

	result := openflow15.NewGroupMod()
	result.GroupId = groupID
	switch group.Type {
	case ovsconfig.GroupAll:
		result.Type = openflow15.GT_ALL
	case ovsconfig.GroupSelect:
		result.Type = openflow15.GT_SELECT
	case ovsconfig.GroupIndirect:
		result.Type = openflow15.GT_INDIRECT
	case ovsconfig.GroupFastFailover:
		result.Type = openflow15.GT_FF
	default:
		return nil, errGroupType
	}

	if group.SelectionMethod != "" {
		if group.Type != ovsconfig.GroupSelect || len(group.SelectionMethod) > 15 {
			return nil, errGroupMethod
		}

		result.Properties = append(
			result.Properties,
			openflow15.NewNTRSelectionMethod(
				openflow15.NTRSelectionMethodType(group.SelectionMethod),
				group.SelectionParameter,
			),
		)
	}

	properties, err := rawProperties(group.RawProperties)
	if err != nil {
		return nil, err
	}

	result.Properties = append(result.Properties, properties...)
	if int(result.Len()) > maxFlowSize {
		return nil, errFlowSize
	}

	seen := make(map[uint32]bool, len(group.Buckets))
	for _, bucket := range group.Buckets {
		if seen[bucket.ID] {
			slog.ErrorContext(
				ctx,
				"Duplicate OpenFlow bucket ID",
				"id",
				bucket.ID,
				"err",
				errDuplicateGroup,
			)

			return nil, fmt.Errorf("%w: bucket %d", errDuplicateGroup, bucket.ID)
		}

		seen[bucket.ID] = true
		encoded, err := compileBucket(ctx, bucket, numbers)
		if err != nil {
			return nil, fmt.Errorf(
				"compile OpenFlow group %d bucket %d: %w",
				group.ID,
				bucket.ID,
				err,
			)
		}

		if int(result.Len())+int(encoded.Len()) > maxFlowSize {
			return nil, errFlowSize
		}

		result.AddBucket(*encoded)
	}

	return result, nil
}

func compileBucket(
	ctx context.Context,
	bucket ovsconfig.Bucket,
	numbers map[string]uint32,
) (*openflow15.Bucket, error) {
	if bucket.ID > maxBucketID {
		return nil, errBucketID
	}

	encoded := openflow15.NewBucket(bucket.ID)
	if bucket.Weight != nil {
		encoded.AddProperty(openflow15.NewGroupBucketPropWeight(*bucket.Weight))
	}

	properties, err := rawProperties(bucket.RawProperties)
	if err != nil {
		return nil, err
	}

	encoded.Properties = append(encoded.Properties, properties...)
	var actions []openflow15.Action
	if len(bucket.Actions) != 0 {
		if len(bucket.RawActions) != 0 {
			return nil, errRawAction
		}

		bucket.RawActions, err = marshalActionFields(bucketExpression, bucket.Actions, numbers)
		if err != nil {
			return nil, err
		}
	}

	for _, raw := range bucket.RawActions {
		if len(raw) < instructionSize || len(raw)%wireAlignment != 0 ||
			int(binary.BigEndian.Uint16(raw[2:4])) != len(raw) {
			return nil, errRawAction
		}

		actions = append(
			actions,
			wireAction{
				Buffer: util.NewBuffer(raw),
				header: openflow15.ActionHeader{
					Type:   binary.BigEndian.Uint16(raw),
					Length: binary.BigEndian.Uint16(raw[2:4]),
				},
			},
		)
	}

	if bucket.Output != "" {
		port, found := numbers[bucket.Output]
		if !found {
			slog.ErrorContext(
				ctx,
				"OpenFlow group output port unavailable",
				"port",
				bucket.Output,
				"err",
				errUnknownFlowPort,
			)

			return nil, fmt.Errorf("%w: %q", errUnknownFlowPort, bucket.Output)
		}

		output := openflow15.NewActionOutput(port)
		output.MaxLen = 0
		actions = append(actions, output)
	}

	for _, action := range actions {
		if int(encoded.Len())+int(action.Len()) > maxFlowSize {
			return nil, errFlowSize
		}

		encoded.AddAction(action)
	}

	return encoded, nil
}

func rawProperties(values [][]byte) ([]util.Message, error) {
	result := make([]util.Message, 0, len(values))
	total := 0

	for _, value := range values {
		if len(value) < wireAlignment || len(value)%wireAlignment != 0 {
			return nil, errRawProperty
		}

		length := int(binary.BigEndian.Uint16(value[2:4]))
		if length < 4 || (length+wireAlignment-1)/wireAlignment*wireAlignment != len(value) {
			return nil, errRawProperty
		}

		total += len(value)
		if total > maxFlowSize-flowHeaderSize {
			return nil, errFlowSize
		}

		result = append(result, util.NewBuffer(value))
	}

	return result, nil
}

type wireAction struct {
	*util.Buffer

	header openflow15.ActionHeader
}

func (action wireAction) Header() *openflow15.ActionHeader {
	return &action.header
}
