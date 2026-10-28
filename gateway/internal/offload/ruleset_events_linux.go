//go:build linux

package offload

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
)

const rulesetHeaderBytes = 4

type rulesetRequest struct {
	sequence uint32
	reply    chan uint64
}

// RulesetEvents retains notification loss and supplies checkpoints on its own
// socket. A checkpoint drains earlier notifications before policy is certified.
type RulesetEvents struct {
	Changes <-chan struct{}
	Errors  <-chan error

	socket     *netlink.Conn
	generation atomic.Uint64
	sequence   atomic.Uint32
	requests   chan struct{}
	replyMu    sync.Mutex
	pending    *rulesetRequest
	failureMu  sync.RWMutex
	failure    error
	done       chan struct{}
	stopping   chan struct{}
	closeOnce  sync.Once
	closeErr   error
}

// NewRulesetEvents subscribes in the constructor caller's network namespace.
func NewRulesetEvents(ctx context.Context, socketBytes int) (result *RulesetEvents, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "start nftables observations failed", "err", resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if socketBytes <= 0 {
		return nil, errors.New("nftables observations require a positive socket capacity")
	}
	socket, err := netlink.Dial(unix.NETLINK_NETFILTER, nil)
	if err != nil {
		return nil, fmt.Errorf("open nftables observation socket: %w", err)
	}
	if err := socket.SetReadBuffer(socketBytes); err != nil {
		return nil, errors.Join(fmt.Errorf("set nftables observation capacity: %w", err), socket.Close())
	}
	if err := socket.JoinGroup(unix.NFNLGRP_NFTABLES); err != nil {
		return nil, errors.Join(fmt.Errorf("subscribe nftables changes: %w", err), socket.Close())
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect nftables observation socket: %w", err), socket.Close())
	}
	changes := make(chan struct{}, 1)
	failures := make(chan error, 1)
	source := &RulesetEvents{
		Changes: changes, Errors: failures, socket: socket,
		generation: atomic.Uint64{}, sequence: atomic.Uint32{}, requests: make(chan struct{}, 1),
		replyMu: sync.Mutex{}, pending: nil, failureMu: sync.RWMutex{}, failure: nil,
		done: make(chan struct{}), stopping: make(chan struct{}), closeOnce: sync.Once{}, closeErr: nil,
	}
	stop := context.AfterFunc(ctx, source.closeSocket)
	go func() {
		defer close(source.done)
		defer close(changes)
		defer close(failures)
		defer stop()
		defer source.closeSocket()
		var receiveErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				receiveErr = fmt.Errorf("nftables observation receiver panicked: %v", recovered)
			}
			if receiveErr != nil {
				failure := &ForwardingEventLossError{Cause: receiveErr}
				source.failureMu.Lock()
				source.failure = failure
				source.failureMu.Unlock()
				source.changed(changes)
				failures <- failure
			}
		}()
		receiveErr = source.receive(raw, changes)
	}()
	return source, nil
}

// Generation counts complete nftables transaction notifications.
func (source *RulesetEvents) Generation() uint64 { return source.generation.Load() }

// Err preserves an observation gap after channel closure.
func (source *RulesetEvents) Err() error {
	source.failureMu.RLock()
	defer source.failureMu.RUnlock()
	return source.failure
}

func (source *RulesetEvents) closeSocket() {
	source.closeOnce.Do(func() {
		close(source.stopping)
		source.closeErr = source.socket.Close()
	})
}

// Close interrupts the receiver and waits for final error publication.
func (source *RulesetEvents) Close() error {
	source.closeSocket()
	<-source.done
	return source.closeErr
}

// Barrier waits for a generation reply on the subscribed socket. The caller
// supplies a deadline so a blocked send cannot outlive policy certification.
func (source *RulesetEvents) Barrier(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, present := ctx.Deadline(); !present {
		return 0, errors.New("nftables generation checkpoint requires a context deadline")
	}
	select {
	case source.requests <- struct{}{}:
		defer func() { <-source.requests }()
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-source.done:
		return 0, source.closedError()
	}
	if err := source.Err(); err != nil {
		return 0, err
	}
	sequence := source.sequence.Add(1)
	if sequence == 0 {
		sequence = source.sequence.Add(1)
	}
	request := &rulesetRequest{sequence: sequence, reply: make(chan uint64, 1)}
	source.replyMu.Lock()
	source.pending = request
	source.replyMu.Unlock()
	defer source.clearRequest(request)
	if err := source.sendBarrier(ctx, sequence); err != nil {
		return 0, err
	}
	select {
	case reply := <-request.reply:
		return reply, source.Err()
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-source.done:
		return 0, source.closedError()
	}
}

