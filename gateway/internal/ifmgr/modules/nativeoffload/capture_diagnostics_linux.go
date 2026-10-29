//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/offload"
)

type captureStage uint8

const (
	captureReceived captureStage = iota
	captureSenderRejected
	captureInterfaceRejected
	captureOutgoingRejected
	captureSocketOverflow
	capturePaused
	captureChecksumPending
	captureVLAN
	captureDecoderIneligible
	captureQueued
	captureQueueFull
	captureStageCount
)

type captureSample struct {
	observedAt      time.Time
	observedAtValid bool
	sender          unix.SockaddrLinklayer
	senderValid     bool
	status          uint32
	statusValid     bool
	metadataError   error
	decoded         offload.NativeObservation
	decodedValid    bool
	decodeError     error
}

type captureDiagnostics struct {
	mu      sync.Mutex
	name    string
	ifindex int
	counts  [captureStageCount]uint64
	samples [captureStageCount]*captureSample
}

func (diagnostics *captureDiagnostics) count(stage captureStage, count uint64) {
	diagnostics.mu.Lock()
	diagnostics.counts[stage] += count
	diagnostics.mu.Unlock()
}

func (diagnostics *captureDiagnostics) record(stage captureStage, frame []byte, sender unix.Sockaddr, metadata captureMetadata, control []byte, decoded *offload.NativeObservation) {
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	diagnostics.counts[stage]++
	if diagnostics.samples[stage] != nil {
		return
	}
	sample := &captureSample{}
	if address, ok := sender.(*unix.SockaddrLinklayer); ok {
		sample.sender, sample.senderValid = *address, true
	}
	if control != nil {
		metadata, sample.metadataError = observationControl(control)
	}
	sample.status, sample.statusValid = metadata.status, metadata.auxiliaryValid
	sample.observedAt, sample.observedAtValid = metadata.at, sample.metadataError == nil && !metadata.at.IsZero()
	if decoded != nil {
		sample.decoded, sample.decodedValid = *decoded, true
	} else {
		observations, err := offload.ObserveNativeFrames([][]byte{frame})
		if err != nil {
			sample.decodeError = err
		} else {
			sample.decoded, sample.decodedValid = observations[0], true
		}
	}
	diagnostics.samples[stage] = sample
}

func (diagnostics *captureDiagnostics) snapshot() ([captureStageCount]uint64, [captureStageCount]*captureSample) {
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	counts, samples := diagnostics.counts, diagnostics.samples
	diagnostics.samples = [captureStageCount]*captureSample{}
	return counts, samples
}

func (sample *captureSample) logValue() slog.Value {
	attributes := []slog.Attr{
		slog.Bool("observed_at_valid", sample.observedAtValid),
		slog.Bool("sender_valid", sample.senderValid),
		slog.Bool("auxiliary_valid", sample.statusValid),
		slog.Bool("decoded_valid", sample.decodedValid),
	}
	if sample.observedAtValid {
		attributes = append(attributes, slog.Time("observed_at", sample.observedAt))
	}
	if sample.senderValid {
		attributes = append(attributes, slog.Int("sender_ifindex", sample.sender.Ifindex), slog.Uint64("sender_protocol", uint64(sample.sender.Protocol)), slog.Uint64("sender_hatype", uint64(sample.sender.Hatype)), slog.Uint64("sender_packet_type", uint64(sample.sender.Pkttype)), slog.Uint64("sender_address_length", uint64(sample.sender.Halen)), slog.Any("sender_address", sample.sender.Addr))
	}
	if sample.statusValid {
		attributes = append(attributes, slog.Uint64("auxiliary_status", uint64(sample.status)))
	}
	if sample.metadataError != nil {
		attributes = append(attributes, slog.String("metadata_error", sample.metadataError.Error()))
	}
	if sample.decodeError != nil {
		attributes = append(attributes, slog.String("decode_error", sample.decodeError.Error()))
	}
	if sample.decodedValid {
		observation := sample.decoded
		attributes = append(attributes, slog.Any("tuple", observation.Tuple), slog.Any("source_mac", observation.SourceMAC), slog.Any("destination_mac", observation.DestinationMAC), slog.Uint64("ip_length", uint64(observation.NetworkLength)), slog.Uint64("tcp_flags", uint64(observation.TCPFlags)), slog.Bool("eligible", observation.Eligible))
		if observation.DecodeError != nil {
			attributes = append(attributes, slog.String("packet_decode_error", observation.DecodeError.Error()))
		}
	}
	return slog.GroupValue(attributes...)
}

func (module *Module) logCaptureDiagnostics(ctx context.Context) {
	stages := [captureStageCount]string{"received", "sender_rejected", "interface_rejected", "outgoing_rejected", "socket_overflow", "paused", "checksum_pending", "vlan", "decoder_ineligible", "queued", "queue_full"}
	for _, capture := range module.captureSnapshot() {
		diagnostics := capture.diagnostics
		counts, samples := diagnostics.snapshot()
		attributes := make([]slog.Attr, 0, captureStageCount)
		for stage, count := range counts {
			attributes = append(attributes, slog.Uint64(stages[stage], count))
		}
		module.Log.InfoContext(ctx, "native capture diagnostics", "interface", diagnostics.name, "ifindex", diagnostics.ifindex, "counters", slog.GroupValue(attributes...))
		for stage, sample := range samples {
			if sample != nil {
				module.Log.InfoContext(ctx, "native capture diagnostic sample", "interface", diagnostics.name, "ifindex", diagnostics.ifindex, "stage", stages[stage], "sample", sample.logValue())
			}
		}
	}
}
