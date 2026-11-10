//go:build linux

package bpf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

type tcKind string

const (
	tcUnknown   tcKind = ""
	tcNoQueue   tcKind = "noqueue"
	tcMQ        tcKind = "mq"
	tcClsact    tcKind = "clsact"
	tcFQCodel   tcKind = "fq_codel"
	tcPFIFOFast tcKind = "pfifo_fast"
	tcBPF       tcKind = "bpf"
)

type nativeTC struct {
	header  nl.TcMsg
	kind    tcKind
	options []byte
}

// ReadNativeTC requires a complete bounded kernel dump and rejects malformed policy records.
func ReadNativeTC(ctx context.Context, index int, kind uint16, parent uint32) (records []nativeTC, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "native traffic-control read failed", "interface", index, "request_type", kind, "parent", parent, "err", resultErr)
		}
	}()
	if index < 1 || index > math.MaxInt32 {
		return nil, fmt.Errorf("traffic-control interface index %d exceeds the kernel range", index)
	}
	ifindex := int32(index)
	deadline, present := ctx.Deadline()
	if !present {
		return nil, fmt.Errorf("traffic-control inspection requires a deadline")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start traffic-control inspection: %w", err)
	}
	socket, err := mdnetlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, fmt.Errorf("open traffic-control inspection: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, socket.Close()) }()
	if err := socket.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set traffic-control inspection deadline: %w", err)
	}
	header := nl.TcMsg{Ifindex: ifindex, Parent: parent}
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
		if actual.Ifindex != ifindex {
			continue
		}
		record, err := UnmarshalNativeTC(actual, message.Data[nl.SizeofTcMsg:])
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// UnmarshalNativeTC rejects duplicate, unsupported and truncated Linux TC attributes.
func UnmarshalNativeTC(header nl.TcMsg, data []byte) (record nativeTC, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("native traffic-control record decoding failed", "interface", header.Ifindex, "parent", header.Parent, "handle", header.Handle, "err", resultErr)
		}
	}()
	record = nativeTC{header: header, kind: tcUnknown, options: nil}
	attributes, err := mdnetlink.NewAttributeDecoder(data)
	if err != nil {
		return record, fmt.Errorf("decode traffic-control attribute list: %w", err)
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
			record.kind = tcKind(value[:len(value)-1])
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
	if record.kind == tcUnknown || header.Family != unix.AF_UNSPEC || header.Pad != [3]byte{} {
		return record, fmt.Errorf("traffic-control record lacks a supported header or kind")
	}
	return record, nil
}
