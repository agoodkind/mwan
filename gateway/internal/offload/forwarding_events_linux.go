//go:build linux

package offload

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	forwardingNeighborHeaderBytes      = 12
	forwardingNextHopHeaderBytes       = 8
	forwardingNeighborTableHeaderBytes = 4
)

type ForwardingEventLossError struct {
	Cause error
}

func (failure *ForwardingEventLossError) Error() string {
	return fmt.Sprintf("forwarding events may be incomplete: %v", failure.Cause)
}

func (failure *ForwardingEventLossError) Unwrap() error { return failure.Cause }

// Generation preserves change detection when several notifications coalesce.
type ForwardingEvents struct {
	Changes <-chan struct{}
	Errors  <-chan error

	generation    atomic.Uint64
	socket        *netlink.Conn
	done          chan struct{}
	stopping      chan struct{}
	closeOnce     sync.Once
	closeErr      error
	failureMu     sync.RWMutex
	terminalError error
}

func NewForwardingEvents(ctx context.Context, socketBufferBytes int) (*ForwardingEvents, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start forwarding events: %w", err)
	}
	if socketBufferBytes <= 0 {
		return nil, fmt.Errorf("forwarding events require a positive socket capacity")
	}
	socket, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, fmt.Errorf("open forwarding event socket: %w", err)
	}
	if err := socket.SetReadBuffer(socketBufferBytes); err != nil {
		return nil, errors.Join(fmt.Errorf("set forwarding event socket capacity: %w", err), socket.Close())
	}
	groups := []uint32{
		unix.RTNLGRP_LINK, unix.RTNLGRP_IPV4_IFADDR, unix.RTNLGRP_IPV6_IFADDR,
		unix.RTNLGRP_IPV4_ROUTE, unix.RTNLGRP_IPV6_ROUTE,
		unix.RTNLGRP_IPV4_RULE, unix.RTNLGRP_IPV6_RULE,
		unix.RTNLGRP_NEIGH, unix.RTNLGRP_IPV6_IFINFO, unix.RTNLGRP_NEXTHOP,
	}
	for _, group := range groups {
		if err := socket.JoinGroup(group); err != nil {
			return nil, errors.Join(fmt.Errorf("subscribe forwarding event group %d: %w", group, err), socket.Close())
		}
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect forwarding event socket: %w", err), socket.Close())
	}
	changes := make(chan struct{}, 1)
	failures := make(chan error, 1)
	source := &ForwardingEvents{
		Changes: changes, Errors: failures, socket: socket,
		done: make(chan struct{}), stopping: make(chan struct{}),
	}
	stop := context.AfterFunc(ctx, func() { source.closeSocket() })
	go func() {
		defer close(source.done)
		defer close(changes)
		defer close(failures)
		defer stop()
		defer source.closeSocket()
		if err := source.receive(ctx, raw, changes); err != nil {
			failure := &ForwardingEventLossError{Cause: err}
			source.failureMu.Lock()
			source.terminalError = failure
			source.failureMu.Unlock()
			source.changed(changes)
			failures <- failure
		}
	}()
	return source, nil
}

func (source *ForwardingEvents) Generation() uint64 { return source.generation.Load() }

func (source *ForwardingEvents) Err() error {
	source.failureMu.RLock()
	defer source.failureMu.RUnlock()
	return source.terminalError
}

func (source *ForwardingEvents) closeSocket() {
	source.closeOnce.Do(func() {
		close(source.stopping)
		source.closeErr = source.socket.Close()
	})
}

func (source *ForwardingEvents) Close() error {
	source.closeSocket()
	<-source.done
	if source.closeErr != nil {
		return fmt.Errorf("close forwarding event socket: %w", source.closeErr)
	}
	return nil
}

func (source *ForwardingEvents) changed(changes chan<- struct{}) {
	source.generation.Add(1)
	select {
	case changes <- struct{}{}:
	default:
	}
}

func (source *ForwardingEvents) receive(ctx context.Context, raw syscall.RawConn, changes chan<- struct{}) error {
	buffer := make([]byte, conntrackReceiveBytes)
	for {
		count, err := receiveConntrackDatagram(raw, buffer)
		if err != nil {
			select {
			case <-source.stopping:
				return nil
			default:
				return fmt.Errorf("receive forwarding event: %w", err)
			}
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:count])
		if err != nil {
			return fmt.Errorf("decode forwarding event datagram: %w", err)
		}
		mutation := false
		for _, message := range messages {
			changed, err := forwardingMutation(message)
			if err != nil {
				return err
			}
			mutation = mutation || changed
		}
		if mutation {
			source.changed(changes)
		}
		if err := ctx.Err(); err != nil {
			return nil
		}
	}
}

func forwardingMutation(message syscall.NetlinkMessage) (bool, error) {
	if message.Header.Flags&unix.NLM_F_MULTI != 0 {
		return false, fmt.Errorf("forwarding event unexpectedly contains a table dump")
	}
	var headerBytes int
	switch message.Header.Type {
	case unix.NLMSG_NOOP:
		return false, nil
	case unix.NLMSG_OVERRUN:
		return false, fmt.Errorf("kernel forwarding event overrun: %w", unix.ENOBUFS)
	case unix.NLMSG_ERROR:
		if len(message.Data) < conntrackErrorBytes {
			return false, fmt.Errorf("forwarding event error omitted its code")
		}
		code := int32(binary.NativeEndian.Uint32(message.Data[:conntrackErrorBytes]))
		if code < 0 {
			return false, fmt.Errorf("kernel forwarding event failed: %w", syscall.Errno(-code))
		}
		return false, fmt.Errorf("forwarding event unexpectedly contains an acknowledgment")
	case unix.RTM_NEWLINK, unix.RTM_DELLINK, unix.RTM_SETLINK, unix.RTM_NEWLINKPROP, unix.RTM_DELLINKPROP:
		headerBytes = unix.SizeofIfInfomsg
	case unix.RTM_NEWADDR, unix.RTM_DELADDR:
		headerBytes = unix.SizeofIfAddrmsg
	case unix.RTM_NEWROUTE, unix.RTM_DELROUTE, unix.RTM_NEWRULE, unix.RTM_DELRULE:
		headerBytes = unix.SizeofRtMsg
	case unix.RTM_NEWNEIGH, unix.RTM_DELNEIGH:
		headerBytes = forwardingNeighborHeaderBytes
	case unix.RTM_NEWNEIGHTBL, unix.RTM_SETNEIGHTBL:
		headerBytes = forwardingNeighborTableHeaderBytes
	case unix.RTM_NEWNEXTHOP, unix.RTM_DELNEXTHOP, unix.RTM_NEWNEXTHOPBUCKET, unix.RTM_DELNEXTHOPBUCKET:
		headerBytes = forwardingNextHopHeaderBytes
	default:
		return false, fmt.Errorf("forwarding event has unsupported message type %d", message.Header.Type)
	}
	if len(message.Data) < headerBytes {
		return false, fmt.Errorf("forwarding event type %d omitted its Linux header", message.Header.Type)
	}
	attributes, err := netlink.NewAttributeDecoder(message.Data[headerBytes:])
	if err != nil {
		return false, fmt.Errorf("decode forwarding event attributes: %w", err)
	}
	for attributes.Next() {
	}
	if err := attributes.Err(); err != nil {
		return false, fmt.Errorf("decode forwarding event attribute: %w", err)
	}
	return true, nil
}
