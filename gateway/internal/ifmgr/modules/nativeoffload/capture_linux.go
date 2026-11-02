//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/offload"
)

const (
	observationFrameBytes   = 65536
	observationControlBytes = 128
	observationVnetBytes    = 10
)

type packetObservation struct {
	offload.NativeObservation
	EffectiveNetworkLength uint32
	ifindex                int
	at                     time.Time
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
	kernel         offload.NativeKernelMetadata
	at             time.Time
	drops          uint32
	dropsValid     bool
	auxiliaryValid bool
	vnetValid      bool
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
	if err := configureObservationSocket(descriptor, socketBytes); err != nil {
		return nil, errors.Join(err, capture.close())
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
	frame := make([]byte, observationFrameBytes+observationVnetBytes)
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
	if ok && address.Ifindex == ifindex && address.Pkttype == unix.PACKET_HOST && address.Hatype == unix.ARPHRD_ETHER && address.Halen == 6 {
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
	return true
}

func (capture *packetCapture) observeFrame(ctx context.Context, ifindex int, frame []byte, sender unix.Sockaddr, control []byte, observations chan<- packetObservation) (bool, error) {
	if len(frame) <= observationVnetBytes || len(frame) > observationFrameBytes+observationVnetBytes {
		return false, &nativeOperationError{operation: "read native virtual network header", connectionID: 0, ifindex: ifindex, cause: unix.EMSGSIZE}
	}
	prefix := frame[:observationVnetBytes]
	frame = frame[observationVnetBytes:]
	if !capture.senderAllowed(ifindex, frame, sender, control) {
		return true, nil
	}
	metadata, err := observationControl(control)
	if err != nil {
		return false, &nativeOperationError{operation: "read native observation metadata", connectionID: 0, ifindex: ifindex, cause: err}
	}
	metadata.setVnet(prefix)
	if !capture.metadataAllowed(frame, sender, metadata) {
		return true, nil
	}
	decoded, err := offload.ObserveNativeKernelFrames([]offload.NativeKernelFrame{{Frame: frame, Metadata: metadata.kernel}})
	if err != nil {
		return false, &nativeOperationError{operation: "decode native packet observations", connectionID: 0, ifindex: ifindex, cause: err}
	}
	if !decoded[0].Eligible {
		if capture.diagnostics != nil {
			capture.diagnostics.record(captureDecoderIneligible, frame, sender, metadata, nil, &decoded[0])
		}
		return true, nil
	}
	observation := packetObservation{NativeObservation: decoded[0].NativeObservation, EffectiveNetworkLength: decoded[0].EffectiveNetworkLength, ifindex: ifindex, at: metadata.at}
	if capture.diagnostics != nil && observation.NetworkLength > observation.EffectiveNetworkLength {
		capture.diagnostics.record(captureKernelAggregate, frame, sender, metadata, nil, &decoded[0])
	}
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
