package agent

import (
	"strings"
	"testing"

	"context"
	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
)

func TestGetSystemInfo_LiveHost(t *testing.T) {
	t.Parallel()
	srv := NewServer("/x", testLogger(t), nil, nil)
	cli := startTestServer(t, srv)
	res, err := cli.GetSystemInfo(context.Background(), &mwanv1.GetSystemInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.GetHostname()) == "" {
		t.Fatal("empty hostname")
	}
	if res.GetUptimeSeconds() <= 0 {
		t.Fatalf("uptime_seconds=%d want >0", res.GetUptimeSeconds())
	}
	if strings.TrimSpace(res.GetLoadAverage()) == "" {
		t.Fatal("empty load_average")
	}
	if res.GetMemoryTotalBytes() <= 0 || res.GetMemoryUsedBytes() < 0 {
		t.Fatalf("memory total=%d used=%d", res.GetMemoryTotalBytes(), res.GetMemoryUsedBytes())
	}
	if res.GetDiskTotalBytes() <= 0 || res.GetDiskUsedBytes() < 0 {
		t.Fatalf("disk total=%d used=%d", res.GetDiskTotalBytes(), res.GetDiskUsedBytes())
	}
}
