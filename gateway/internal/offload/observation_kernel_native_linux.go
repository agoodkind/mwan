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
	"syscall"
	"unsafe"
)

// NativeKernelMetadata requires the auxiliary data and virtual network header
// from the same receive on the bound AF_PACKET socket. Packet bytes alone do
// not establish this provenance.
type NativeKernelMetadata struct {
	AuxiliaryStatus uint32
	PacketLength    uint32
	CapturedLength  uint32
	MACOffset       uint16
	NetworkOffset   uint16
	VnetFlags       uint8
	GSOType         uint8
	HeaderLength    uint16
	GSOSize         uint16
	ChecksumStart   uint16
	ChecksumOffset  uint16
	Reserved16      uint16
	Reserved32      uint32
}

// NativeKernelFrame pairs Ethernet bytes with metadata from the same kernel capture.
type NativeKernelFrame struct {
	Frame    []byte
	Metadata NativeKernelMetadata
}

// NativeKernelObservation includes the maximum segment IP length for admission.
type NativeKernelObservation struct {
	NativeObservation
	EffectiveNetworkLength uint32
}

// ObserveNativeKernelFrames accepts metadata only from the trusted socket
// capture that supplied each Ethernet frame. Rust retains neither input.
func ObserveNativeKernelFrames(frames []NativeKernelFrame) ([]NativeKernelObservation, error) {
	if C.sizeof_struct_mwan_native_kernel_metadata != 32 || C.sizeof_struct_mwan_native_kernel_frame != 48 || C.sizeof_struct_mwan_native_kernel_observation != 76 {
		return nil, fmt.Errorf("native kernel observation ABI layout is incompatible")
	}
	if len(frames) == 0 || len(frames) > C.MWAN_NATIVE_OBSERVATION_BATCH_LIMIT {
		return nil, fmt.Errorf("native kernel observation batch requires between 1 and %d frames", C.MWAN_NATIVE_OBSERVATION_BATCH_LIMIT)
	}
	bytes := make([][]byte, len(frames))
	for index, frame := range frames {
		bytes[index] = frame.Frame
	}
	payload, copied, err := copyNativeObservationFrames(bytes)
	if err != nil {
		return nil, err
	}
	defer C.free(payload)
	var packet NativePacketDiagnostic
	descriptors := C.calloc(C.size_t(len(frames)), C.sizeof_struct_mwan_native_kernel_frame)
	if descriptors == nil {
		return nil, &NativeError{
			Code: syscall.ENOMEM, Operation: C.MWAN_NATIVE_OP_PACKET, ObjectKind: NativeObjectNone,
			ObjectID: 0, Direction: 0, FlowErrorType: 0, Message: "allocate native kernel descriptors", Packet: packet,
		}
	}
	defer C.free(descriptors)
	inputs := unsafe.Slice((*C.struct_mwan_native_kernel_frame)(descriptors), len(frames))
	for index, frame := range frames {
		inputs[index].frame = copied[index]
		inputs[index].metadata = encodeNativeKernelMetadata(frame.Metadata)
	}
	observations := make([]C.struct_mwan_native_kernel_observation, len(frames))
	if result := C.mwan_native_observe_kernel_batch((*C.struct_mwan_native_kernel_frame)(descriptors), &observations[0], C.uint32_t(len(frames))); result != 0 {
		return nil, &NativeError{
			Code: syscall.Errno(-result), Operation: C.MWAN_NATIVE_OP_PACKET, ObjectKind: NativeObjectNone,
			ObjectID: 0, Direction: 0, FlowErrorType: 0, Message: "decode native kernel observations", Packet: packet,
		}
	}
	decoded := make([]NativeKernelObservation, len(frames))
	for index, observation := range observations {
		decoded[index] = NativeKernelObservation{
			NativeObservation:      decodeNativeObservation(observation.observation),
			EffectiveNetworkLength: uint32(observation.effective_network_length),
		}
	}
	return decoded, nil
}

func encodeNativeKernelMetadata(metadata NativeKernelMetadata) C.struct_mwan_native_kernel_metadata {
	return C.struct_mwan_native_kernel_metadata{
		auxiliary_status: C.uint32_t(metadata.AuxiliaryStatus),
		packet_length:    C.uint32_t(metadata.PacketLength),
		captured_length:  C.uint32_t(metadata.CapturedLength),
		mac_offset:       C.uint16_t(metadata.MACOffset),
		network_offset:   C.uint16_t(metadata.NetworkOffset),
		vnet_flags:       C.uint8_t(metadata.VnetFlags),
		gso_type:         C.uint8_t(metadata.GSOType),
		header_length:    C.uint16_t(metadata.HeaderLength),
		gso_size:         C.uint16_t(metadata.GSOSize),
		checksum_start:   C.uint16_t(metadata.ChecksumStart),
		checksum_offset:  C.uint16_t(metadata.ChecksumOffset),
		reserved16:       C.uint16_t(metadata.Reserved16), reserved32: C.uint32_t(metadata.Reserved32),
	}
}
