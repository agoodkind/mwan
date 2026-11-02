//go:build linux && cgo && nativeoffload

package offload_test

import (
	"testing"

	"goodkind.io/mwan/internal/offload"
)

func TestNativeKernelObservationBounds(t *testing.T) {
	for _, frames := range [][]offload.NativeKernelFrame{
		nil,
		make([]offload.NativeKernelFrame, 257),
		{{Frame: nil}},
		{{Frame: make([]byte, 65537)}},
	} {
		if observations, err := offload.ObserveNativeKernelFrames(frames); err == nil || observations != nil {
			t.Fatalf("kernel observation accepted invalid allocation bounds: count=%d observations=%+v error=%v", len(frames), observations, err)
		}
	}
}
