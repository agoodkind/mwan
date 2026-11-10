//go:build linux

package netif

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	packetNexthopHeaderBytes              = 8
	packetNexthopMemberBytes              = 8
	packetNexthopGroupMPath               = 0
	packetNexthopGroupResilient           = 1
	packetNexthopResilientAttributes      = 12
	packetNexthopResilientBuckets         = 1
	packetNexthopResilientIdleTimer       = 2
	packetNexthopResilientUnbalancedTimer = 3
	packetNexthopResilientUnbalancedTime  = 4
)

type packetNexthop struct {
	id           uint32
	flags        uint32
	blackhole    bool
	encapsulated bool
	output       int
	gateway      net.IP
	group        []uint32
	groupType    uint16
}

func queryPacketNexthop(ctx context.Context, id uint32) (packetNexthop, error) {
	var nexthop packetNexthop
	var query packetQuery
	query.operation, query.objectID = packetQueryNexthop, id
	query.request.Header.Type, query.response = unix.RTM_GETNEXTHOP, unix.RTM_NEWNEXTHOP
	query.request.Header.Flags = netlink.Request | netlink.Acknowledge
	if id == 0 {
		return nexthop, query.failure("encode request", errors.New("nexthop ID must be positive"))
	}
	var header unix.Nhmsg
	data, err := binary.Append(nil, binary.NativeEndian, header)
	if err != nil {
		return nexthop, query.failure("encode header", err)
	}
	attributes := netlink.NewAttributeEncoder()
	attributes.ByteOrder = binary.NativeEndian
	attributes.Uint32(unix.NHA_ID, id)
	encoded, err := attributes.Encode()
	if err != nil {
		return nexthop, query.failure("encode ID", err)
	}
	data = append(data, encoded...)
	query.request.Data = data
	data, err = query.execute(ctx)
	if err != nil {
		return nexthop, err
	}
	if err := nexthop.UnmarshalBinary(data); err != nil {
		return nexthop, query.failure("decode object", err)
	}
	if nexthop.id != id {
		return nexthop, query.failure("validate ID", fmt.Errorf("requested nexthop %d but kernel returned %d", id, nexthop.id))
	}
	return nexthop, nil
}

func (nexthop *packetNexthop) UnmarshalBinary(data []byte) error {
	var empty packetNexthop
	*nexthop = empty
	if len(data) < packetNexthopHeaderBytes {
		return errors.New("kernel nexthop omitted its header")
	}
	var header unix.Nhmsg
	if _, err := binary.Decode(data[:packetNexthopHeaderBytes], binary.NativeEndian, &header); err != nil {
		return fmt.Errorf("decode kernel nexthop header: %w", err)
	}
	nexthop.flags = header.Flags
	attributes, err := packetQueryAttributes(data[packetNexthopHeaderBytes:])
	if err != nil {
		return err
	}
	for _, attribute := range attributes {
		if err := nexthop.decodeAttribute(int(header.Family), attribute); err != nil {
			return fmt.Errorf("nexthop attribute %d: %w", attribute.Type, err)
		}
	}
	if nexthop.id == 0 {
		return errors.New("kernel nexthop omitted its identity")
	}
	return nil
}

func (nexthop *packetNexthop) decodeAttribute(family int, attribute netlink.Attribute) error {
	switch attribute.Type {
	case unix.NHA_ID, unix.NHA_OIF:
		return nexthop.decodeScalar(attribute)
	case unix.NHA_GATEWAY:
		address, err := packetQueryAddress(family, attribute.Data)
		if err != nil {
			return err
		}
		nexthop.gateway = address
	case unix.NHA_BLACKHOLE:
		if len(attribute.Data) != 0 {
			return errors.New("kernel blackhole attribute must be empty")
		}
		nexthop.blackhole = true
	case unix.NHA_GROUP:
		return nexthop.decodeGroup(attribute.Data)
	case unix.NHA_GROUP_TYPE, unix.NHA_ENCAP_TYPE:
		if len(attribute.Data) != 2 {
			return errors.New("kernel nexthop type requires a two-byte value")
		}
		value := binary.NativeEndian.Uint16(attribute.Data)
		if attribute.Type == unix.NHA_GROUP_TYPE {
			nexthop.groupType = value
		} else {
			nexthop.encapsulated = nexthop.encapsulated || value != 0
		}
	case unix.NHA_ENCAP:
		nexthop.encapsulated = true
	case packetNexthopResilientAttributes:
		return validatePacketResilientGroup(attribute.Data)
	}
	return nil
}

func (nexthop *packetNexthop) decodeScalar(attribute netlink.Attribute) error {
	value, err := packetQueryUint32(attribute.Data)
	if err != nil {
		return err
	}
	if attribute.Type == unix.NHA_ID {
		nexthop.id = value
		return nil
	}
	if value > math.MaxInt32 {
		return errors.New("kernel nexthop output exceeds the interface index range")
	}
	nexthop.output = int(value)
	return nil
}

func (nexthop *packetNexthop) decodeGroup(data []byte) error {
	if len(data) == 0 || len(data)%packetNexthopMemberBytes != 0 {
		return errors.New("kernel nexthop group has an invalid member length")
	}
	seen := make(map[uint32]struct{})
	for len(data) != 0 {
		id := binary.NativeEndian.Uint32(data)
		if id == 0 {
			return errors.New("kernel nexthop group contains a zero member ID")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("kernel nexthop group repeats member %d", id)
		}
		seen[id] = struct{}{}
		nexthop.group = append(nexthop.group, id)
		data = data[packetNexthopMemberBytes:]
	}
	return nil
}

func validatePacketResilientGroup(data []byte) error {
	attributes, err := packetQueryAttributes(data)
	if err != nil {
		return err
	}
	for _, attribute := range attributes {
		size := len(attribute.Data)
		switch attribute.Type {
		case packetNexthopResilientBuckets:
			size = 2
		case packetNexthopResilientIdleTimer, packetNexthopResilientUnbalancedTimer:
			size = 4
		case packetNexthopResilientUnbalancedTime:
			size = 8
		}
		if len(attribute.Data) != size {
			return fmt.Errorf("kernel resilient nexthop attribute %d requires %d bytes", attribute.Type, size)
		}
	}
	return nil
}
