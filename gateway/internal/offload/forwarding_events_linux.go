//go:build linux

package offload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/mdlayher/netlink"
	rtnetlink "github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

const (
	forwardingNeighborHeaderBytes      = 12
	forwardingNextHopHeaderBytes       = 8
	forwardingNeighborTableHeaderBytes = 4
)

// ForwardingEventLossError invalidates evidence after a notification gap.
type ForwardingEventLossError struct {
	Cause error
}

// Error reports the failure that interrupted forwarding notifications.
func (failure *ForwardingEventLossError) Error() string {
	return fmt.Sprintf("forwarding events may be incomplete: %v", failure.Cause)
}

// Unwrap preserves the underlying socket or decoding failure.
func (failure *ForwardingEventLossError) Unwrap() error { return failure.Cause }

// ForwardingEvents retains every observed generation when change notifications coalesce.
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
	neighbors     forwardingNeighborCache
}

type forwardingMutation struct {
	message     syscall.NetlinkMessage
	invalidates bool
}

// NewForwardingEvents subscribes to kernel changes that invalidate forwarding evidence.
func NewForwardingEvents(ctx context.Context, socketBufferBytes int) (result *ForwardingEvents, resultErr error) {
	log := slog.Default()
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "start forwarding events failed", "err", resultErr)
		}
	}()
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
		unix.RTNLGRP_TC,
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
		generation: atomic.Uint64{}, closeOnce: sync.Once{}, closeErr: nil,
		failureMu: sync.RWMutex{}, terminalError: nil,
		neighbors: forwardingNeighborCache{entries: make(map[forwardingNeighborKey]rtnetlink.Neigh)},
	}
	stop := context.AfterFunc(ctx, func() { source.closeSocket() })
	go func() {
		defer close(source.done)
		defer close(changes)
		defer close(failures)
		defer stop()
		defer source.closeSocket()
		var receiveErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				receiveErr = fmt.Errorf("forwarding event receiver panicked: %v", recovered)
				slog.ErrorContext(ctx, "forwarding event receiver panicked", "err", receiveErr)
			}
			if receiveErr != nil {
				failure := &ForwardingEventLossError{Cause: receiveErr}
				source.failureMu.Lock()
				source.terminalError = failure
				source.failureMu.Unlock()
				source.changed(changes)
				failures <- failure
			}
		}()
		receiveErr = source.receive(ctx, log, raw, changes)
	}()
	return source, nil
}

// Generation counts changes even when the pending notification coalesces them.
func (source *ForwardingEvents) Generation() uint64 { return source.generation.Load() }

// Err retains notification loss after the source closes its channels.
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

// Close interrupts the receive and waits for failure publication and channel closure.
func (source *ForwardingEvents) Close() error {
	source.closeSocket()
	<-source.done
	if source.closeErr != nil {
		failure := fmt.Errorf("close forwarding event socket: %w", source.closeErr)
		slog.Warn("close forwarding events failed", "err", failure)
		return failure
	}
	return nil
}

func (source *ForwardingEvents) changed(changes chan<- struct{}) uint64 {
	generation := source.generation.Add(1)
	select {
	case changes <- struct{}{}:
	default:
	}
	return generation
}

func (source *ForwardingEvents) receive(ctx context.Context, log *slog.Logger, raw syscall.RawConn, changes chan<- struct{}) (resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.WarnContext(ctx, "forwarding event receive failed", "err", resultErr)
		}
	}()
	buffer := make([]byte, conntrackReceiveBytes)
	for {
		count, err := readNetlinkDatagram(raw, buffer)
		if err != nil {
			select {
			case <-source.stopping:
				return nil
			default:
				delegatedFailure = true
				return fmt.Errorf("receive forwarding event: %w", err)
			}
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:count])
		if err != nil {
			return fmt.Errorf("decode forwarding event datagram: %w", err)
		}
		mutation := false
		mutations := make([]forwardingMutation, 0, len(messages))
		for _, message := range messages {
			changed, err := unmarshalForwardingMutation(message)
			if err != nil {
				delegatedFailure = true
				return err
			}
			if !changed {
				continue
			}
			invalidates := source.neighbors.observe(message)
			mutation = mutation || invalidates
			mutations = append(mutations, forwardingMutation{message: message, invalidates: invalidates})
		}
		if len(mutations) != 0 {
			generation := source.Generation()
			if mutation {
				generation = source.changed(changes)
			}
			logForwardingMutations(ctx, log, generation, mutations)
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

func unmarshalForwardingMutation(message syscall.NetlinkMessage) (changed bool, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("kernel forwarding notification validation failed", "type", message.Header.Type, "sequence", message.Header.Seq, "err", resultErr)
		}
	}()
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
		errno, err := unmarshalNetlinkErrorCode(message.Data)
		if err != nil {
			return false, err
		}
		if errno != 0 {
			return false, fmt.Errorf("kernel forwarding event failed: %w", errno)
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
	case unix.RTM_NEWQDISC, unix.RTM_DELQDISC, unix.RTM_NEWTCLASS, unix.RTM_DELTCLASS,
		unix.RTM_NEWTFILTER, unix.RTM_DELTFILTER, unix.RTM_NEWCHAIN, unix.RTM_DELCHAIN:
		headerBytes = nl.SizeofTcMsg
	case unix.RTM_NEWACTION, unix.RTM_DELACTION:
		headerBytes = nl.SizeofTcActionMsg
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
