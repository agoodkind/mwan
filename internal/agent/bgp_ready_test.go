package agent

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/forwardingready"
)

func TestPrimaryBGPAnnouncesOnlyReadyFamilies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ipv4Ready atomic.Bool
	var ipv6Ready atomic.Bool
	socketPath := filepath.Join(t.TempDir(), "ready.sock")
	readTimeout := 500 * time.Millisecond
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- forwardingready.Serve(ctx, socketPath, readTimeout, func() forwardingready.State {
			return forwardingready.State{IPv4: ipv4Ready.Load(), IPv6: ipv6Ready.Load()}
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-serveDone; err != nil {
			t.Errorf("readiness server: %v", err)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := forwardingready.Read(ctx, socketPath, readTimeout); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness socket did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	speaker := bgp.New(bgp.Config{
		Enabled:      true,
		RequireReady: true,
		ASN:          65001,
		RouterID:     "10.0.0.3",
		NextHopV6:    "fd00::3",
		ListenPort:   -1,
		Announce: bgp.AnnounceConfig{
			IPv4: []string{"0.0.0.0/0"},
			IPv6: []string{"::/0"},
		},
	}, testLogger(t))
	if err := speaker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = speaker.Stop() })
	agent := NewServer("", testLogger(t), speaker, nil)
	agent.SetForwardingReady(socketPath, time.Second, readTimeout, true, true)
	client := startTestServer(t, agent)

	request := &mwanv1.AnnounceRoutesRequest{}
	response, err := client.AnnounceRoutes(ctx, request)
	if err != nil || response.GetSuccess() {
		t.Fatalf("unready primary announce: response=%v error=%v", response, err)
	}
	ipv4Ready.Store(true)
	agent.reconcileForwardingReady(ctx)
	status := speaker.Status(ctx)
	if !status.AnnouncingV4 || status.AnnouncingV6 {
		t.Fatalf("v4-only readiness announcements: %+v", status)
	}
	response, err = client.AnnounceRoutes(ctx, request)
	if err != nil || response.GetSuccess() {
		t.Fatalf("partially ready primary announce: response=%v error=%v", response, err)
	}
	ipv6Ready.Store(true)
	response, err = client.AnnounceRoutes(ctx, request)
	if err != nil || !response.GetSuccess() {
		t.Fatalf("ready primary announce: response=%v error=%v", response, err)
	}
	status = speaker.Status(ctx)
	if !status.AnnouncingV4 || !status.AnnouncingV6 {
		t.Fatalf("ready announcements: %+v", status)
	}

	if _, err := client.WithdrawRoutes(ctx, &mwanv1.WithdrawRoutesRequest{}); err != nil {
		t.Fatal(err)
	}
	agent.reconcileForwardingReady(ctx)
	status = speaker.Status(ctx)
	if status.AnnouncingV4 || status.AnnouncingV6 {
		t.Fatalf("manual withdrawal was reversed: %+v", status)
	}
	ipv6Ready.Store(false)
	response, err = client.AnnounceRoutes(ctx, request)
	if err != nil || response.GetSuccess() {
		t.Fatalf("recovery before IPv6 readiness: response=%v error=%v", response, err)
	}
	ipv6Ready.Store(true)
	response, err = client.AnnounceRoutes(ctx, request)
	if err != nil || !response.GetSuccess() {
		t.Fatalf("recovery after readiness: response=%v error=%v", response, err)
	}
}
