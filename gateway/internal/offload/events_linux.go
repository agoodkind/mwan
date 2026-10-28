//go:build linux

package offload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/ti-mo/conntrack"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
)

const (
	conntrackReceiveBytes    = 65536
	conntrackLookupDatagrams = 3
	conntrackErrorBytes      = 4
	// Linux assigns bits zero through two to the connection notification groups.
	conntrackEventGroups uint32 = 7
)

// ConntrackEvent records one kernel notification rather than a table snapshot.
type ConntrackEvent struct {
	Flow       conntrack.Flow
	Destroyed  bool
	ReceivedAt time.Time
}

// ConntrackEventLossError invalidates admissions that depend on complete events.
type ConntrackEventLossError struct {
	Cause error
}

// Error reports the cause that invalidated connection event continuity.
func (failure *ConntrackEventLossError) Error() string {
	return fmt.Sprintf("conntrack event continuity failed: %v", failure.Cause)
}

// Unwrap preserves the underlying socket or decoding failure.
func (failure *ConntrackEventLossError) Unwrap() error { return failure.Cause }

// ConntrackEvents stops after loss instead of silently omitting kernel changes.
type ConntrackEvents struct {
	Events <-chan ConntrackEvent
	Errors <-chan error

	socket        *netlink.Conn
	done          chan struct{}
	stopping      chan struct{}
	closeOnce     sync.Once
	closeErr      error
	failureMu     sync.RWMutex
	terminalError error
}

// NewConntrackEvents subscribes before callers inspect the existing connection table.
func NewConntrackEvents(ctx context.Context, capacity, socketBufferBytes int) (result *ConntrackEvents, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "start conntrack events failed", "err", resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start conntrack events: %w", err)
	}
	if capacity <= 0 || socketBufferBytes <= 0 {
		return nil, fmt.Errorf("conntrack events require positive event and socket capacities")
	}
	socket, err := netlink.Dial(unix.NETLINK_NETFILTER, &netlink.Config{Groups: conntrackEventGroups})
	if err != nil {
		return nil, fmt.Errorf("open conntrack event socket: %w", err)
	}
	if err := socket.SetReadBuffer(socketBufferBytes); err != nil {
		return nil, errors.Join(fmt.Errorf("set conntrack event socket capacity: %w", err), socket.Close())
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect conntrack event socket: %w", err), socket.Close())
	}
	events := make(chan ConntrackEvent, capacity)
	failures := make(chan error, 1)
	source := &ConntrackEvents{
		Events: events, Errors: failures, socket: socket,
		done: make(chan struct{}), stopping: make(chan struct{}),
		closeOnce: sync.Once{}, closeErr: nil, failureMu: sync.RWMutex{}, terminalError: nil,
	}
	stop := context.AfterFunc(ctx, func() { source.closeSocket() })
	go func() {
		defer close(source.done)
		defer close(events)
		defer close(failures)
		defer stop()
		defer source.closeSocket()
		var receiveErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				receiveErr = fmt.Errorf("conntrack event receiver panicked: %v", recovered)
				slog.ErrorContext(ctx, "conntrack event receiver panicked", "err", receiveErr)
			}
			if receiveErr != nil {
				failure := &ConntrackEventLossError{Cause: receiveErr}
				source.failureMu.Lock()
				source.terminalError = failure
				source.failureMu.Unlock()
				failures <- failure
			}
		}()
		receiveErr = source.receive(ctx, raw, events)
	}()
	return source, nil
}

// Err retains event loss after the notification channels close.
func (source *ConntrackEvents) Err() error {
	source.failureMu.RLock()
	defer source.failureMu.RUnlock()
	return source.terminalError
}

func (source *ConntrackEvents) closeSocket() {
	source.closeOnce.Do(func() {
		close(source.stopping)
		source.closeErr = source.socket.Close()
	})
}

