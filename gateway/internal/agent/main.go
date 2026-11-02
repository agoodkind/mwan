// Package agent provides the gRPC agent for serving the MWAN API.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/mdlayher/vsock"
	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Run is the entry point for the agent subcommand.
// It starts logging and the gRPC server with explicit options.
func Run(cfg *config.Config, options config.AgentSection) error {
	vsockPort := &options.VsockPort
	tcpAddr := &options.TCPAddr
	deployFile := &options.DeployFile
	deployExpected := &options.DeployExpected
	logFile := &options.LogFile
	debug := &options.Debug

	handlers := []slog.Handler{logging.StdoutJSON()}
	if *logFile != "" {
		handlers = append(handlers, logging.FileJSON(*logFile))
	}
	logger, _ := logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers:     handlers,
	})
	notifier := notify.FromConfig(cfg.Email, cfg.NotifyConfig(), logger, "mwan-agent")
	runID := tracing.NewID()
	logger = logger.With(
		slog.String(tracing.RunIDKey, runID),
		slog.String(tracing.ComponentKey, "agent"),
	)

	port := *vsockPort

	logger.Info(
		"mwan-agent starting",
		"detail", version.BuildVersionString(),
		"vsock_port", *vsockPort,
		"tcp_addr", *tcpAddr,
	)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bgpSpeaker, err := newBGPSpeaker(cfg, logger)
	if err != nil {
		return err
	}
	if bgpSpeaker != nil {
		if err := configurePlatformBGP(runCtx, cfg, bgpSpeaker, logger); err != nil {
			return err
		}
		if err := bgpSpeaker.Start(runCtx); err != nil {
			logger.Error("bgp speaker start failed", "error", err)
			return fmt.Errorf("bgp speaker start: %w", err)
		}
		logger.Info("bgp speaker started", "asn", cfg.BGP.ASN, "router_id", cfg.BGP.RouterID)
	}

	var vsockLis net.Listener
	if port != 0 {
		var vsockErr error
		vsockLis, vsockErr = vsock.Listen(port, nil)
		if vsockErr != nil {
			logger.Warn(
				"vsock listen failed, continuing without vsock",
				"error", vsockErr,
				"vsock_port", port,
			)
		}
	}

	tcpLis, err := net.Listen("tcp", *tcpAddr)
	if err != nil {
		logger.Error("tcp listen", "error", err, "tcp_addr", *tcpAddr)
		return fmt.Errorf("tcp listen %s: %w", *tcpAddr, err)
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(newTraceStatsHandler(logger, &mwanv1.MWANAgent_ServiceDesc)),
	)
	agentServer := NewServer(*deployFile, logger, bgpSpeaker, notifier)
	agentServer.SetDeployExpected(*deployExpected)
	if bgpSpeaker != nil && cfg.BGP.UseWanconfig {
		readiness := cfg.BGP.ForwardingReadiness
		agentServer.SetForwardingReady(
			readiness.SocketPath,
			readiness.PollInterval(),
			readiness.ReadTimeout(),
			len(cfg.BGP.Announce.IPv4) > 0,
			len(cfg.BGP.Announce.IPv6) > 0,
		)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.ErrorContext(runCtx, "primary forwarding readiness poll panic", "error", recovered)
				}
			}()
			agentServer.pollForwardingReady(runCtx)
		}()
	}
	mwanv1.RegisterMWANAgentServer(grpcServer, agentServer)
	if *debug {
		reflection.Register(grpcServer)
		logger.Info("gRPC reflection enabled (debug mode)")
	}

	serveCount := 1
	if vsockLis != nil {
		serveCount++
	}

	errCh := make(chan error, 2)

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				recoveredErr := fmt.Errorf("panic: %v", recovered)
				logger.Error("grpc serve panic", "listener", "tcp", "error", recoveredErr)
				errCh <- fmt.Errorf("grpc serve tcp panic: %v", recovered)
			}
		}()
		errCh <- grpcServer.Serve(tcpLis)
	}()
	if vsockLis != nil {
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					recoveredErr := fmt.Errorf("panic: %v", recovered)
					logger.Error("grpc serve panic", "listener", "vsock", "error", recoveredErr)
					errCh <- fmt.Errorf("grpc serve vsock panic: %v", recovered)
				}
			}()
			errCh <- grpcServer.Serve(vsockLis)
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer func() {
		signal.Stop(sigCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("grpc serve", "error", err)
			return fmt.Errorf("grpc serve: %w", err)
		}
		for i := 1; i < serveCount; i++ {
			if err := <-errCh; err != nil {
				logger.Error("grpc serve", "error", err)
				return fmt.Errorf("grpc serve: %w", err)
			}
		}
	case sig := <-sigCh:
		logger.Info("shutdown signal", "signal", sig.String())
		if bgpSpeaker != nil {
			// When BGP Graceful Restart is enabled, skip the pre-emptive
			// route withdrawal: an explicit WITHDRAW defeats GR semantics
			// because the helper (OPNsense FRR) sees the withdraw and
			// removes the route immediately. With GR off, the pre-withdraw
			// is the right behaviour for clean shutdown.
			if !cfg.BGP.GracefulRestart.Enabled {
				_ = bgpSpeaker.WithdrawDefault()
			}
			_ = bgpSpeaker.Stop()
		}
		cancel()
		grpcServer.GracefulStop()
		for i := 0; i < serveCount; i++ {
			if err := <-errCh; err != nil {
				logger.Error("grpc after graceful stop", "error", err)
				return fmt.Errorf("grpc after graceful stop: %w", err)
			}
		}
	}
	return nil
}
