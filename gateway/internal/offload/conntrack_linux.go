//go:build linux

// Package offload manages kernel state required for hardware forwarding.
package offload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
)

// These identifiers follow linux/netfilter/nfnetlink_conntrack.h. The
// conntrack dependency keeps them private and its TCP encoder also writes
// state and window scales, which an offload update must preserve.
const (
	ctMessageNew       netfilter.MessageType = 0
	ctMessageGet       netfilter.MessageType = 1
	ctTupleOriginal    uint16                = 1
	ctTupleReply       uint16                = 2
	ctProtocolInfo     uint16                = 4
	ctTimeout          uint16                = 7
	ctID               uint16                = 12
	ctZone             uint16                = 18
	ctTupleIP          uint16                = 1
	ctTupleProtocol    uint16                = 2
	ctTupleZone        uint16                = 3
	ctIPv4Source       uint16                = 1
	ctIPv4Destination  uint16                = 2
	ctIPv6Source       uint16                = 3
	ctIPv6Destination  uint16                = 4
	ctProtocolNumber   uint16                = 1
	ctSourcePort       uint16                = 2
	ctDestinationPort  uint16                = 3
	ctTCPInfo          uint16                = 1
	ctTCPFlagsOriginal uint16                = 4
	ctTCPFlagsReply    uint16                = 5
	tcpBeLiberal       byte                  = 0x08
)

// ConntrackUpdater serializes requests and socket closure in one network namespace.
type ConntrackUpdater struct {
	mu       sync.Mutex
	socket   *netlink.Conn
	timeout  time.Duration
	clock    clock.Clock
	closed   bool
	closeErr error
}

// NewConntrackUpdater uses the calling thread's network namespace.
func NewConntrackUpdater(timeout time.Duration) (updater *ConntrackUpdater, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("open conntrack updater failed", "err", resultErr)
		}
	}()
	if timeout <= 0 {
		return nil, fmt.Errorf("conntrack update timeout must be positive")
	}
	socket, err := netlink.Dial(unix.NETLINK_NETFILTER, nil)
	if err != nil {
		return nil, fmt.Errorf("open conntrack update socket: %w", err)
	}
	if err := socket.SetReadBuffer(conntrackReceiveBytes); err != nil {
		return nil, errors.Join(fmt.Errorf("set conntrack update socket capacity: %w", err), socket.Close())
	}
	return &ConntrackUpdater{
		mu: sync.Mutex{}, socket: socket, timeout: timeout, clock: clock.Real{},
		closed: false, closeErr: nil,
	}, nil
}

// SetLiberal preserves TCP state, window scales, and unrelated flags.
// The kernel does not compare CTA_ID on updates. The reads reject stale
// identities and detect replacement, but cannot make tuple reuse atomic
// with the update. Callers must not treat this as a compare-and-swap.
func (u *ConntrackUpdater) SetLiberal(ctx context.Context, flow conntrack.Flow) (resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.WarnContext(ctx, "conntrack TCP flag update failed", "id", flow.ID, "zone", flow.Zone, "err", resultErr)
		}
	}()
	if err := validateTCPIdentity(flow); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	finish, err := u.beginRequest(ctx)
	if err != nil {
		return err
	}
	defer finish()

	family, identity := conntrackIdentity(flow)
	if _, err := u.readIdentity(family, identity, flow); err != nil {
		delegatedFailure = true
		return fmt.Errorf("verify conntrack before update: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("conntrack update context: %w", err)
	}
	attributes := slices.Clone(identity)
	attributes = append(attributes, netfilter.Attribute{
		Type: ctProtocolInfo, Nested: true, Children: []netfilter.Attribute{{
			Type: ctTCPInfo, Nested: true, Children: []netfilter.Attribute{
				{Type: ctTCPFlagsOriginal, Data: []byte{tcpBeLiberal, tcpBeLiberal}},
				{Type: ctTCPFlagsReply, Data: []byte{tcpBeLiberal, tcpBeLiberal}},
			},
		}},
	})
	if _, err := u.query(family, ctMessageNew, attributes); err != nil {
		delegatedFailure = true
		return fmt.Errorf("set conntrack liberal TCP flags: %w", err)
	}
	current, err := u.readIdentity(family, identity, flow)
	if err != nil {
		delegatedFailure = true
		return fmt.Errorf("verify conntrack after update: %w", err)
	}
	// The dependency decodes the two-byte {flags, mask} payload as big endian.
	if current.ProtoInfo.TCP == nil ||
		byte(current.ProtoInfo.TCP.OriginalFlags>>8)&tcpBeLiberal == 0 ||
		byte(current.ProtoInfo.TCP.ReplyFlags>>8)&tcpBeLiberal == 0 {
		return fmt.Errorf("conntrack %d did not retain both liberal TCP flags", flow.ID)
	}
	return nil
}

