//go:build linux

package netif

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	packetRouteNexthopID    = 30
	packetRouteFlowLabel    = 31
	packetAttributeTypeMask = 0x3fff
)

type packetRouteObservation struct {
	route            netlink.Route
	nexthopID        uint32
	encapsulated     bool
	hopEncapsulation []bool
}

type packetRouteRequest struct {
	selector PacketSelector
	fibMatch bool
	uid      packetRouteUID
}

func queryPacketRoute(ctx context.Context, packet PacketSelector, fibMatch bool, uid packetRouteUID) (packetRouteObservation, error) {
	var observation packetRouteObservation
	var query packetQuery
	query.operation = packetQueryRoute
	if fibMatch {
		query.operation = packetQueryFIB
	}
	query.request.Header.Type = unix.RTM_GETROUTE
	query.request.Header.Flags = mdnetlink.Request | mdnetlink.Acknowledge
	query.response = unix.RTM_NEWROUTE
	request := packetRouteRequest{selector: packet, fibMatch: fibMatch, uid: uid}
	var err error
	query.request.Data, err = request.MarshalBinary()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return observation, query.failure("encode request", err)
	}
	data, err := query.execute(ctx)
	if err != nil {
		return observation, err
	}
	if err := observation.UnmarshalBinary(data); err != nil {
		return observation, query.failure("decode route", err)
	}
	if observation.route.Family != packet.family() {
		return observation, query.failure("validate route family", fmt.Errorf("kernel route family %d does not match packet family %d", observation.route.Family, packet.family()))
	}
	return observation, nil
}

func (request packetRouteRequest) MarshalBinary() ([]byte, error) {
	packet, fibMatch, uid := request.selector, request.fibMatch, request.uid
	if packet.InputInterface <= 0 || packet.InputInterface > math.MaxInt32 {
		return nil, errors.New("packet query requires a positive kernel interface index")
	}
	switch uid.mode {
	case packetRouteUIDAbsent:
		if packet.Source.Addr().Is6() {
			return nil, errors.New("IPv6 route query requires packet UID evidence")
		}
	case packetRouteUIDProjected, packetRouteUIDIndependent:
		if !packet.Source.Addr().Is6() {
			return nil, errors.New("packet UID evidence requires an IPv6 query")
		}
	default:
		return nil, errors.New("unknown packet UID evidence mode")
	}
	var header unix.RtMsg
	header.Family, header.Dst_len, header.Src_len = unix.AF_INET, net.IPv4len*8, net.IPv4len*8
	if packet.Source.Addr().Is6() {
		header.Family, header.Dst_len, header.Src_len = unix.AF_INET6, net.IPv6len*8, net.IPv6len*8
	}
	header.Tos = packet.TrafficClass
	header.Flags = unix.RTM_F_LOOKUP_TABLE
	if fibMatch {
		header.Flags |= unix.RTM_F_FIB_MATCH
	}
	attributes := mdnetlink.NewAttributeEncoder()
	attributes.ByteOrder = binary.NativeEndian
	attributes.Bytes(unix.RTA_DST, packet.Destination.Addr().AsSlice())
	attributes.Bytes(unix.RTA_SRC, packet.Source.Addr().AsSlice())
	attributes.Uint32(unix.RTA_IIF, uint32(packet.InputInterface))
	if packet.Mark != 0 {
		attributes.Uint32(unix.RTA_MARK, packet.Mark)
	}
	attributes.Uint8(unix.RTA_IP_PROTO, packet.Protocol)
	if uid.mode == packetRouteUIDProjected {
		attributes.Uint32(unix.RTA_UID, uid.value)
	}
	attributes.ByteOrder = binary.BigEndian
	attributes.Uint16(unix.RTA_SPORT, packet.Source.Port())
	attributes.Uint16(unix.RTA_DPORT, packet.Destination.Port())
	if packet.Source.Addr().Is6() {
		attributes.Uint32(packetRouteFlowLabel, packet.FlowLabel)
	}
	encoded, err := attributes.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode packet route attributes: %w", err)
	}
	data, err := binary.Append(nil, binary.NativeEndian, header)
	if err != nil {
		return nil, fmt.Errorf("encode packet route header: %w", err)
	}
	data = append(data, encoded...)
	return data, nil
}