// Close interrupts the receive and waits for failure publication and channel closure.
func (source *ConntrackEvents) Close() error {
	source.closeSocket()
	<-source.done
	if source.closeErr != nil {
		failure := fmt.Errorf("close conntrack event socket: %w", source.closeErr)
		slog.Warn("close conntrack events failed", "err", failure)
		return failure
	}
	return nil
}

func (source *ConntrackEvents) receive(ctx context.Context, raw syscall.RawConn, events chan<- ConntrackEvent) (resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.WarnContext(ctx, "conntrack event receive failed", "err", resultErr)
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
				return fmt.Errorf("receive conntrack event: %w", err)
			}
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:count])
		if err != nil {
			return fmt.Errorf("decode conntrack event datagram: %w", err)
		}
		for _, message := range messages {
			if message.Header.Type == unix.NLMSG_OVERRUN {
				return fmt.Errorf("kernel conntrack event overrun: %w", unix.ENOBUFS)
			}
			if message.Header.Type == unix.NLMSG_NOOP {
				continue
			}
			event, err := decodeConntrackEvent(ctx, message)
			if err != nil {
				delegatedFailure = true
				return err
			}
			select {
			case events <- event:
			case <-ctx.Done():
				return nil
			case <-source.stopping:
				return nil
			default:
				return fmt.Errorf("conntrack event capacity exceeded: %w", unix.ENOBUFS)
			}
		}
	}
}

func decodeConntrackEvent(ctx context.Context, message syscall.NetlinkMessage) (event ConntrackEvent, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "conntrack event receive failed", "type", message.Header.Type, "sequence", message.Header.Seq, "err", resultErr)
		}
	}()
	if message.Header.Flags&unix.NLM_F_MULTI != 0 {
		return ConntrackEvent{}, fmt.Errorf("conntrack event requires a single kernel message")
	}
	var decoded conntrack.Event
	if err := decoded.Unmarshal(netlink.Message{
		Header: netlink.Header{
			Length: message.Header.Len, Type: netlink.HeaderType(message.Header.Type),
			Flags: netlink.HeaderFlags(message.Header.Flags), Sequence: message.Header.Seq, PID: message.Header.Pid,
		},
		Data: message.Data,
	}); err != nil {
		return ConntrackEvent{}, fmt.Errorf("decode conntrack event: %w", err)
	}
	if decoded.Flow == nil {
		return ConntrackEvent{}, fmt.Errorf("conntrack event omitted its connection")
	}
	return ConntrackEvent{
		Flow: *decoded.Flow, Destroyed: decoded.Type == conntrack.EventDestroy, ReceivedAt: clock.Real{}.Now(),
	}, nil
}

func readNetlinkDatagram(raw syscall.RawConn, buffer []byte) (received int, resultErr error) {
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, net.ErrClosed) && !errors.Is(resultErr, os.ErrClosed) {
			slog.Warn("read kernel netlink datagram failed", "capacity", len(buffer), "err", resultErr)
		}
	}()
	var count, flags int
	var sender unix.Sockaddr
	var receiveErr error
	err := raw.Read(func(descriptor uintptr) bool {
		count, _, flags, sender, receiveErr = unix.Recvmsg(int(descriptor), buffer, nil, unix.MSG_DONTWAIT)
		return !errors.Is(receiveErr, unix.EAGAIN) && !errors.Is(receiveErr, unix.EINTR)
	})
	if err != nil {
		return 0, fmt.Errorf("read netlink socket descriptor: %w", err)
	}
	if receiveErr != nil {
		return 0, fmt.Errorf("receive netlink datagram: %w", receiveErr)
	}
	if flags&unix.MSG_TRUNC != 0 {
		return 0, fmt.Errorf("netlink datagram exceeds %d bytes: %w", len(buffer), unix.EMSGSIZE)
	}
	address, ok := sender.(*unix.SockaddrNetlink)
	if !ok || address.Pid != 0 {
		return 0, fmt.Errorf("netlink datagram sender is not the kernel")
	}
	if count == 0 {
		return 0, fmt.Errorf("kernel sent an empty netlink datagram")
	}
	return count, nil
}
