//go:build linux

package offload

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"syscall"

	"github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

type forwardingFieldFormat uint8

const (
	forwardingRouteNextHopID    = 30
	forwardingNeighborTableName = 1
	forwardingMetadataEntries   = 32
)

const (
	forwardingUint32 forwardingFieldFormat = iota
	forwardingAddress
	forwardingMAC
	forwardingName
)

type forwardingField struct {
	kind   uint16
	name   string
	format forwardingFieldFormat
}

func logForwardingMutations(ctx context.Context, log *slog.Logger, generation uint64, messages []forwardingMutation) {
	if !log.Enabled(ctx, slog.LevelInfo) {
		return
	}
	mutations := make([]slog.Attr, 0, min(len(messages), forwardingMetadataEntries))
	omitted := 0
	invalidating, suppressed := 0, 0
	for _, mutation := range messages {
		message := mutation.message
		changed, err := unmarshalForwardingMutation(message)
		if err != nil || !changed {
			continue
		}
		if mutation.invalidates {
			invalidating++
		} else {
			suppressed++
		}
		if len(mutations) == forwardingMetadataEntries {
			omitted++
			continue
		}
		mutations = append(mutations, slog.Attr{
			Key: strconv.Itoa(len(mutations)),
			Value: slog.GroupValue(
				slog.Int("message_type", int(message.Header.Type)),
				slog.Uint64("sequence", uint64(message.Header.Seq)),
				slog.Bool("invalidates", mutation.invalidates),
				slog.Attr{Key: "object", Value: forwardingIdentity(message)},
			),
		})
	}
	log.LogAttrs(ctx, slog.LevelInfo, "kernel forwarding notifications processed",
		slog.Uint64("generation", generation), slog.Int("mutation_count", len(mutations)+omitted),
		slog.Int("invalidating_mutations", invalidating), slog.Int("suppressed_mutations", suppressed),
		slog.Int("omitted_mutations", omitted), slog.Attr{Key: "mutations", Value: slog.GroupValue(mutations...)})
}

func forwardingIdentity(message syscall.NetlinkMessage) slog.Value {
	data := message.Data
	fields, offset, attributes := forwardingIdentityHeader(message.Header.Type, data)
	decoder, err := netlink.NewAttributeDecoder(data[offset:])
	if err != nil {
		return slog.GroupValue(append(fields, slog.String("identity_error", err.Error()))...)
	}
	seen := make(map[uint16]bool, len(attributes))
	for decoder.Next() {
		for _, field := range attributes {
			if field.kind != decoder.Type() || seen[field.kind] {
				continue
			}
			seen[field.kind] = true
			fields = append(fields, forwardingAttribute(field, decoder.Bytes()))
			break
		}
	}
	if err := decoder.Err(); err != nil {
		fields = append(fields, slog.String("identity_error", err.Error()))
	}
	return slog.GroupValue(fields...)
}