// Refresh updates only expiry after the caller verifies hardware activity.
func (u *ConntrackUpdater) Refresh(ctx context.Context, flow conntrack.Flow, timeoutSeconds uint32) (resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.WarnContext(ctx, "conntrack timeout refresh failed", "id", flow.ID, "zone", flow.Zone, "err", resultErr)
		}
	}()
	if timeoutSeconds == 0 || flow.ID == 0 {
		return fmt.Errorf("conntrack refresh requires a connection ID and positive timeout")
	}
	if err := validateConntrackLookupTuple(flow.TupleOrig); err != nil {
		return err
	}
	if err := validateConntrackLookupTuple(flow.TupleReply); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	finish, err := u.beginRequest(ctx)
	if err != nil {
		return err
	}
	defer finish()
	family, identity := conntrackIdentity(flow)
	if _, err := u.readIdentity(family, identity, flow); err != nil {
		delegatedFailure = true
		return fmt.Errorf("verify conntrack before timeout refresh: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("conntrack refresh context: %w", err)
	}
	attributes := slices.Clone(identity)
	attributes = append(attributes, netfilter.Attribute{Type: ctTimeout, Data: netfilter.Uint32Bytes(timeoutSeconds)})
	started := u.clock.Now()
	if _, err := u.query(family, ctMessageNew, attributes); err != nil {
		delegatedFailure = true
		return fmt.Errorf("refresh conntrack timeout: %w", err)
	}
	current, err := u.readIdentity(family, identity, flow)
	if err != nil {
		delegatedFailure = true
		return fmt.Errorf("verify conntrack after timeout refresh: %w", err)
	}
	elapsed := u.clock.Now().Sub(started)
	if elapsed < 0 {
		return fmt.Errorf("conntrack timeout verification clock moved backward")
	}
	elapsedSeconds := uint64(elapsed/time.Second) + 1
	// Kernel packets can extend expiry between the update and readback.
	if uint64(current.Timeout)+elapsedSeconds < uint64(timeoutSeconds) {
		return &ConntrackTimeoutVerificationError{
			ConnectionID: flow.ID, RequestedSeconds: timeoutSeconds,
			ObservedSeconds: current.Timeout, Elapsed: elapsed, AllowanceSeconds: elapsedSeconds,
		}
	}
	return nil
}

func (u *ConntrackUpdater) beginRequest(ctx context.Context) (func(), error) {
	if u.closed {
		return nil, fmt.Errorf("conntrack updater: %w", net.ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("conntrack update context: %w", err)
	}
	deadline := u.clock.Now().Add(u.timeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := u.socket.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set conntrack update deadline: %w", err)
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		if err := u.socket.SetDeadline(u.clock.Now()); err != nil {
			slog.WarnContext(ctx, "interrupt conntrack update failed", "err", err)
		}
		close(finished)
	})
	return func() {
		if !stop() {
			<-finished
		}
	}, nil
}

// Close waits for the bounded active update and retains the first close result.
func (u *ConntrackUpdater) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.closed {
		u.closed = true
		if err := u.socket.Close(); err != nil {
			slog.Warn("close conntrack update socket failed", "err", err)
			u.closeErr = fmt.Errorf("close conntrack update socket: %w", err)
		}
	}
	return u.closeErr
}

func validateTCPIdentity(flow conntrack.Flow) error {
	if flow.ID == 0 {
		return fmt.Errorf("conntrack update requires a connection ID")
	}
	if err := validateTCPTuple(flow.TupleOrig, "original"); err != nil {
		return err
	}
	reply := flow.TupleReply
	if reply.IP.SourceAddress.IsValid() || reply.IP.DestinationAddress.IsValid() || reply.Proto.Protocol != 0 {
		if err := validateTCPTuple(reply, "reply"); err != nil {
			return err
		}
		if reply.IP.SourceAddress.Is4() != flow.TupleOrig.IP.SourceAddress.Is4() {
			return fmt.Errorf("conntrack update requires one address family")
		}
	}
	return nil
}

func validateTCPTuple(tuple conntrack.Tuple, direction string) error {
	if tuple.Proto.Protocol != unix.IPPROTO_TCP || tuple.Proto.ICMPv4 || tuple.Proto.ICMPv6 {
		return fmt.Errorf("%s conntrack tuple requires TCP", direction)
	}
	if tuple.Proto.SourcePort == 0 || tuple.Proto.DestinationPort == 0 {
		return fmt.Errorf("%s conntrack tuple requires both TCP ports", direction)
	}
	for _, address := range []netip.Addr{tuple.IP.SourceAddress, tuple.IP.DestinationAddress} {
		if !address.IsValid() || address.IsUnspecified() || address.Is4In6() || address.Zone() != "" {
			return fmt.Errorf("%s conntrack tuple requires unambiguous IP addresses", direction)
		}
	}
	if tuple.IP.SourceAddress.Is4() != tuple.IP.DestinationAddress.Is4() {
		return fmt.Errorf("%s conntrack tuple requires one address family", direction)
	}
	return nil
}