func packetQueryAttributes(data []byte) ([]mdnetlink.Attribute, error) {
	var attributes []mdnetlink.Attribute
	seen := make(map[uint16]struct{})
	for len(data) != 0 {
		if len(data) < unix.SizeofRtAttr {
			return nil, errors.New("kernel attribute omitted its header")
		}
		length := int(binary.NativeEndian.Uint16(data))
		if length < unix.SizeofRtAttr || length > len(data) {
			return nil, errors.New("kernel attribute has an invalid length")
		}
		aligned := (length + packetQueryAlignment - 1) &^ (packetQueryAlignment - 1)
		if aligned > len(data) {
			return nil, errors.New("kernel attribute omitted alignment bytes")
		}
		kind := binary.NativeEndian.Uint16(data[2:]) & packetAttributeTypeMask
		if kind != unix.RTA_PAD {
			if _, duplicate := seen[kind]; duplicate {
				return nil, fmt.Errorf("kernel repeated attribute %d", kind)
			}
			seen[kind] = struct{}{}
			attributes = append(attributes, mdnetlink.Attribute{Type: kind, Data: data[unix.SizeofRtAttr:length]})
		}
		data = data[aligned:]
	}
	return attributes, nil
}

func packetQueryUint32(data []byte) (uint32, error) {
	if len(data) != packetQueryAlignment {
		return 0, errors.New("kernel attribute requires a four-byte value")
	}
	return binary.NativeEndian.Uint32(data), nil
}

func packetQueryAddress(family int, data []byte) (net.IP, error) {
	if (family == unix.AF_INET && len(data) == net.IPv4len) || (family == unix.AF_INET6 && len(data) == net.IPv6len) {
		return append(net.IP(nil), data...), nil
	}
	return nil, fmt.Errorf("kernel address length %d does not match family %d", len(data), family)
}

func (observation *packetRouteObservation) UnmarshalBinary(data []byte) error {
	var empty packetRouteObservation
	*observation = empty
	if len(data) < unix.SizeofRtMsg {
		return errors.New("kernel route omitted its header")
	}
	var header unix.RtMsg
	if _, err := binary.Decode(data[:unix.SizeofRtMsg], binary.NativeEndian, &header); err != nil {
		return fmt.Errorf("decode kernel route header: %w", err)
	}
	family := int(header.Family)
	if family != unix.AF_INET && family != unix.AF_INET6 {
		return errors.New("kernel route returned an unsupported address family")
	}
	bits := net.IPv4len * 8
	if family == unix.AF_INET6 {
		bits = net.IPv6len * 8
	}
	if int(header.Dst_len) > bits || int(header.Src_len) > bits {
		return errors.New("kernel route returned an invalid prefix")
	}
	observation.route.Family, observation.route.Table = family, int(header.Table)
	observation.route.Scope, observation.route.Protocol = netlink.Scope(header.Scope), netlink.RouteProtocol(header.Protocol)
	observation.route.Type, observation.route.Tos = int(header.Type), int(header.Tos)
	if uint64(header.Flags) > uint64(math.MaxInt) {
		return errors.New("kernel route flags exceed their supported range")
	}
	observation.route.Flags = int(header.Flags)
	attributes, err := packetQueryAttributes(data[unix.SizeofRtMsg:])
	if err != nil {
		return err
	}
	for _, attribute := range attributes {
		if err := observation.decodeAttribute(header, attribute); err != nil {
			return fmt.Errorf("route attribute %d: %w", attribute.Type, err)
		}
	}
	return nil
}

func (observation *packetRouteObservation) decodeAttribute(header unix.RtMsg, attribute mdnetlink.Attribute) error {
	switch attribute.Type {
	case unix.RTA_DST, unix.RTA_PREFSRC, unix.RTA_GATEWAY:
		address, err := packetQueryAddress(observation.route.Family, attribute.Data)
		if err != nil {
			return err
		}
		switch attribute.Type {
		case unix.RTA_DST:
			observation.route.Dst = &net.IPNet{IP: address, Mask: net.CIDRMask(int(header.Dst_len), len(address)*8)}
		case unix.RTA_PREFSRC:
			observation.route.Src = address
		case unix.RTA_GATEWAY:
			observation.route.Gw = address
		}
	case unix.RTA_OIF, unix.RTA_IIF, unix.RTA_TABLE, unix.RTA_PRIORITY, packetRouteNexthopID:
		return observation.decodeScalar(attribute)
	case unix.RTA_METRICS:
		return observation.decodeMetrics(attribute.Data)
	case unix.RTA_MULTIPATH:
		return observation.UnmarshalMultipath(attribute.Data)
	case unix.RTA_ENCAP, unix.RTA_ENCAP_TYPE, unix.RTA_VIA, unix.RTA_NEWDST:
		observation.encapsulated = true
	}
	return nil
}

