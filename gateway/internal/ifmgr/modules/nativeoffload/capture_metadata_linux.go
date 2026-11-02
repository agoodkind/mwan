//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
)

const (
	observationTimestampBytes       = 16
	observationCounterBytes         = 4
	observationAuxdataBytes         = 20
	observationVnetHeaderSizeOption = 24
)

func configureObservationSocket(descriptor, socketBytes int) error {
	// Legacy virtio fields use native byte order on these little-endian targets.
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("native kernel capture requires Linux amd64 or arm64")
	}
	for _, option := range []struct{ level, name, value int }{
		{unix.SOL_SOCKET, unix.SO_RCVBUF, socketBytes},
		{unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1},
		{unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1},
		{unix.SOL_PACKET, unix.PACKET_AUXDATA, 1},
		{unix.SOL_PACKET, unix.PACKET_IGNORE_OUTGOING, 1},
		{unix.SOL_PACKET, unix.PACKET_VNET_HDR, 1},
	} {
		if err := unix.SetsockoptInt(descriptor, option.level, option.name, option.value); err != nil {
			return &nativeOperationError{operation: fmt.Sprintf("configure native packet socket option %d/%d", option.level, option.name), connectionID: 0, ifindex: 0, cause: err}
		}
	}
	size, err := unix.GetsockoptInt(descriptor, unix.SOL_PACKET, observationVnetHeaderSizeOption)
	if err != nil {
		return &nativeOperationError{operation: "read native packet virtual header size", connectionID: 0, ifindex: 0, cause: err}
	}
	if size != observationVnetBytes {
		return fmt.Errorf("native packet virtual header size is %d, expected %d", size, observationVnetBytes)
	}
	return nil
}

func (metadata *captureMetadata) setVnet(prefix []byte) {
	metadata.kernel.VnetFlags = prefix[0]
	metadata.kernel.GSOType = prefix[1]
	metadata.kernel.HeaderLength = binary.LittleEndian.Uint16(prefix[2:4])
	metadata.kernel.GSOSize = binary.LittleEndian.Uint16(prefix[4:6])
	metadata.kernel.ChecksumStart = binary.LittleEndian.Uint16(prefix[6:8])
	metadata.kernel.ChecksumOffset = binary.LittleEndian.Uint16(prefix[8:10])
	metadata.vnetValid = true
}

func observationControl(data []byte) (captureMetadata, error) {
	var metadata captureMetadata
	messages, err := unix.ParseSocketControlMessage(data)
	if err != nil {
		return metadata, &nativeOperationError{operation: "decode native observation control", connectionID: 0, ifindex: 0, cause: err}
	}
	for _, message := range messages {
		if err := metadata.readControl(message); err != nil {
			return metadata, err
		}
	}
	controlBytes := 0
	for _, message := range messages {
		controlBytes += unix.CmsgSpace(len(message.Data))
	}
	if controlBytes != len(data) {
		return metadata, fmt.Errorf("native observation control contains incomplete message padding")
	}
	if metadata.at.IsZero() || metadata.at.After(clock.Real{}.Now()) {
		return metadata, fmt.Errorf("native packet observation does not include a valid kernel timestamp")
	}
	if !metadata.auxiliaryValid {
		return metadata, fmt.Errorf("native packet observation does not include required auxiliary data")
	}
	return metadata, nil
}

func (metadata *captureMetadata) readControl(message unix.SocketControlMessage) error {
	switch {
	case message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SO_TIMESTAMPNS:
		if len(message.Data) != observationTimestampBytes || !metadata.at.IsZero() {
			return fmt.Errorf("native observation timestamp has an invalid size or occurs twice")
		}
		seconds := binary.NativeEndian.Uint64(message.Data[:8])
		nanoseconds := binary.NativeEndian.Uint64(message.Data[8:])
		if seconds == 0 || seconds > math.MaxInt64 || nanoseconds >= uint64(time.Second) {
			return fmt.Errorf("native observation timestamp exceeds its clock range")
		}
		metadata.at = time.Unix(int64(seconds), int64(nanoseconds))
	case message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SO_RXQ_OVFL:
		if len(message.Data) != observationCounterBytes || metadata.dropsValid {
			return fmt.Errorf("native observation drop counter has an invalid size or occurs twice")
		}
		metadata.drops = binary.NativeEndian.Uint32(message.Data)
		metadata.dropsValid = true
	case message.Header.Level == unix.SOL_PACKET && message.Header.Type == unix.PACKET_AUXDATA:
		if len(message.Data) != observationAuxdataBytes || metadata.auxiliaryValid {
			return fmt.Errorf("native observation auxiliary data has an invalid size or occurs twice")
		}
		metadata.kernel.AuxiliaryStatus = binary.NativeEndian.Uint32(message.Data[:4])
		metadata.kernel.PacketLength = binary.NativeEndian.Uint32(message.Data[4:8])
		metadata.kernel.CapturedLength = binary.NativeEndian.Uint32(message.Data[8:12])
		metadata.kernel.MACOffset = binary.NativeEndian.Uint16(message.Data[12:14])
		metadata.kernel.NetworkOffset = binary.NativeEndian.Uint16(message.Data[14:16])
		metadata.auxiliaryValid = true
	}
	return nil
}
