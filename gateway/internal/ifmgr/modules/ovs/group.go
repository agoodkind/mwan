package ovs

// OpenFlow group compilation reserves a range of group IDs for mwan and
// encodes bucket actions with the bridge's port numbers. The installer
// uses the resulting messages to add or update groups.
//
//   configured group 7 -> OpenFlow group 0x6d770007

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
	errEncodedAction  = errors.New("encoded action must contain one complete OpenFlow action")
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
		encoded, err := compileBucket(bucket, numbers)
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

	actions, err := marshalActionFields(bucketExpression, bucket.Actions, numbers)
	if err != nil {
		return nil, err
	}

	for _, raw := range actions {
		if len(raw) < instructionSize || len(raw)%wireAlignment != 0 ||
			int(binary.BigEndian.Uint16(raw[2:4])) != len(raw) {
			return nil, errEncodedAction
		}

		if int(encoded.Len())+len(raw) > maxFlowSize {
			return nil, errFlowSize
		}

		action := wireAction{
			Buffer: util.NewBuffer(raw),
			header: openflow15.ActionHeader{
				Type:   binary.BigEndian.Uint16(raw),
				Length: binary.BigEndian.Uint16(raw[2:4]),
			},
		}
		encoded.AddAction(action)
	}

	return encoded, nil
}

type wireAction struct {
	*util.Buffer

	header openflow15.ActionHeader
}

func (action wireAction) Header() *openflow15.ActionHeader {
	return &action.header
}