// The receiver validates every header and attribute boundary before logging.
// Metadata decoding must not change the notification's invalidation decision.
func forwardingIdentityHeader(kind uint16, data []byte) ([]slog.Attr, int, []forwardingField) {
	fields := []slog.Attr{slog.Int("family", int(data[0]))}
	switch kind {
	case unix.RTM_NEWLINK, unix.RTM_DELLINK, unix.RTM_SETLINK, unix.RTM_NEWLINKPROP, unix.RTM_DELLINKPROP:
		fields = append(fields, slog.Uint64("interface_index", uint64(binary.NativeEndian.Uint32(data[4:8]))),
			slog.Uint64("flags", uint64(binary.NativeEndian.Uint32(data[8:12]))),
			slog.Uint64("change", uint64(binary.NativeEndian.Uint32(data[12:16]))))
		return fields, unix.SizeofIfInfomsg, []forwardingField{
			{unix.IFLA_IFNAME, "interface_name", forwardingName},
			{unix.IFLA_ADDRESS, "link_address", forwardingMAC},
			{unix.IFLA_MASTER, "master_index", forwardingUint32},
			{unix.IFLA_MTU, "mtu", forwardingUint32},
		}
	case unix.RTM_NEWADDR, unix.RTM_DELADDR:
		fields = append(fields, slog.Uint64("interface_index", uint64(binary.NativeEndian.Uint32(data[4:8]))),
			slog.Int("prefix_length", int(data[1])), slog.Int("scope", int(data[3])))
		return fields, unix.SizeofIfAddrmsg, []forwardingField{
			{unix.IFA_ADDRESS, "address", forwardingAddress},
			{unix.IFA_LOCAL, "local", forwardingAddress},
			{unix.IFA_LABEL, "label", forwardingName},
		}
	case unix.RTM_NEWROUTE, unix.RTM_DELROUTE:
		fields = append(fields, slog.Int("destination_prefix_length", int(data[1])),
			slog.Int("source_prefix_length", int(data[2])), slog.Int("table", int(data[4])),
			slog.Int("protocol", int(data[5])), slog.Int("scope", int(data[6])), slog.Int("route_type", int(data[7])))
		return fields, unix.SizeofRtMsg, []forwardingField{
			{unix.RTA_DST, "destination", forwardingAddress},
			{unix.RTA_SRC, "source", forwardingAddress},
			{unix.RTA_GATEWAY, "gateway", forwardingAddress},
			{unix.RTA_OIF, "output_index", forwardingUint32},
			{unix.RTA_IIF, "input_index", forwardingUint32},
			{unix.RTA_TABLE, "extended_table", forwardingUint32},
			{unix.RTA_PRIORITY, "priority", forwardingUint32},
			{forwardingRouteNextHopID, "next_hop_id", forwardingUint32},
		}
	case unix.RTM_NEWRULE, unix.RTM_DELRULE:
		fields = append(fields, slog.Int("destination_prefix_length", int(data[1])),
			slog.Int("source_prefix_length", int(data[2])), slog.Int("tos", int(data[3])),
			slog.Int("table", int(data[4])), slog.Int("action", int(data[7])))
		return fields, unix.SizeofRtMsg, []forwardingField{
			{unix.FRA_DST, "destination", forwardingAddress},
			{unix.FRA_SRC, "source", forwardingAddress},
			{unix.FRA_IIFNAME, "input_name", forwardingName},
			{unix.FRA_OIFNAME, "output_name", forwardingName},
			{unix.FRA_PRIORITY, "priority", forwardingUint32},
			{unix.FRA_FWMARK, "mark", forwardingUint32},
			{unix.FRA_FWMASK, "mark_mask", forwardingUint32},
			{unix.FRA_TABLE, "extended_table", forwardingUint32},
		}
	case unix.RTM_NEWNEIGH, unix.RTM_DELNEIGH:
		fields = append(fields, slog.Uint64("interface_index", uint64(binary.NativeEndian.Uint32(data[4:8]))),
			slog.Int("state", int(binary.NativeEndian.Uint16(data[8:10]))), slog.Int("flags", int(data[10])))
		return fields, forwardingNeighborHeaderBytes, []forwardingField{
			{unix.NDA_DST, "destination", forwardingAddress}, {unix.NDA_LLADDR, "link_address", forwardingMAC},
		}
	case unix.RTM_NEWNEIGHTBL, unix.RTM_SETNEIGHTBL:
		return fields, forwardingNeighborTableHeaderBytes, []forwardingField{{forwardingNeighborTableName, "table_name", forwardingName}}
	case unix.RTM_NEWNEXTHOP, unix.RTM_DELNEXTHOP, unix.RTM_NEWNEXTHOPBUCKET, unix.RTM_DELNEXTHOPBUCKET:
		fields = append(fields, slog.Int("scope", int(data[1])), slog.Int("protocol", int(data[2])),
			slog.Uint64("flags", uint64(binary.NativeEndian.Uint32(data[4:8]))))
		return fields, forwardingNextHopHeaderBytes, []forwardingField{
			{unix.NHA_ID, "next_hop_id", forwardingUint32},
			{unix.NHA_OIF, "output_index", forwardingUint32},
			{unix.NHA_GATEWAY, "gateway", forwardingAddress},
		}
	case unix.RTM_NEWQDISC, unix.RTM_DELQDISC, unix.RTM_NEWTCLASS, unix.RTM_DELTCLASS,
		unix.RTM_NEWTFILTER, unix.RTM_DELTFILTER, unix.RTM_NEWCHAIN, unix.RTM_DELCHAIN:
		fields = append(fields, slog.Uint64("interface_index", uint64(binary.NativeEndian.Uint32(data[4:8]))),
			slog.Uint64("handle", uint64(binary.NativeEndian.Uint32(data[8:12]))),
			slog.Uint64("parent", uint64(binary.NativeEndian.Uint32(data[12:16]))),
			slog.Uint64("info", uint64(binary.NativeEndian.Uint32(data[16:20]))))
		return fields, nl.SizeofTcMsg, []forwardingField{{nl.TCA_KIND, "kind", forwardingName}}
	case unix.RTM_NEWACTION, unix.RTM_DELACTION:
		return fields, nl.SizeofTcActionMsg, nil
	default:
		return fields, len(data), nil
	}
}

func forwardingAttribute(field forwardingField, data []byte) slog.Attr {
	switch field.format {
	case forwardingUint32:
		if len(data) == 4 {
			return slog.Uint64(field.name, uint64(binary.NativeEndian.Uint32(data)))
		}
	case forwardingAddress:
		if address, ok := netip.AddrFromSlice(data); ok {
			return slog.String(field.name, address.String())
		}
	case forwardingMAC:
		if len(data) == 6 {
			return slog.String(field.name, net.HardwareAddr(data).String())
		}
	case forwardingName:
		if len(data) <= unix.IFNAMSIZ {
			return slog.String(field.name, string(bytes.TrimRight(data, "\x00")))
		}
	}
	return slog.Group(field.name, "invalid_length", len(data))
}
