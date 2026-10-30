//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/bits"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/offload"
)

const (
	observationFrameBytes      = 65536
	observationControlBytes    = 128
	observationTimestampBytes  = 16
	observationCounterBytes    = 4
	observationAuxdataBytes    = 20
	observationChecksumPending = 0x08
	observationVLANPresent     = 0x10
)

type packetObservation struct {
	offload.NativeObservation
	ifindex int
	at      time.Time
}

type packetCapture struct {
	file          *os.File
	cancel        context.CancelFunc
	done          chan struct{}
	closeOnce     sync.Once
	closeErr      error
	paused        *atomic.Bool
	skipped       atomic.Uint64
	previousDrops uint32
	diagnostics   *captureDiagnostics
}

type captureMetadata struct {
	at             time.Time
	checksumReady  bool
	drops          uint32
	dropsValid     bool
	status         uint32
	auxiliaryValid bool
}

func openPacketCapture(ctx context.Context, link *net.Interface, socketBytes int, observations chan<- packetObservation, failures chan<- error, paused *atomic.Bool, diagnosticsEnabled bool) (result *packetCapture, resultErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		if resultErr != nil {
			cancel()
			slog.WarnContext(ctx, "native packet observation startup failed", "interface", link.Name, "err", resultErr)
		}
	}()
	descriptor, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, int(bits.ReverseBytes16(unix.ETH_P_ALL)))
	if err != nil {
		return nil, fmt.Errorf("open native packet observation socket: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), "native-observation-"+link.Name)
	capture := &packetCapture{
		file: file, cancel: cancel, done: make(chan struct{}),
		closeOnce: sync.Once{}, closeErr: nil, paused: paused,
		skipped: atomic.Uint64{}, previousDrops: 0, diagnostics: nil,
	}
	if diagnosticsEnabled {
		capture.diagnostics = new(captureDiagnostics)
		capture.diagnostics.name, capture.diagnostics.ifindex = link.Name, link.Index
	}
	for _, option := range []struct{ level, name, value int }{
		{unix.SOL_SOCKET, unix.SO_RCVBUF, socketBytes},
		{unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1},
		{unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1},
		{unix.SOL_PACKET, unix.PACKET_AUXDATA, 1},
		{unix.SOL_PACKET, unix.PACKET_IGNORE_OUTGOING, 1},
	} {
		if err := unix.SetsockoptInt(descriptor, option.level, option.name, option.value); err != nil {
			return nil, errors.Join(fmt.Errorf("configure native packet observations: %w", err), capture.close())
		}
	}
	if err := unix.Bind(descriptor, &unix.SockaddrLinklayer{Protocol: bits.ReverseBytes16(unix.ETH_P_ALL), Ifindex: link.Index}); err != nil {
		return nil, errors.Join(fmt.Errorf("bind native packet observations: %w", err), capture.close())
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect native packet observation socket: %w", err), capture.close())
	}
	stop := context.AfterFunc(ctx, func() { _ = capture.close() })
	go func() {
		defer close(capture.done)
		defer stop()
		defer func() {
			// Shutdown reports the retained close result.
			_ = capture.close()
		}()
		defer func() {
			if recovered := recover(); recovered != nil {
				var cause error
				switch value := recovered.(type) {
				case error:
					cause = value
				case string:
					cause = errors.New("panic value: " + value)
				default:
					cause = fmt.Errorf("panic value: %v", value)
				}
				failure := panicFailure("native packet observation", cause)
				if paused != nil {
					paused.Store(true)
				}
				slog.ErrorContext(ctx, "native packet observation panicked", "err", failure)
				select {
				case failures <- failure:
				case <-ctx.Done():
				}
			}
		}()
		if err := capture.receive(ctx, raw, link.Index, observations); err != nil && ctx.Err() == nil {
			select {
			case failures <- err:
			case <-ctx.Done():
			}
		}
	}()
	return capture, nil
}

func (capture *packetCapture) close() error {
	if capture.cancel != nil {
		capture.cancel()
	}
	capture.closeOnce.Do(func() { capture.closeErr = capture.file.Close() })
	return capture.closeErr
}

func (capture *packetCapture) receive(ctx context.Context, raw syscall.RawConn, ifindex int, observations chan<- packetObservation) (resultErr error) {
	defer func() {
		if resultErr != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "native packet observation receive failed", "ifindex", ifindex, "err", resultErr)
		}
	}()
	frame := make([]byte, observationFrameBytes)
	control := make([]byte, observationControlBytes)
	for {
		var count, controlCount, flags int
		var sender unix.Sockaddr
		var receiveErr error
		if err := raw.Read(func(descriptor uintptr) bool {
			count, controlCount, flags, sender, receiveErr = unix.Recvmsg(int(descriptor), frame, control, unix.MSG_DONTWAIT)
			return !errors.Is(receiveErr, unix.EAGAIN) && !errors.Is(receiveErr, unix.EINTR)
		}); err != nil {
			return fmt.Errorf("read native packet observations: %w", err)
		}
		if receiveErr != nil {
			return fmt.Errorf("receive native packet observations: %w", receiveErr)
		}
		if capture.diagnostics != nil {
			capture.diagnostics.count(captureReceived, 1)
		}
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || count == 0 {
			return fmt.Errorf("native packet observation truncated: %w", unix.EMSGSIZE)
		}
		keepReading, err := capture.observeFrame(ctx, ifindex, frame[:count], sender, control[:controlCount], observations)
		if err != nil {
			return err
		}
		if !keepReading {
			return nil
		}
	}
}

