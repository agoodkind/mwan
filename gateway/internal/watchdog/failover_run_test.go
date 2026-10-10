package watchdog_test

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/watchdog"
	"google.golang.org/grpc"
)

const (
	primaryVMID   = "301"
	failoverLXCID = "203"
	loopbackAddr  = "127.0.0.1:0"
)

// recordingAgent answers the route-control RPCs and records which ones it
// received.
//
//testdouble:external the agents run in the gateway VM and the failover container, outside the hypervisor host
type recordingAgent struct {
	mwanv1.UnimplementedMWANAgentServer

	mu    sync.Mutex
	calls []string
}

func (a *recordingAgent) record(method string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, method)
}

func (a *recordingAgent) received(method string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, call := range a.calls {
		if call == method {
			return true
		}
	}
	return false
}

func (a *recordingAgent) GetBGPStatus(
	_ context.Context, _ *mwanv1.GetBGPStatusRequest,
) (*mwanv1.GetBGPStatusResponse, error) {
	a.record("GetBGPStatus")
	return &mwanv1.GetBGPStatusResponse{}, nil
}

func (a *recordingAgent) AnnounceRoutes(
	_ context.Context, _ *mwanv1.AnnounceRoutesRequest,
) (*mwanv1.AnnounceRoutesResponse, error) {
	a.record("AnnounceRoutes")
	return &mwanv1.AnnounceRoutesResponse{Success: true}, nil
}

func (a *recordingAgent) WithdrawRoutes(
	_ context.Context, _ *mwanv1.WithdrawRoutesRequest,
) (*mwanv1.WithdrawRoutesResponse, error) {
	a.record("WithdrawRoutes")
	return &mwanv1.WithdrawRoutesResponse{Success: true}, nil
}

// startAgent serves a recordingAgent on a loopback TCP port and returns the
// agent with its address.
func startAgent(t *testing.T) (*recordingAgent, string) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(
		context.Background(), "tcp", loopbackAddr,
	)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	agent := &recordingAgent{}
	server := grpc.NewServer()
	mwanv1.RegisterMWANAgentServer(server, agent)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)
	return agent, listener.Addr().String()
}

func failoverConfig(t *testing.T, primaryAddr, failoverAddr string) *config.Config {
	t.Helper()
	logDir := t.TempDir()
	cfg := &config.Config{}
	cfg.MwanVMID = primaryVMID
	cfg.Watchdog.MwanAgentTCPAddr = primaryAddr
	cfg.Watchdog.LogFile = filepath.Join(logDir, "watchdog.log")
	cfg.Watchdog.JSONLogFile = filepath.Join(logDir, "watchdog.json")
	cfg.Failover.LXCID = failoverLXCID
	cfg.Failover.AgentTCPAddr = failoverAddr
	return cfg
}

func TestFailoverRunWithdrawsOnPrimaryAndAnnouncesOnFailoverAgent(t *testing.T) {
	primary, primaryAddr := startAgent(t)
	failover, failoverAddr := startAgent(t)
	cfg := failoverConfig(t, primaryAddr, failoverAddr)

	if err := watchdog.FailoverRun(cfg); err != nil {
		t.Fatalf("FailoverRun: %v", err)
	}

	if !primary.received("WithdrawRoutes") {
		t.Error("primary agent did not receive WithdrawRoutes")
	}
	if primary.received("AnnounceRoutes") {
		t.Error("primary agent received AnnounceRoutes")
	}
	if !failover.received("GetBGPStatus") {
		t.Error("failover agent did not receive GetBGPStatus")
	}
	if !failover.received("AnnounceRoutes") {
		t.Error("failover agent did not receive AnnounceRoutes")
	}
	if failover.received("WithdrawRoutes") {
		t.Error("failover agent received WithdrawRoutes")
	}
}

func TestFailoverRunRejectsMissingFailoverAgentAddress(t *testing.T) {
	_, primaryAddr := startAgent(t)
	cfg := failoverConfig(t, primaryAddr, "")

	err := watchdog.FailoverRun(cfg)
	if err == nil {
		t.Fatal("FailoverRun succeeded without [failover] agent_tcp_addr")
	}
	if !strings.Contains(err.Error(), "agent_tcp_addr") {
		t.Fatalf("error %q does not mention agent_tcp_addr", err)
	}
}

func TestFailoverRunRejectsEqualGuestIdentifiers(t *testing.T) {
	primary, primaryAddr := startAgent(t)
	failover, failoverAddr := startAgent(t)
	cfg := failoverConfig(t, primaryAddr, failoverAddr)
	cfg.Failover.LXCID = cfg.MwanVMID

	err := watchdog.FailoverRun(cfg)
	if err == nil {
		t.Fatal("FailoverRun succeeded with [failover] lxc_id equal to mwan_vmid")
	}
	if !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("error %q does not mention must differ", err)
	}
	if primary.received("AnnounceRoutes") {
		t.Error("primary agent received AnnounceRoutes")
	}
	if failover.received("AnnounceRoutes") {
		t.Error("failover agent received AnnounceRoutes")
	}
}
