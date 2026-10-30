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
	captureDecoderIneligible
	captureQueued
	captureQueueFull
	captureKernelAggregate
	captureStageCount
)

type captureSample struct {
	observedAt      time.Time
	observedAtValid bool
	sender          unix.SockaddrLinklayer
	senderValid     bool
	kernel          offload.NativeKernelMetadata
	vnetValid       bool
	statusValid     bool
	metadataError   error
	decoded         offload.NativeKernelObservation
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

func (diagnostics *captureDiagnostics) record(stage captureStage, frame []byte, sender unix.Sockaddr, metadata captureMetadata, control []byte, decoded *offload.NativeKernelObservation) {
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	diagnostics.counts[stage]++
	if diagnostics.samples[stage] != nil {
		return
	}
	sample := new(captureSample)
	if address, ok := sender.(*unix.SockaddrLinklayer); ok {
		sample.sender, sample.senderValid = *address, true
	}
	if control != nil {
		metadata, sample.metadataError = observationControl(control)
	}
	sample.kernel, sample.statusValid, sample.vnetValid = metadata.kernel, metadata.auxiliaryValid, metadata.vnetValid
	sample.observedAt, sample.observedAtValid = metadata.at, sample.metadataError == nil && !metadata.at.IsZero()
	if decoded != nil {
		sample.decoded, sample.decodedValid = *decoded, true
	} else if stage >= capturePaused && metadata.vnetValid {
		observations, err := offload.ObserveNativeKernelFrames([]offload.NativeKernelFrame{{Frame: frame, Metadata: metadata.kernel}})
		if err != nil {
			sample.decodeError = err
		} else {
			sample.decoded, sample.decodedValid = observations[0], true
		}
	} else {
		observations, err := offload.ObserveNativeFrames([][]byte{frame})
		if err != nil {
			sample.decodeError = err
		} else {
			sample.decoded.NativeObservation, sample.decodedValid = observations[0], true
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
		attributes = append(attributes, slog.Uint64("auxiliary_status", uint64(sample.kernel.AuxiliaryStatus)), slog.Uint64("packet_length", uint64(sample.kernel.PacketLength)), slog.Uint64("captured_length", uint64(sample.kernel.CapturedLength)), slog.Uint64("mac_offset", uint64(sample.kernel.MACOffset)), slog.Uint64("network_offset", uint64(sample.kernel.NetworkOffset)))
	}
	if sample.vnetValid {
		attributes = append(attributes, slog.Uint64("vnet_flags", uint64(sample.kernel.VnetFlags)), slog.Uint64("gso_type", uint64(sample.kernel.GSOType)), slog.Uint64("header_length", uint64(sample.kernel.HeaderLength)), slog.Uint64("gso_size", uint64(sample.kernel.GSOSize)), slog.Uint64("checksum_start", uint64(sample.kernel.ChecksumStart)), slog.Uint64("checksum_offset", uint64(sample.kernel.ChecksumOffset)))
	}
	if sample.metadataError != nil {
		attributes = append(attributes, slog.String("metadata_error", sample.metadataError.Error()))
	}
	if sample.decodeError != nil {
		attributes = append(attributes, slog.String("decode_error", sample.decodeError.Error()))
	}
	if sample.decodedValid {
		observation := sample.decoded
		attributes = append(attributes, slog.Uint64("effective_ip_length", uint64(observation.EffectiveNetworkLength)))
		attributes = append(attributes, slog.Any("tuple", observation.Tuple), slog.Any("source_mac", observation.SourceMAC), slog.Any("destination_mac", observation.DestinationMAC), slog.Uint64("ip_length", uint64(observation.NetworkLength)), slog.Uint64("tcp_flags", uint64(observation.TCPFlags)), slog.Bool("eligible", observation.Eligible))
		if observation.DecodeError != nil {
			attributes = append(attributes, slog.String("packet_decode_error", observation.DecodeError.Error()))
		}
	}
	return slog.GroupValue(attributes...)
}

func (module *Module) logCaptureDiagnostics(ctx context.Context) {
	stages := [captureStageCount]string{"received", "sender_rejected", "interface_rejected", "outgoing_rejected", "socket_overflow", "paused", "decoder_ineligible", "queued", "queue_full", "kernel_aggregate"}
	captures := module.captureSnapshot()
	interfaces := make([]slog.Attr, 0, len(captures))
	for _, capture := range captures {
		diagnostics := capture.diagnostics
		counts, samples := diagnostics.snapshot()
		attributes := make([]slog.Attr, 0, captureStageCount)
		for stage, count := range counts {
			attributes = append(attributes, slog.Uint64(stages[stage], count))
		}
		sampleAttributes := make([]slog.Attr, 0, captureStageCount)
		for stage, sample := range samples {
			if sample != nil {
				sampleAttributes = append(sampleAttributes, slog.Attr{Key: stages[stage], Value: sample.logValue()})
			}
		}
		value := slog.GroupValue(slog.Int("ifindex", diagnostics.ifindex), slog.Attr{Key: "counters", Value: slog.GroupValue(attributes...)}, slog.Attr{Key: "samples", Value: slog.GroupValue(sampleAttributes...)})
		interfaces = append(interfaces, slog.Attr{Key: diagnostics.name, Value: value})
	}
	if len(interfaces) != 0 {
		module.Log.InfoContext(ctx, "native capture diagnostics", "interfaces", slog.GroupValue(interfaces...))
	}
}
