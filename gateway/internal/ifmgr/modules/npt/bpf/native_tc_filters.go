//go:build linux

package bpf

import (
	"bytes"
	"context"
	"fmt"
	"math/bits"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

func inspectNativeFilters(ctx context.Context, index int, direction Direction, programID uint32) error {
	filters, err := readNativeTC(ctx, index, unix.RTM_GETTFILTER, filterParent(direction))
	if err != nil {
		return err
	}
	if len(filters) == 0 && programID == 0 {
		return nil
	}
	info := uint32(filterPriority)*0x10000 | uint32(bits.ReverseBytes16(unix.ETH_P_ALL))
	// Linux can emit the classifier protocol header before its concrete filter.
	if len(filters) == 2 && filters[0].kind == "bpf" && filters[0].header.Handle == 0 && filters[0].header.Parent == filterParent(direction) && filters[0].header.Info == info && len(filters[0].options) == 0 {
		filters = filters[1:]
	}
	if len(filters) != 1 || programID == 0 {
		return fmt.Errorf("native admission cannot certify additional or missing classifiers")
	}
	filter := filters[0]
	if filter.kind != "bpf" || filter.header.Parent != filterParent(direction) || filter.header.Handle != filterHandle || filter.header.Info != info {
		return fmt.Errorf("native classifier differs from the current NPTv6 attachment")
	}
	return inspectNativeBPF(filter.options, direction, programID)
}

func inspectNativeBPF(data []byte, direction Direction, programID uint32) error {
	const notInHardware = 8
	attributes, err := mdnetlink.NewAttributeDecoder(data)
	if err != nil {
		return err
	}
	seen := make(map[uint16]bool)
	for attributes.Next() {
		kind := attributes.Type()
		if seen[kind] {
			return fmt.Errorf("NPTv6 classifier repeats an option")
		}
		seen[kind] = true
		switch kind {
		case nl.TCA_BPF_ID:
			if attributes.Uint32() != programID {
				return fmt.Errorf("NPTv6 classifier references another program")
			}
		case nl.TCA_BPF_NAME:
			if !bytes.Equal(attributes.Bytes(), nl.ZeroTerminated(filterName(direction))) {
				return fmt.Errorf("NPTv6 classifier label differs from its owned attachment")
			}
		case nl.TCA_BPF_FLAGS:
			if attributes.Uint32() != nl.TCA_BPF_FLAG_ACT_DIRECT {
				return fmt.Errorf("NPTv6 classifier does not use its direct action")
			}
		case nl.TCA_BPF_FLAGS_GEN:
			if flags := attributes.Uint32(); flags != 0 && flags != notInHardware {
				return fmt.Errorf("NPTv6 classifier uses unsupported execution flags")
			}
		case nl.TCA_BPF_TAG:
			if len(attributes.Bytes()) != 8 {
				return fmt.Errorf("NPTv6 classifier has an invalid program tag")
			}
		case nl.TCA_BPF_CLASSID:
			if attributes.Uint32() != 0 {
				return fmt.Errorf("NPTv6 classifier selects an unsupported queue class")
			}
		default:
			return fmt.Errorf("NPTv6 classifier contains unsupported option %d", kind)
		}
	}
	if err := attributes.Err(); err != nil {
		return err
	}
	if !seen[nl.TCA_BPF_ID] || !seen[nl.TCA_BPF_NAME] || !seen[nl.TCA_BPF_FLAGS] {
		return fmt.Errorf("NPTv6 classifier omitted its program identity or action")
	}
	return nil
}
