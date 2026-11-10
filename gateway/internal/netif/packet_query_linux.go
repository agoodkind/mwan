//go:build linux

package netif

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"syscall"

	"github.com/mdlayher/netlink"
	upstreamnetlink "github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	packetQueryBytes     = 65536
	packetQueryDatagrams = 8
	packetQueryAlignment = 4
	packetMaximumErrno   = 4095
)

type packetQueryOperation uint8

const (
	packetQueryRoute packetQueryOperation = iota
	packetQueryFIB
	packetQueryNexthop
)

func (operation packetQueryOperation) String() string {
	switch operation {
	case packetQueryRoute:
		return "packet route"
	case packetQueryFIB:
		return "packet FIB match"
	case packetQueryNexthop:
		return "packet nexthop"
	default:
		return "unknown packet query"
	}
}

type packetQuery struct {
	operation packetQueryOperation
	objectID  uint32
	request   netlink.Message
	response  netlink.HeaderType
}

type packetQueryError struct {
	operation packetQueryOperation
	objectID  uint32
	stage     string
	cause     error
}

func (failure *packetQueryError) Error() string {
	return fmt.Sprintf("%s %s (nexthop_id=%d): %v", failure.operation, failure.stage, failure.objectID, failure.cause)
}

func (failure *packetQueryError) Unwrap() error { return failure.cause }

func (failure *packetQueryError) LogValue() slog.Value {
	return slog.GroupValue(slog.String("operation", failure.operation.String()), slog.String("stage", failure.stage),
		slog.Uint64("nexthop_id", uint64(failure.objectID)), slog.String("error", failure.cause.Error()))
}

func (query packetQuery) failure(stage string, cause error) error {
	return &packetQueryError{operation: query.operation, objectID: query.objectID, stage: stage, cause: cause}
}

func (query packetQuery) execute(ctx context.Context) (data []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, query.failure("start", err)
	}
	// Dial creates the socket synchronously in the calling thread's namespace.
	socket, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, query.failure("open socket", err)
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
			resultErr = errors.Join(resultErr, query.failure("close socket", closeErr))
		}
	}()
	deadline, bounded := ctx.Deadline()
	if !bounded {
		deadline = realClock{}.Now().Add(upstreamnetlink.GetSocketTimeout())
	}
	if err := socket.SetDeadline(deadline); err != nil {
		return nil, query.failure("set deadline", err)
	}
	if err := socket.SetReadBuffer(packetQueryBytes); err != nil {
		return nil, query.failure("set receive capacity", err)
	}
	data, err = query.read(socket)
	if err := ctx.Err(); err != nil {
		return nil, query.failure("complete", err)
	}
	return data, err
}

type packetQueryReplies struct {
	query        *packetQuery
	request      netlink.Message
	data         []byte
	acknowledged bool
	completed    bool
}

func (query packetQuery) read(socket *netlink.Conn) ([]byte, error) {
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, query.failure("inspect socket", err)
	}
	request, err := socket.Send(query.request)
	if err != nil {
		return nil, query.failure("send request", err)
	}
	buffer := make([]byte, packetQueryBytes)
	var state packetQueryReplies
	state.query, state.request = &query, request
	reader := packetQueryReader{raw: raw}
	for range packetQueryDatagrams {
		count, err := reader.Read(buffer)
		if err != nil {
			return nil, query.failure("receive response", err)
		}
		if err := state.UnmarshalDatagram(buffer[:count]); err != nil {
			return nil, query.failure("validate response", err)
		}
		if state.acknowledged {
			if state.data == nil {
				return nil, query.failure("validate response", errors.New("kernel omitted the requested object"))
			}
			return state.data, nil
		}
	}
	return nil, query.failure("validate response", errors.New("kernel omitted the request acknowledgment"))
}

type packetQueryReader struct {
	raw syscall.RawConn
}

