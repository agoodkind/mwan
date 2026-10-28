//go:build linux

package bpf

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

func inspectNativeQueues(ctx context.Context, link netlink.Link) (resultErr error) {
	queues, err := ReadNativeTC(ctx, link.Attrs().Index, unix.RTM_GETQDISC, 0)
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("%w: link=%+v queues=%+v", resultErr, link.Attrs(), queues)
		}
	}()
	roots, hooks := 0, 0
	rootKind := tcUnknown
	leaves := make(map[uint32]bool)
	for _, queue := range queues {
		if queue.kind == tcClsact {
			hooks++
			if queue.header.Handle != netlink.MakeHandle(0xffff, 0) || queue.header.Parent != netlink.HANDLE_CLSACT || len(queue.options) != 0 {
				return fmt.Errorf("native clsact attachment has unsupported attributes")
			}
			continue
		}
		if queue.header.Handle != 0 {
			return fmt.Errorf("native admission cannot bypass an explicitly configured qdisc")
		}
		if queue.header.Parent == netlink.HANDLE_ROOT {
			roots++
			rootKind = queue.kind
			if rootKind != tcNoQueue && rootKind != tcMQ || len(queue.options) != 0 {
				return fmt.Errorf("native admission cannot certify root qdisc %s", rootKind)
			}
			continue
		}
		if queue.header.Parent == 0 || queue.header.Parent > uint32(link.Attrs().NumTxQueues) || leaves[queue.header.Parent] {
			return fmt.Errorf("native admission cannot certify queue topology")
		}
		if err := inspectDefaultLeaf(queue, link.Attrs().MTU); err != nil {
			return err
		}
		leaves[queue.header.Parent] = true
	}
	if roots != 1 || hooks > 1 || rootKind == tcNoQueue && len(leaves) != 0 {
		return fmt.Errorf("native admission requires a complete kernel-default queue topology")
	}
	if rootKind == tcMQ {
		return inspectNativeMQ(ctx, link, leaves)
	}
	return nil
}

func inspectDefaultLeaf(queue nativeTC, mtu int) error {
	switch queue.kind {
	case tcFQCodel:
		return inspectDefaultFQCodel(queue.options, mtu)
	case tcPFIFOFast:
		defaults := nl.TcPrioMap{Bands: 3, Priomap: [netlink.PRIORITY_MAP_LEN]uint8{1, 2, 2, 2, 1, 2, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1}}
		if !bytes.Equal(queue.options, defaults.Serialize()) {
			return fmt.Errorf("pfifo_fast options differ from the kernel default")
		}
		return nil
	default:
		return fmt.Errorf("native admission cannot certify leaf qdisc %s", queue.kind)
	}
}

func inspectNativeMQ(ctx context.Context, link netlink.Link, leaves map[uint32]bool) error {
	classes, err := ReadNativeTC(ctx, link.Attrs().Index, unix.RTM_GETTCLASS, 0)
	if err != nil {
		return err
	}
	allocated := link.Attrs().NumTxQueues
	if allocated < 1 || len(classes) != allocated || len(leaves) == 0 || len(leaves) > allocated {
		return fmt.Errorf("mq class readback is incomplete: allocated=%d classes=%d active_leaves=%d", allocated, len(classes), len(leaves))
	}
	seen := make(map[uint32]bool, len(classes))
	for _, class := range classes {
		if class.kind != tcMQ || class.header.Parent != netlink.HANDLE_ROOT || class.header.Info != 0 || len(class.options) != 0 || class.header.Handle == 0 || class.header.Handle > uint32(allocated) || seen[class.header.Handle] {
			return fmt.Errorf("mq class differs from the default zero-handle child contract: %+v", class)
		}
		seen[class.header.Handle] = true
	}
	// mq_walk reports all allocated classes. mq_attach hashes only active
	// queue qdiscs, which the completed qdisc dump reports as a dense prefix.
	for index := range len(leaves) {
		if !leaves[uint32(index+1)] || !seen[uint32(index+1)] {
			return fmt.Errorf("mq active queue readback has a gap")
		}
	}
	return nil
}

func inspectDefaultFQCodel(data []byte, mtu int) (resultErr error) {
	const (
		defaultLimit        = 10240
		defaultFlows        = 1024
		defaultDropBatch    = 64
		defaultMemory       = 32 * 1024 * 1024
		ethernetHeaderBytes = 14
	)
	defer func() {
		if resultErr != nil {
			slog.Warn("native fq_codel policy inspection failed", "mtu", mtu, "err", resultErr)
		}
	}()
	if mtu < 1 || mtu > math.MaxUint16 {
		return fmt.Errorf("native fq_codel MTU %d exceeds the supported range", mtu)
	}
	options, err := mdnetlink.NewAttributeDecoder(data)
	if err != nil {
		return fmt.Errorf("decode fq_codel option list: %w", err)
	}
	expected := map[uint16]uint32{
		nl.TCA_FQ_CODEL_LIMIT: defaultLimit, nl.TCA_FQ_CODEL_FLOWS: defaultFlows,
		nl.TCA_FQ_CODEL_QUANTUM: uint32(mtu + ethernetHeaderBytes), nl.TCA_FQ_CODEL_ECN: 1,
		nl.TCA_FQ_CODEL_DROP_BATCH_SIZE: defaultDropBatch, nl.TCA_FQ_CODEL_MEMORY_LIMIT: defaultMemory,
		nl.TCA_FQ_CODEL_TARGET: 5000, nl.TCA_FQ_CODEL_INTERVAL: 100000,
	}
	for options.Next() {
		kind := options.Type()
		value, known := expected[kind]
		if !known {
			return fmt.Errorf("fq_codel contains an unknown or duplicate option %d", kind)
		}
		actual := options.Uint32()
		// Linux CoDel time conversion can round the default down by one microsecond.
		if actual != value && (kind != nl.TCA_FQ_CODEL_TARGET && kind != nl.TCA_FQ_CODEL_INTERVAL || actual+1 != value) {
			return fmt.Errorf("fq_codel option %d differs from the kernel default", kind)
		}
		delete(expected, kind)
	}
	if err := options.Err(); err != nil {
		return fmt.Errorf("decode fq_codel option value: %w", err)
	}
	if len(expected) != 0 {
		return fmt.Errorf("fq_codel inspection omitted required default options")
	}
	return nil
}
