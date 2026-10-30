//go:build linux && cgo && nativeoffload

package offload

import "testing"

func TestNativeKernelObservationBounds(t *testing.T) {
	for _, frames := range [][]NativeKernelFrame{
		nil,
		make([]NativeKernelFrame, 257),
		{{Frame: nil}},
		{{Frame: make([]byte, 65537)}},
	} {
		if observations, err := ObserveNativeKernelFrames(frames); err == nil || observations != nil {
			t.Fatalf("kernel observation accepted invalid allocation bounds: count=%d observations=%+v error=%v", len(frames), observations, err)
		}
	}
}