func (reader packetQueryReader) Read(buffer []byte) (int, error) {
	var count int
	var receiveErr error
	err := reader.raw.Read(func(descriptor uintptr) bool {
		if descriptor > math.MaxInt32 {
			receiveErr = errors.New("netlink descriptor exceeds the kernel file descriptor range")
			return true
		}
		var flags int
		var sender unix.Sockaddr
		count, _, flags, sender, receiveErr = unix.Recvmsg(int(descriptor), buffer, nil, 0)
		if errors.Is(receiveErr, unix.EAGAIN) || errors.Is(receiveErr, unix.EINTR) {
			return false
		}
		if receiveErr != nil {
			return true
		}
		peer, ok := sender.(*unix.SockaddrNetlink)
		switch {
		case !ok || peer.Pid != 0:
			receiveErr = errors.New("netlink response did not originate from the kernel")
		case flags&unix.MSG_TRUNC != 0:
			receiveErr = fmt.Errorf("netlink response exceeds receive capacity: %w", unix.EMSGSIZE)
		case count <= 0 || count > len(buffer):
			receiveErr = errors.New("kernel returned an invalid netlink datagram size")
		}
		return true
	})
	if err != nil {
		return 0, fmt.Errorf("read netlink query datagram: %w", err)
	}
	if receiveErr != nil {
		return 0, fmt.Errorf("receive kernel netlink datagram: %w", receiveErr)
	}
	return count, nil
}

func (state *packetQueryReplies) UnmarshalDatagram(datagram []byte) error {
	for len(datagram) > 0 {
		if len(datagram) < unix.NLMSG_HDRLEN {
			return errors.New("netlink response omitted its message header")
		}
		length := binary.NativeEndian.Uint32(datagram)
		if length < unix.NLMSG_HDRLEN || uint64(length) > uint64(len(datagram)) {
			return errors.New("netlink response has an invalid message length")
		}
		aligned := (uint64(length) + packetQueryAlignment - 1) &^ uint64(packetQueryAlignment-1)
		if aligned > uint64(len(datagram)) {
			return errors.New("netlink response omitted message alignment bytes")
		}
		if err := state.UnmarshalMessage(datagram[:length]); err != nil {
			return err
		}
		datagram = datagram[aligned:]
	}
	return nil
}

func (state *packetQueryReplies) UnmarshalMessage(data []byte) error {
	if state.query == nil {
		return errors.New("packet query decoder requires a request context")
	}
	if len(data) < unix.NLMSG_HDRLEN {
		return errors.New("netlink response omitted its message header")
	}
	length := binary.NativeEndian.Uint32(data)
	if uint64(length) != uint64(len(data)) {
		return errors.New("netlink response has an invalid message length")
	}
	var reply netlink.Message
	reply.Header.Length = length
	reply.Header.Type = netlink.HeaderType(binary.NativeEndian.Uint16(data[4:6]))
	reply.Header.Flags = netlink.HeaderFlags(binary.NativeEndian.Uint16(data[6:8]))
	reply.Header.Sequence = binary.NativeEndian.Uint32(data[8:12])
	reply.Header.PID = binary.NativeEndian.Uint32(data[12:16])
	reply.Data = data[unix.NLMSG_HDRLEN:]
	if err := netlink.Validate(state.request, []netlink.Message{reply}); err != nil {
		return fmt.Errorf("validate netlink reply identity: %w", err)
	}
	if reply.Header.Flags&netlink.HeaderFlags(unix.NLM_F_DUMP_INTR) != 0 {
		return errors.New("kernel interrupted the packet query response")
	}
	switch reply.Header.Type {
	case netlink.Noop:
		return nil
	case netlink.Overrun:
		return fmt.Errorf("kernel packet query response overrun: %w", unix.ENOBUFS)
	case netlink.Error:
		if state.acknowledged {
			return errors.New("kernel returned multiple packet query acknowledgments")
		}
		state.acknowledged = true
		return packetQueryKernelError(reply.Data)
	case netlink.Done:
		if state.completed {
			return errors.New("kernel returned multiple packet query completions")
		}
		state.completed = true
		if len(reply.Data) != 0 {
			return packetQueryKernelError(reply.Data)
		}
		return nil
	default:
		if reply.Header.Type != state.query.response {
			return fmt.Errorf("kernel packet query returned message type %d, expected %d", reply.Header.Type, state.query.response)
		}
		if state.data != nil {
			if state.query.operation != packetQueryNexthop {
				return &EgressObservationRequiredError{Reason: EgressObservationMultipleRoutes, NexthopID: 0}
			}
			return errors.New("kernel returned multiple nexthop objects")
		}
		state.data = append([]byte{}, reply.Data...)
		return nil
	}
}

func packetQueryKernelError(data []byte) error {
	if len(data) < packetQueryAlignment {
		return errors.New("kernel packet query acknowledgment omitted its error code")
	}
	code := binary.NativeEndian.Uint32(data)
	if code == 0 {
		return nil
	}
	errno := ^code + 1
	if errno == 0 || errno > packetMaximumErrno {
		return fmt.Errorf("kernel packet query returned invalid error code %d", code)
	}
	return syscall.Errno(errno)
}