func conntrackIdentity(flow conntrack.Flow) (netfilter.ProtoFamily, []netfilter.Attribute) {
	family, tuple := conntrackTupleAttribute(flow.TupleOrig, ctTupleOriginal)
	return family, []netfilter.Attribute{
		tuple,
		{Type: ctID, Data: netfilter.Uint32Bytes(flow.ID)},
		{Type: ctZone, Data: netfilter.Uint16Bytes(flow.Zone)},
	}
}

func conntrackTupleAttribute(tuple conntrack.Tuple, kind uint16) (netfilter.ProtoFamily, netfilter.Attribute) {
	family := netfilter.ProtoIPv4
	source, destination := ctIPv4Source, ctIPv4Destination
	if tuple.IP.SourceAddress.Is6() {
		family = netfilter.ProtoIPv6
		source, destination = ctIPv6Source, ctIPv6Destination
	}
	children := []netfilter.Attribute{
		{Type: ctTupleIP, Nested: true, Children: []netfilter.Attribute{
			{Type: source, Data: tuple.IP.SourceAddress.AsSlice()},
			{Type: destination, Data: tuple.IP.DestinationAddress.AsSlice()},
		}},
		{Type: ctTupleProtocol, Nested: true, Children: []netfilter.Attribute{
			{Type: ctProtocolNumber, Data: []byte{tuple.Proto.Protocol}},
			{Type: ctSourcePort, Data: netfilter.Uint16Bytes(tuple.Proto.SourcePort)},
			{Type: ctDestinationPort, Data: netfilter.Uint16Bytes(tuple.Proto.DestinationPort)},
		}},
	}
	if tuple.Zone != 0 {
		children = append(children, netfilter.Attribute{Type: ctTupleZone, Data: netfilter.Uint16Bytes(tuple.Zone)})
	}
	return family, netfilter.Attribute{Type: kind, Nested: true, Children: children}
}

func (u *ConntrackUpdater) query(family netfilter.ProtoFamily, kind netfilter.MessageType, attributes []netfilter.Attribute) (messages []netlink.Message, resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.Warn("conntrack request failed", "type", kind, "err", resultErr)
		}
	}()
	message, err := netfilter.MarshalNetlink(netfilter.Header{
		SubsystemID: netfilter.NFSubsysCTNetlink, MessageType: kind,
		Family: family, Flags: netlink.Request | netlink.Acknowledge,
	}, attributes)
	if err != nil {
		return nil, fmt.Errorf("encode conntrack request: %w", err)
	}
	messages, err = readConntrackReplies(u.socket, message, kind == ctMessageGet)
	if err != nil {
		delegatedFailure = true
		return nil, fmt.Errorf("query conntrack socket: %w", err)
	}
	return messages, nil
}

func (u *ConntrackUpdater) readIdentity(family netfilter.ProtoFamily, attributes []netfilter.Attribute, expected conntrack.Flow) (flow conntrack.Flow, resultErr error) {
	delegatedFailure := false
	defer func() {
		if resultErr != nil && !delegatedFailure {
			slog.Warn("conntrack identity verification failed", "id", expected.ID, "zone", expected.Zone, "err", resultErr)
		}
	}()
	messages, err := u.query(family, ctMessageGet, attributes)
	if err != nil {
		delegatedFailure = true
		return conntrack.Flow{}, err
	}
	var current *conntrack.Flow
	for _, message := range messages {
		// Query validates the acknowledgment separately from the entry payload.
		if message.Header.Type == netlink.Error {
			continue
		}
		var event conntrack.Event
		if err := event.Unmarshal(message); err != nil {
			return conntrack.Flow{}, fmt.Errorf("decode conntrack lookup: %w", err)
		}
		if current != nil || event.Flow == nil {
			return conntrack.Flow{}, fmt.Errorf("conntrack lookup did not return one entry")
		}
		current = event.Flow
	}
	if current == nil || current.ID != expected.ID || current.Zone != expected.Zone || current.TupleOrig != expected.TupleOrig {
		return conntrack.Flow{}, fmt.Errorf("conntrack identity changed: %w", unix.ESTALE)
	}
	if expected.TupleReply.IP.SourceAddress.IsValid() && current.TupleReply != expected.TupleReply {
		return conntrack.Flow{}, fmt.Errorf("conntrack reply identity changed: %w", unix.ESTALE)
	}
	return *current, nil
}