func (capture *packetCapture) senderAllowed(ifindex int, frame []byte, sender unix.Sockaddr, control []byte) bool {
	address, ok := sender.(*unix.SockaddrLinklayer)
	if ok && address.Ifindex == ifindex && address.Pkttype != unix.PACKET_OUTGOING {
		return true
	}
	if capture.diagnostics == nil {
		return false
	}
	stage := captureSenderRejected
	if ok && address.Ifindex != ifindex {
		stage = captureInterfaceRejected
	} else if ok && address.Pkttype == unix.PACKET_OUTGOING {
		stage = captureOutgoingRejected
	}
	var metadata captureMetadata
	capture.diagnostics.record(stage, frame, sender, metadata, control, nil)
	return false
}

func (capture *packetCapture) metadataAllowed(frame []byte, sender unix.Sockaddr, metadata captureMetadata) bool {
	if metadata.dropsValid {
		drops := uint64(metadata.drops - capture.previousDrops)
		capture.skipped.Add(drops)
		if capture.diagnostics != nil {
			capture.diagnostics.count(captureSocketOverflow, drops)
		}
		capture.previousDrops = metadata.drops
	}
	if capture.paused != nil && capture.paused.Load() {
		capture.skipped.Add(1)
		if capture.diagnostics != nil {
			capture.diagnostics.record(capturePaused, frame, sender, metadata, nil, nil)
		}
		return false
	}
	if metadata.checksumReady {
		return true
	}
	if capture.diagnostics == nil {
		return false
	}
	if metadata.status&observationChecksumPending != 0 {
		capture.diagnostics.record(captureChecksumPending, frame, sender, metadata, nil, nil)
	}
	if metadata.status&observationVLANPresent != 0 {
		capture.diagnostics.record(captureVLAN, frame, sender, metadata, nil, nil)
	}
	return false
}

func (capture *packetCapture) observeFrame(ctx context.Context, ifindex int, frame []byte, sender unix.Sockaddr, control []byte, observations chan<- packetObservation) (bool, error) {
	if !capture.senderAllowed(ifindex, frame, sender, control) {
		return true, nil
	}
	metadata, err := observationControl(control)
	if err != nil {
		return false, err
	}
	if !capture.metadataAllowed(frame, sender, metadata) {
		return true, nil
	}
	decoded, err := offload.ObserveNativeFrames([][]byte{frame})
	if err != nil {
		return false, &nativeOperationError{operation: "decode native packet observations", connectionID: 0, ifindex: ifindex, cause: err}
	}
	if !decoded[0].Eligible {
		if capture.diagnostics != nil {
			capture.diagnostics.record(captureDecoderIneligible, frame, sender, metadata, nil, &decoded[0])
		}
		return true, nil
	}
	observation := packetObservation{NativeObservation: decoded[0], ifindex: ifindex, at: metadata.at}
	select {
	case observations <- observation:
		if capture.diagnostics != nil {
			capture.diagnostics.record(captureQueued, frame, sender, metadata, nil, &decoded[0])
		}
	case <-ctx.Done():
		return false, nil
	default:
		capture.skipped.Add(1)
		if capture.diagnostics != nil {
			capture.diagnostics.record(captureQueueFull, frame, sender, metadata, nil, &decoded[0])
		}
	}
	return true, nil
}

func observationControl(data []byte) (metadata captureMetadata, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("native packet observation metadata is invalid", "err", resultErr)
		}
	}()
	messages, err := unix.ParseSocketControlMessage(data)
	if err != nil {
		return metadata, fmt.Errorf("decode native observation control: %w", err)
	}
	auxiliaryValid := false
	for _, message := range messages {
		switch {
		case message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SO_TIMESTAMPNS:
			if len(message.Data) != observationTimestampBytes {
				return metadata, fmt.Errorf("native observation timestamp has an invalid size")
			}
			seconds := binary.NativeEndian.Uint64(message.Data[:8])
			nanoseconds := binary.NativeEndian.Uint64(message.Data[8:])
			if seconds > math.MaxInt64 || nanoseconds >= uint64(time.Second) {
				return metadata, fmt.Errorf("native observation timestamp exceeds its clock range")
			}
			metadata.at = time.Unix(int64(seconds), int64(nanoseconds))
		case message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SO_RXQ_OVFL:
			if len(message.Data) != observationCounterBytes {
				return metadata, fmt.Errorf("native observation drop counter has an invalid size")
			}
			metadata.drops = binary.NativeEndian.Uint32(message.Data)
			metadata.dropsValid = true
		case message.Header.Level == unix.SOL_PACKET && message.Header.Type == unix.PACKET_AUXDATA:
			if len(message.Data) != observationAuxdataBytes {
				return metadata, fmt.Errorf("native packet observation auxiliary data has an invalid size")
			}
			status := binary.NativeEndian.Uint32(message.Data[:4])
			metadata.status = status
			metadata.auxiliaryValid = true
			metadata.checksumReady = status&(observationChecksumPending|observationVLANPresent) == 0
			auxiliaryValid = true
		}
	}
	if metadata.at.IsZero() || metadata.at.After(clock.Real{}.Now()) {
		return metadata, fmt.Errorf("native packet observation does not include a valid kernel timestamp")
	}
	if !auxiliaryValid {
		return metadata, fmt.Errorf("native packet observation does not include required auxiliary data")
	}
	return metadata, nil
}
