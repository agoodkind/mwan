//go:build linux && cgo && nativeoffload

package offload

/*
#cgo pkg-config: mwan-native
#include <stdlib.h>
#include <mwan_native.h>
*/
import "C"

import (
	"fmt"
	"log/slog"
	"syscall"
	"unsafe"

	"github.com/ti-mo/conntrack"
)

// NativeObservation contains only packet fields decoded by the Rust worker.
type NativeObservation struct {
	Tuple          conntrack.Tuple
	SourceMAC      [6]byte
	DestinationMAC [6]byte
	NetworkLength  uint32
	FlowLabel      uint32
	TrafficClass   uint8
	TCPFlags       uint8
	TTL            uint8
	Eligible       bool
	DecodeError    error
}

// ObserveNativeFrames copies bounded Ethernet frames for Rust header validation.
func ObserveNativeFrames(frames [][]byte) (result []NativeObservation, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("native packet observation failed", "frames", len(frames), "err", resultErr)
		}
	}()
	payload, inputs, err := copyNativeObservationFrames(frames)
	if err != nil {
		return nil, err
	}
	defer C.free(payload)
	descriptors := C.calloc(C.size_t(len(frames)), C.sizeof_struct_mwan_native_frame)
	if descriptors == nil {
		return nil, syscall.ENOMEM
	}
	defer C.free(descriptors)
	copy(unsafe.Slice((*C.struct_mwan_native_frame)(descriptors), len(frames)), inputs)
	observations := make([]C.struct_mwan_native_observation, len(frames))
	if result := C.mwan_native_observe_batch((*C.struct_mwan_native_frame)(descriptors), &observations[0], C.uint32_t(len(frames))); result != 0 {
		return nil, fmt.Errorf("decode native observations: %w", syscall.Errno(-result))
	}
	decoded := make([]NativeObservation, len(frames))
	for index, observation := range observations {
		decoded[index] = decodeNativeObservation(observation)
	}
	return decoded, nil
}

func copyNativeObservationFrames(frames [][]byte) (unsafe.Pointer, []C.struct_mwan_native_frame, error) {
	if len(frames) == 0 || len(frames) > C.MWAN_NATIVE_OBSERVATION_BATCH_LIMIT {
		return nil, nil, fmt.Errorf("native observation batch requires between 1 and %d frames", C.MWAN_NATIVE_OBSERVATION_BATCH_LIMIT)
	}
	totalBytes := 0
	for _, frame := range frames {
		if len(frame) == 0 || len(frame) > C.MWAN_NATIVE_OBSERVATION_FRAME_LIMIT {
			return nil, nil, fmt.Errorf("native observation frame requires between 1 and %d bytes", C.MWAN_NATIVE_OBSERVATION_FRAME_LIMIT)
		}
		totalBytes += len(frame)
	}
	payload := C.malloc(C.size_t(totalBytes))
	if payload == nil {
		return nil, nil, syscall.ENOMEM
	}
	inputs := make([]C.struct_mwan_native_frame, len(frames))
	storage := unsafe.Slice((*byte)(payload), totalBytes)
	offset := 0
	for index, frame := range frames {
		copy(storage[offset:], frame)
		inputs[index].data = (*C.uint8_t)(unsafe.Add(payload, offset))
		inputs[index].length = C.uint32_t(len(frame))
		offset += len(frame)
	}
	return payload, inputs, nil
}

func decodeNativeObservation(observation C.struct_mwan_native_observation) NativeObservation {
	var decoded NativeObservation
	var diagnostic C.struct_mwan_native_packet_diagnostic
	diagnostic.validity = C.MWAN_NATIVE_PACKET_ETHERNET_VALID | C.MWAN_NATIVE_PACKET_IP_VALID | C.MWAN_NATIVE_PACKET_PORTS_VALID
	diagnostic.tuple = observation.tuple
	decoded.Tuple = decodeNativePacket(diagnostic).Tuple
	for octet := range decoded.SourceMAC {
		decoded.SourceMAC[octet] = byte(observation.source_mac[octet])
		decoded.DestinationMAC[octet] = byte(observation.destination_mac[octet])
	}
	decoded.NetworkLength = uint32(observation.network_length)
	decoded.FlowLabel = uint32(observation.flow_label)
	decoded.TrafficClass = uint8(observation.traffic_class)
	decoded.TCPFlags = uint8(observation.tcp_flags)
	decoded.TTL = uint8(observation.ttl)
	decoded.Eligible = observation.eligible != 0
	if observation.decode_status != 0 {
		decoded.DecodeError = syscall.Errno(-observation.decode_status)
	}
	return decoded
}