func (source *RulesetEvents) closedError() error {
	if err := source.Err(); err != nil {
		return err
	}
	return net.ErrClosed
}

func (source *RulesetEvents) clearRequest(request *rulesetRequest) {
	source.replyMu.Lock()
	defer source.replyMu.Unlock()
	if source.pending == request {
		source.pending = nil
	}
}

func (source *RulesetEvents) sendBarrier(ctx context.Context, sequence uint32) error {
	deadline, _ := ctx.Deadline()
	if err := source.socket.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("bound nftables generation request: %w", err)
	}
	interrupted := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() {
		interrupted <- source.socket.SetWriteDeadline(clock.Real{}.Now())
	})
	_, sendErr := source.socket.Send(netlink.Message{
		Header: netlink.Header{Type: unix.NFNL_SUBSYS_NFTABLES<<8 | unix.NFT_MSG_GETGEN, Flags: netlink.Request, Sequence: sequence},
		Data:   make([]byte, rulesetHeaderBytes),
	})
	var interruptErr error
	if !stop() {
		interruptErr = <-interrupted
	}
	resetErr := source.socket.SetWriteDeadline(time.Time{})
	if err := errors.Join(sendErr, interruptErr, resetErr); err != nil {
		return fmt.Errorf("request nftables generation: %w", err)
	}
	return ctx.Err()
}

func (source *RulesetEvents) changed(changes chan<- struct{}) {
	select {
	case changes <- struct{}{}:
	default:
	}
}

func (source *RulesetEvents) receive(raw syscall.RawConn, changes chan<- struct{}) error {
	buffer := make([]byte, conntrackReceiveBytes)
	for {
		count, err := readNetlinkDatagram(raw, buffer)
		if err != nil {
			select {
			case <-source.stopping:
				return nil
			default:
				return fmt.Errorf("receive nftables observations: %w", err)
			}
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:count])
		if err != nil {
			return fmt.Errorf("decode nftables observation datagram: %w", err)
		}
		for _, message := range messages {
			if err := source.message(message, changes); err != nil {
				return err
			}
		}
	}
}

func (source *RulesetEvents) message(message syscall.NetlinkMessage, changes chan<- struct{}) error {
	switch message.Header.Type {
	case unix.NLMSG_NOOP, unix.NFNL_MSG_BATCH_BEGIN, unix.NFNL_MSG_BATCH_END:
		return nil
	case unix.NLMSG_OVERRUN:
		return fmt.Errorf("nftables observation overrun: %w", unix.ENOBUFS)
	case unix.NLMSG_ERROR:
		errno, err := unmarshalNetlinkErrorCode(message.Data)
		if err != nil {
			return err
		}
		if errno == 0 {
			return errors.New("unexpected nftables observation acknowledgment")
		}
		return fmt.Errorf("nftables observation request failed: %w", errno)
	}
	if message.Header.Type>>8 != unix.NFNL_SUBSYS_NFTABLES || len(message.Data) < rulesetHeaderBytes {
		return fmt.Errorf("invalid nftables observation type %d or header length %d", message.Header.Type, len(message.Data))
	}
	attributes, err := netlink.NewAttributeDecoder(message.Data[rulesetHeaderBytes:])
	if err != nil {
		return fmt.Errorf("decode nftables observation attributes: %w", err)
	}
	attributes.ByteOrder = binary.BigEndian
	generation := false
	for attributes.Next() {
		if message.Header.Type&0xff == unix.NFT_MSG_NEWGEN && attributes.Type() == unix.NFTA_GEN_ID {
			if generation {
				return errors.New("nftables generation message repeated its generation ID")
			}
			_ = attributes.Uint32()
			generation = true
		}
	}
	if err := attributes.Err(); err != nil {
		return fmt.Errorf("decode nftables observation attribute value: %w", err)
	}
	if message.Header.Type&0xff != unix.NFT_MSG_NEWGEN {
		return nil
	}
	if !generation {
		return errors.New("nftables generation message omitted its generation ID")
	}
	if message.Header.Seq == 0 {
		source.generation.Add(1)
		source.changed(changes)
		return nil
	}
	source.replyMu.Lock()
	defer source.replyMu.Unlock()
	if source.pending != nil && source.pending.sequence == message.Header.Seq {
		source.pending.reply <- source.Generation()
		source.pending = nil
	}
	return nil
}
