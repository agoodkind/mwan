//go:build linux

package bpf

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

type nativeTC struct {
	header  nl.TcMsg
	kind    string
	options []byte
}

func readNativeTC(ctx context.Context, index int, kind uint16, parent uint32) (records []nativeTC, resultErr error) {
	if _, err := interfaceKey(index); err != nil {
		return nil, err
	}
	deadline, present := ctx.Deadline()
	if !present {
		return nil, fmt.Errorf("traffic-control inspection requires a deadline")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	socket, err := mdnetlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, fmt.Errorf("open traffic-control inspection: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, socket.Close()) }()
	if err := socket.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set traffic-control inspection deadline: %w", err)
	}
	header := nl.TcMsg{Ifindex: int32(index), Parent: parent}
	messages, err := socket.Execute(mdnetlink.Message{
		Header: mdnetlink.Header{Type: mdnetlink.HeaderType(kind), Flags: mdnetlink.Request | mdnetlink.Dump},
		Data:   header.Serialize(),
	})
	if err != nil {
		return nil, fmt.Errorf("read traffic-control objects: %w", err)
	}
	for _, message := range messages {
		if message.Header.Type != mdnetlink.HeaderType(kind-2) || len(message.Data) < nl.SizeofTcMsg {
			return nil, fmt.Errorf("traffic-control dump returned an invalid record")
		}
		actual := *nl.DeserializeTcMsg(message.Data)
		if actual.Ifindex != int32(index) {
			continue
		}
		record, err := decodeNativeTC(actual, message.Data[nl.SizeofTcMsg:])
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func decodeNativeTC(header nl.TcMsg, data []byte) (nativeTC, error) {
	record := nativeTC{header: header, kind: "", options: nil}
	attributes, err := mdnetlink.NewAttributeDecoder(data)
	if err != nil {
		return record, err
	}
	seen := make(map[uint16]bool)
	for attributes.Next() {
		kind := attributes.Type()
		if seen[kind] && kind != nl.TCA_PAD {
			return record, fmt.Errorf("duplicate traffic-control attribute %d", kind)
		}
		seen[kind] = true
		switch kind {
		case nl.TCA_KIND:
			value := attributes.Bytes()
			if len(value) < 2 || bytes.IndexByte(value, 0) != len(value)-1 {
				return record, fmt.Errorf("traffic-control kind is not a terminated string")
			}
			record.kind = string(value[:len(value)-1])
		case nl.TCA_OPTIONS:
			record.options = attributes.Bytes()
		case nl.TCA_STATS, nl.TCA_XSTATS, nl.TCA_STATS2, nl.TCA_PAD:
			// These counters and alignment bytes do not select packet behavior.
		case nl.TCA_HW_OFFLOAD:
			if attributes.Uint8() != 0 {
				return record, fmt.Errorf("traffic-control object already uses hardware offload")
			}
		case nl.TCA_CHAIN, nl.TCA_INGRESS_BLOCK, nl.TCA_EGRESS_BLOCK:
			if attributes.Uint32() != 0 {
				return record, fmt.Errorf("traffic-control object uses a chain or shared block")
			}
		default:
			return record, fmt.Errorf("unsupported traffic-control attribute %d", kind)
		}
	}
	if err := attributes.Err(); err != nil {
		return record, fmt.Errorf("decode traffic-control attributes: %w", err)
	}
	if record.kind == "" || header.Family != unix.AF_UNSPEC || header.Pad != [3]byte{} {
		return record, fmt.Errorf("traffic-control record lacks a supported header or kind")
	}
	return record, nil
}
