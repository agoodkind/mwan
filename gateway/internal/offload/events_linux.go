//go:build linux

package offload

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"
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

func (failure *ConntrackEventLossError) Error() string {
	return fmt.Sprintf("conntrack event continuity failed: %v", failure.Cause)
}

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

func NewConntrackEvents(ctx context.Context, capacity, socketBufferBytes int) (*ConntrackEvents, error) {
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
	}
	stop := context.AfterFunc(ctx, func() { source.closeSocket() })
	go func() {
		defer close(source.done)
		defer close(events)
		defer close(failures)
		defer stop()
		defer source.closeSocket()
		if err := source.receive(ctx, raw, events); err != nil {
			failure := &ConntrackEventLossError{Cause: err}
			source.failureMu.Lock()
			source.terminalError = failure
			source.failureMu.Unlock()
			failures <- failure
		}
	}()
	return source, nil
}

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

func (source *ConntrackEvents) Close() error {
	source.closeSocket()
	<-source.done
	if source.closeErr != nil {
		return fmt.Errorf("close conntrack event socket: %w", source.closeErr)
	}
	return nil
}

func (source *ConntrackEvents) receive(ctx context.Context, raw syscall.RawConn, events chan<- ConntrackEvent) error {
	buffer := make([]byte, conntrackReceiveBytes)
	for {
		count, err := receiveConntrackDatagram(raw, buffer)
		if err != nil {
			select {
			case <-source.stopping:
				return nil
			default:
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
			if message.Header.Flags&unix.NLM_F_MULTI != 0 {
				return fmt.Errorf("conntrack event requires a single kernel message")
			}
			var decoded conntrack.Event
			if err := decoded.Unmarshal(netlink.Message{
				Header: netlink.Header{
					Length: message.Header.Len, Type: netlink.HeaderType(message.Header.Type),
					Flags: netlink.HeaderFlags(message.Header.Flags), Sequence: message.Header.Seq, PID: message.Header.Pid,
				},
				Data: message.Data,
			}); err != nil {
				return fmt.Errorf("decode conntrack event: %w", err)
			}
			if decoded.Flow == nil {
				return fmt.Errorf("conntrack event omitted its connection")
			}
			event := ConntrackEvent{
				Flow: *decoded.Flow, Destroyed: decoded.Type == conntrack.EventDestroy, ReceivedAt: clock.Real{}.Now(),
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

func receiveConntrackDatagram(raw syscall.RawConn, buffer []byte) (int, error) {
	var count, flags int
	var sender unix.Sockaddr
	var receiveErr error
	err := raw.Read(func(descriptor uintptr) bool {
		count, _, flags, sender, receiveErr = unix.Recvmsg(int(descriptor), buffer, nil, unix.MSG_DONTWAIT)
		return !errors.Is(receiveErr, unix.EAGAIN) && !errors.Is(receiveErr, unix.EINTR)
	})
	if err != nil {
		return 0, err
	}
	if receiveErr != nil {
		return 0, receiveErr
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

// FreshConntrackFlow rejects tuple reuse after a kernel identity observation.
func FreshConntrackFlow(ctx context.Context, identity conntrack.Flow) (conntrack.Flow, error) {
	if err := ctx.Err(); err != nil {
		return conntrack.Flow{}, fmt.Errorf("start conntrack identity lookup: %w", err)
	}
	if identity.ID == 0 {
		return conntrack.Flow{}, fmt.Errorf("conntrack identity lookup requires a connection ID")
	}
	if err := validateConntrackLookupTuple(identity.TupleOrig); err != nil {
		return conntrack.Flow{}, err
	}
	family, attributes := conntrackIdentity(identity)
	current, err := lookupConntrackAttributes(ctx, family, attributes)
	if err != nil {
		return conntrack.Flow{}, err
	}
	if current.ID != identity.ID || current.Zone != identity.Zone || current.TupleOrig != identity.TupleOrig || current.TupleReply != identity.TupleReply {
		return conntrack.Flow{}, fmt.Errorf("conntrack identity changed: %w", unix.ESTALE)
	}
	return current, nil
}

// LookupConntrackFlow uses exact tuple queries for connections already in Linux.
func LookupConntrackFlow(ctx context.Context, tuple conntrack.Tuple, zone uint16) (conntrack.Flow, error) {
	if err := ctx.Err(); err != nil {
		return conntrack.Flow{}, fmt.Errorf("start conntrack tuple lookup: %w", err)
	}
	if err := validateConntrackLookupTuple(tuple); err != nil {
		return conntrack.Flow{}, err
	}
	for _, kind := range []uint16{ctTupleOriginal, ctTupleReply} {
		family, attribute := conntrackTupleAttribute(tuple, kind)
		current, err := lookupConntrackAttributes(ctx, family, []netfilter.Attribute{
			attribute, {Type: ctZone, Data: netfilter.Uint16Bytes(zone)},
		})
		if errors.Is(err, unix.ENOENT) && kind == ctTupleOriginal {
			continue
		}
		if err != nil {
			return conntrack.Flow{}, err
		}
		matched := current.TupleOrig == tuple || current.TupleReply == tuple
		if current.ID == 0 || current.Zone != zone || !matched {
			return conntrack.Flow{}, fmt.Errorf("conntrack tuple lookup returned a different identity: %w", unix.ESTALE)
		}
		return current, nil
	}
	return conntrack.Flow{}, fmt.Errorf("conntrack tuple lookup did not find a connection: %w", unix.ENOENT)
}

func validateConntrackLookupTuple(tuple conntrack.Tuple) error {
	if tuple.Proto.Protocol != unix.IPPROTO_TCP && tuple.Proto.Protocol != unix.IPPROTO_UDP || tuple.Proto.ICMPv4 || tuple.Proto.ICMPv6 {
		return fmt.Errorf("conntrack tuple lookup requires TCP or UDP")
	}
	if tuple.Proto.SourcePort == 0 || tuple.Proto.DestinationPort == 0 {
		return fmt.Errorf("conntrack tuple lookup requires both transport ports")
	}
	for _, address := range []netip.Addr{tuple.IP.SourceAddress, tuple.IP.DestinationAddress} {
		if !address.IsValid() || address.IsUnspecified() || address.Is4In6() || address.Zone() != "" {
			return fmt.Errorf("conntrack tuple lookup requires unambiguous IP addresses")
		}
	}
	if tuple.IP.SourceAddress.Is4() != tuple.IP.DestinationAddress.Is4() {
		return fmt.Errorf("conntrack tuple lookup requires one address family")
	}
	return nil
}

func lookupConntrackAttributes(ctx context.Context, family netfilter.ProtoFamily, attributes []netfilter.Attribute) (current conntrack.Flow, resultErr error) {
	if err := ctx.Err(); err != nil {
		return conntrack.Flow{}, fmt.Errorf("start conntrack identity lookup: %w", err)
	}
	socket, err := netlink.Dial(unix.NETLINK_NETFILTER, nil)
	if err != nil {
		return conntrack.Flow{}, fmt.Errorf("open conntrack identity socket: %w", err)
	}
	var once sync.Once
	var closeErr error
	closeSocket := func() { once.Do(func() { closeErr = socket.Close() }) }
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		closeSocket()
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		closeSocket()
		if closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close conntrack identity socket: %w", closeErr))
		}
	}()
	if err := socket.SetReadBuffer(conntrackReceiveBytes); err != nil {
		return conntrack.Flow{}, fmt.Errorf("set conntrack identity socket capacity: %w", err)
	}
	message, err := netfilter.MarshalNetlink(netfilter.Header{
		SubsystemID: netfilter.NFSubsysCTNetlink, MessageType: ctMessageGet,
		Family: family, Flags: netlink.Request | netlink.Acknowledge,
	}, attributes)
	if err != nil {
		return conntrack.Flow{}, fmt.Errorf("encode conntrack identity request: %w", err)
	}
	messages, err := queryBoundedConntrack(socket, message, true)
	if ctx.Err() != nil {
		return conntrack.Flow{}, fmt.Errorf("conntrack identity lookup interrupted: %w", ctx.Err())
	}
	if err != nil {
		return conntrack.Flow{}, fmt.Errorf("read conntrack identity: %w", err)
	}
	for _, reply := range messages {
		if reply.Header.Type == netlink.Error {
			continue
		}
		var decoded conntrack.Event
		if err := decoded.Unmarshal(reply); err != nil {
			return conntrack.Flow{}, fmt.Errorf("decode conntrack identity: %w", err)
		}
		if decoded.Flow == nil {
			return conntrack.Flow{}, fmt.Errorf("conntrack identity lookup omitted its connection")
		}
		return *decoded.Flow, nil
	}
	return conntrack.Flow{}, fmt.Errorf("conntrack identity lookup omitted its connection")
}

func queryBoundedConntrack(socket *netlink.Conn, message netlink.Message, expectsEntry bool) ([]netlink.Message, error) {
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("inspect conntrack socket: %w", err)
	}
	request, err := socket.Send(message)
	if err != nil {
		return nil, fmt.Errorf("send conntrack request: %w", err)
	}
	buffer := make([]byte, conntrackReceiveBytes)
	replies := make([]netlink.Message, 0, conntrackLookupDatagrams)
	entries := 0
	acknowledged := false
	completed := false
	var responseError error
	for range conntrackLookupDatagrams {
		count, err := receiveConntrackDatagram(raw, buffer)
		if err != nil {
			return nil, fmt.Errorf("receive conntrack response: %w", err)
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:count])
		if err != nil {
			return nil, fmt.Errorf("decode conntrack response datagram: %w", err)
		}
		for _, received := range messages {
			reply := netlink.Message{
				Header: netlink.Header{
					Length: received.Header.Len, Type: netlink.HeaderType(received.Header.Type),
					Flags: netlink.HeaderFlags(received.Header.Flags), Sequence: received.Header.Seq, PID: received.Header.Pid,
				},
				Data: slices.Clone(received.Data),
			}
			if err := netlink.Validate(request, []netlink.Message{reply}); err != nil {
				return nil, fmt.Errorf("validate conntrack response: %w", err)
			}
			if reply.Header.Flags&netlink.HeaderFlags(unix.NLM_F_DUMP_INTR) != 0 {
				responseError = errors.Join(responseError, fmt.Errorf("kernel interrupted the conntrack response"))
			}
			if reply.Header.Type == netlink.Noop {
				continue
			}
			if reply.Header.Type == netlink.Done {
				if completed {
					responseError = errors.Join(responseError, fmt.Errorf("conntrack request returned multiple completions"))
				}
				if len(reply.Data) != 0 {
					responseError = errors.Join(responseError, conntrackAcknowledgment(reply))
				}
				completed = true
				continue
			}
			if reply.Header.Type == netlink.Error {
				responseError = errors.Join(responseError, conntrackAcknowledgment(reply))
				if acknowledged {
					responseError = errors.Join(responseError, fmt.Errorf("conntrack request returned multiple acknowledgments"))
				}
				acknowledged = true
				replies = append(replies, reply)
				continue
			}
			entries++
			if !expectsEntry || entries > 1 {
				responseError = errors.Join(responseError, fmt.Errorf("conntrack request returned an unexpected connection"))
				continue
			}
			replies = append(replies, reply)
		}
		if acknowledged && (responseError != nil || !expectsEntry || entries == 1) {
			return replies, responseError
		}
	}
	return nil, fmt.Errorf("conntrack request omitted its connection or acknowledgment")
}

func conntrackAcknowledgment(message netlink.Message) error {
	if len(message.Data) < conntrackErrorBytes {
		return fmt.Errorf("conntrack identity acknowledgment omitted its error code")
	}
	code := int32(binary.NativeEndian.Uint32(message.Data[:conntrackErrorBytes]))
	if code < 0 {
		return fmt.Errorf("conntrack identity request failed: %w", syscall.Errno(-code))
	}
	if code != 0 {
		return fmt.Errorf("conntrack identity acknowledgment returned invalid error code %d", code)
	}
	return nil
}