func (observation *packetRouteObservation) decodeScalar(attribute mdnetlink.Attribute) error {
	value, err := packetQueryUint32(attribute.Data)
	if err != nil {
		return err
	}
	if attribute.Type == packetRouteNexthopID {
		if value == 0 {
			return errors.New("kernel route returned a zero nexthop ID")
		}
		observation.nexthopID = value
		return nil
	}
	if uint64(value) > uint64(math.MaxInt) || ((attribute.Type == unix.RTA_OIF || attribute.Type == unix.RTA_IIF) && value > math.MaxInt32) {
		return errors.New("kernel route integer exceeds its supported range")
	}
	switch attribute.Type {
	case unix.RTA_OIF:
		observation.route.LinkIndex = int(value)
	case unix.RTA_IIF:
		observation.route.ILinkIndex = int(value)
	case unix.RTA_TABLE:
		observation.route.Table = int(value)
	case unix.RTA_PRIORITY:
		observation.route.Priority = int(value)
	}
	return nil
}

func (observation *packetRouteObservation) decodeMetrics(data []byte) error {
	attributes, err := packetQueryAttributes(data)
	if err != nil {
		return err
	}
	for _, attribute := range attributes {
		if attribute.Type != unix.RTAX_MTU {
			continue
		}
		mtu, err := packetQueryUint32(attribute.Data)
		if err != nil {
			return err
		}
		if uint64(mtu) > uint64(math.MaxInt) {
			return errors.New("kernel MTU exceeds its supported range")
		}
		observation.route.MTU = int(mtu)
	}
	return nil
}

func (observation *packetRouteObservation) UnmarshalMultipath(data []byte) error {
	if len(data) == 0 {
		return errors.New("kernel route returned an empty multipath list")
	}
	for len(data) != 0 {
		if len(data) < unix.SizeofRtNexthop {
			return errors.New("kernel multipath entry omitted its header")
		}
		var header unix.RtNexthop
		if _, err := binary.Decode(data[:unix.SizeofRtNexthop], binary.NativeEndian, &header); err != nil {
			return fmt.Errorf("decode multipath nexthop header: %w", err)
		}
		length := int(header.Len)
		aligned := (length + packetQueryAlignment - 1) &^ (packetQueryAlignment - 1)
		if length < unix.SizeofRtNexthop || aligned > len(data) || header.Ifindex < 0 {
			return errors.New("kernel multipath entry has an invalid length or interface")
		}
		hop, encapsulated, err := decodePacketRouteHop(observation.route.Family, header, data[unix.SizeofRtNexthop:length])
		if err != nil {
			return err
		}
		observation.route.MultiPath = append(observation.route.MultiPath, hop)
		observation.hopEncapsulation = append(observation.hopEncapsulation, encapsulated)
		data = data[aligned:]
	}
	return nil
}

func decodePacketRouteHop(family int, header unix.RtNexthop, data []byte) (*netlink.NexthopInfo, bool, error) {
	var hop netlink.NexthopInfo
	hop.LinkIndex, hop.Hops, hop.Flags = int(header.Ifindex), int(header.Hops), int(header.Flags)
	attributes, err := packetQueryAttributes(data)
	if err != nil {
		return nil, false, err
	}
	encapsulated := false
	for _, attribute := range attributes {
		switch attribute.Type {
		case unix.RTA_GATEWAY:
			hop.Gw, err = packetQueryAddress(family, attribute.Data)
			if err != nil {
				return nil, false, err
			}
		case unix.RTA_ENCAP, unix.RTA_ENCAP_TYPE, unix.RTA_VIA, unix.RTA_NEWDST:
			encapsulated = true
		}
	}
	return &hop, encapsulated, nil
}
