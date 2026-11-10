//go:build linux

package offload

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"
	"sync"
	"syscall"

	"github.com/mdlayher/netlink"
	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"
	"golang.org/x/sys/unix"
)

// FreshConntrackFlow rejects tuple reuse after a kernel identity observation.
func FreshConntrackFlow(ctx context.Context, identity conntrack.Flow) (result conntrack.Flow, resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.WarnContext(ctx, "verify current conntrack identity failed", "id", identity.ID, "zone", identity.Zone, "err", resultErr)
		}
	}()
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
	current, err := readConntrackAttributes(ctx, family, attributes)
	if err != nil {
		delegatedFailure = true
		return conntrack.Flow{}, err
	}
	if current.ID != identity.ID || current.Zone != identity.Zone || current.TupleOrig != identity.TupleOrig || current.TupleReply != identity.TupleReply {
		return conntrack.Flow{}, fmt.Errorf("conntrack identity changed: %w", unix.ESTALE)
	}
	return current, nil
}

// LookupConntrackFlow uses exact tuple queries for connections already in Linux.
func LookupConntrackFlow(ctx context.Context, tuple conntrack.Tuple, zone uint16) (result conntrack.Flow, resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.WarnContext(ctx, "lookup conntrack tuple failed", "source", tuple.IP.SourceAddress, "destination", tuple.IP.DestinationAddress, "zone", zone, "err", resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return conntrack.Flow{}, fmt.Errorf("start conntrack tuple lookup: %w", err)
	}
	if err := validateConntrackLookupTuple(tuple); err != nil {
		return conntrack.Flow{}, err
	}
	for _, kind := range []uint16{ctTupleOriginal, ctTupleReply} {
		family, attribute := conntrackTupleAttribute(tuple, kind)
		current, err := readConntrackAttributes(ctx, family, []netfilter.Attribute{
			attribute, {Type: ctZone, Data: netfilter.Uint16Bytes(zone)},
		})
		if errors.Is(err, unix.ENOENT) && kind == ctTupleOriginal {
			continue
		}
		if err != nil {
			delegatedFailure = true
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

func readConntrackAttributes(ctx context.Context, family netfilter.ProtoFamily, attributes []netfilter.Attribute) (current conntrack.Flow, resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.WarnContext(ctx, "read conntrack identity attributes failed", "family", family, "err", resultErr)
		}
	}()
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
			delegatedFailure = false
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
	messages, err := readConntrackReplies(socket, message, true)
	if ctx.Err() != nil {
		return conntrack.Flow{}, fmt.Errorf("conntrack identity lookup interrupted: %w", ctx.Err())
	}
	if err != nil {
		delegatedFailure = true
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

type conntrackReplyState struct {
	replies      []netlink.Message
	entries      int
	acknowledged bool
	completed    bool
	failure      error
}

func readConntrackReplies(socket *netlink.Conn, message netlink.Message, expectsEntry bool) (replies []netlink.Message, resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.Warn("conntrack request failed", "type", message.Header.Type, "expects_entry", expectsEntry, "err", resultErr)
		}
	}()
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("inspect conntrack socket: %w", err)
	}
	request, err := socket.Send(message)
	if err != nil {
		return nil, fmt.Errorf("send conntrack request: %w", err)
	}
	buffer := make([]byte, conntrackReceiveBytes)
	var state conntrackReplyState
	state.replies = make([]netlink.Message, 0, conntrackLookupDatagrams)
	for range conntrackLookupDatagrams {
		count, err := readNetlinkDatagram(raw, buffer)
		if err != nil {
			delegatedFailure = true
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
			if err := state.unmarshalReply(request, reply, expectsEntry); err != nil {
				delegatedFailure = true
				return nil, err
			}
		}
		if state.acknowledged && (state.failure != nil || !expectsEntry || state.entries == 1) {
			return state.replies, state.failure
		}
	}
	return nil, fmt.Errorf("conntrack request omitted its connection or acknowledgment")
}

func (state *conntrackReplyState) unmarshalReply(request, reply netlink.Message, expectsEntry bool) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("conntrack response validation failed", "request_sequence", request.Header.Sequence, "reply_sequence", reply.Header.Sequence, "reply_type", reply.Header.Type, "err", resultErr)
		}
	}()
	if err := netlink.Validate(request, []netlink.Message{reply}); err != nil {
		return fmt.Errorf("validate conntrack response: %w", err)
	}
	if reply.Header.Flags&netlink.HeaderFlags(unix.NLM_F_DUMP_INTR) != 0 {
		state.failure = errors.Join(state.failure, fmt.Errorf("kernel interrupted the conntrack response"))
	}
	switch reply.Header.Type {
	case netlink.Noop:
		return nil
	case netlink.Overrun:
		return fmt.Errorf("kernel conntrack response overrun: %w", unix.ENOBUFS)
	case netlink.Done:
		if state.completed {
			state.failure = errors.Join(state.failure, fmt.Errorf("conntrack request returned multiple completions"))
		}
		if len(reply.Data) != 0 {
			state.failure = errors.Join(state.failure, unmarshalConntrackAcknowledgment(reply))
		}
		state.completed = true
	case netlink.Error:
		state.failure = errors.Join(state.failure, unmarshalConntrackAcknowledgment(reply))
		if state.acknowledged {
			state.failure = errors.Join(state.failure, fmt.Errorf("conntrack request returned multiple acknowledgments"))
		}
		state.acknowledged = true
		state.replies = append(state.replies, reply)
	default:
		state.entries++
		if !expectsEntry || state.entries > 1 {
			state.failure = errors.Join(state.failure, fmt.Errorf("conntrack request returned an unexpected connection"))
			return nil
		}
		state.replies = append(state.replies, reply)
	}
	return nil
}

func unmarshalConntrackAcknowledgment(message netlink.Message) error {
	errno, err := unmarshalNetlinkErrorCode(message.Data)
	if err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

func unmarshalNetlinkErrorCode(data []byte) (syscall.Errno, error) {
	if len(data) < conntrackErrorBytes {
		return 0, fmt.Errorf("netlink acknowledgment omitted its error code")
	}
	code := binary.NativeEndian.Uint32(data[:conntrackErrorBytes])
	if code == 0 {
		return 0, nil
	}
	if code <= math.MaxInt32 {
		return 0, fmt.Errorf("netlink acknowledgment returned invalid positive error code %d", code)
	}
	// Linux stores a negative signed errno in this unsigned wire word.
	return syscall.Errno(^code + 1), nil
}
