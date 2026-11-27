package ovs

// The OpenFlow compiler resolves port names and encodes group properties.
// Cases check that a select group sends weights 3 and 1 with port numbers
// 5 and 9, and that an all group can compile without a selection policy. These
// checks inspect the encoded message.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"antrea.io/libOpenflow/openflow15"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestGroupRejectsInvalidBuckets(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ovsconfig.Group)
		want   error
	}{
		{"invalid ID", func(group *ovsconfig.Group) { group.ID = 0 }, errGroupID},
		{
			"missing port",
			func(group *ovsconfig.Group) { group.Buckets[0].Actions[0].Argument = "absent" },
			errExpression,
		},
		{"missing type", func(group *ovsconfig.Group) { group.Type = "" }, errGroupType},
		{
			"reserved bucket ID",
			func(group *ovsconfig.Group) { group.Buckets[0].ID = maxBucketID + 1 },
			errBucketID,
		},
	}
	for _, test := range cases {
		t.Run(
			test.name,
			func(t *testing.T) {
				group := testWeightedGroup()
				test.change(&group)
				_, err := compileGroups(
					t.Context(),
					[]ovsconfig.Group{group},
					map[string]uint32{"wan0": 1, "wan1": 2},
				)
				if !errors.Is(err, test.want) {
					t.Fatalf("compileGroups error = %v, want %v", err, test.want)
				}
			},
		)
	}
}

func TestGroupSerializesWeightedOutputs(t *testing.T) {
	groups, err := compileGroups(
		t.Context(),
		[]ovsconfig.Group{testWeightedGroup()},
		map[string]uint32{"wan0": 5, "wan1": 9},
	)
	if err != nil {
		t.Fatal(err)
	}

	data, err := groups[0].MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	const groupHeaderSize = 24
	if data[10] != openflow15.GT_SELECT ||
		binary.BigEndian.Uint32(data[12:16]) != groupNamespace|7 {
		t.Fatalf("unexpected group header: %x", data[:groupHeaderSize])
	}

	offset := groupHeaderSize

	for index, weight := range []uint16{3, 1} {
		bucket := openflow15.NewBucket(0)
		if err := bucket.UnmarshalBinary(data[offset:]); err != nil {
			t.Fatal(err)
		}

		property, ok := bucket.Properties[0].(*openflow15.GroupBucketPropWeight)
		if !ok || property.Weight != weight {
			t.Fatalf("bucket %d has incorrect weight: %+v", index, property)
		}

		output, ok := bucket.Actions[0].(*openflow15.ActionOutput)
		if !ok || output.Port != []uint32{5, 9}[index] {
			t.Fatalf("bucket %d has incorrect output: %+v", index, output)
		}

		offset += int(bucket.Length)
	}

	method := openflow15.NewNTRSelectionMethod(openflow15.NTR_HASH, 0)
	if err := method.UnmarshalBinary(data[offset:]); err != nil {
		t.Fatal(err)
	}

	if string(bytes.TrimRight(method.SelectionMethod[:], "\x00")) != string(
		openflow15.NTR_DP_HASH,
	) {
		t.Fatalf("selection method = %q", method.SelectionMethod)
	}
}

func testWeightedGroup() ovsconfig.Group {
	first, second := uint16(3), uint16(1)

	return ovsconfig.Group{
		ID:                 7,
		Type:               "select",
		SelectionMethod:    "dp_hash",
		SelectionParameter: 0,
		Buckets: []ovsconfig.Bucket{
			{
				ID:      1,
				Weight:  &first,
				Actions: []ovsconfig.Action{{Name: "output", Argument: "wan0"}},
			},
			{
				ID:      2,
				Weight:  &second,
				Actions: []ovsconfig.Action{{Name: "output", Argument: "wan1"}},
			},
		},
	}
}

func TestGroupDoesNotAddSelectionPolicy(t *testing.T) {
	group := testWeightedGroup()
	group.Type = "all"

	group.SelectionMethod = ""
	for index := range group.Buckets {
		group.Buckets[index].Weight = nil
	}

	compiled, err := compileGroup(t.Context(), group, map[string]uint32{"wan0": 5, "wan1": 9})
	if err != nil {
		t.Fatal(err)
	}

	if compiled.Type != openflow15.GT_ALL || len(compiled.Properties) != 0 {
		t.Fatalf("compiler changed configured group policy: %+v", compiled)
	}

	for _, bucket := range compiled.Buckets {
		if len(bucket.Properties) != 0 {
			t.Fatalf("compiler added bucket properties: %+v", bucket)
		}
	}
}
