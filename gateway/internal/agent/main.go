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

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Run is the entry point for the agent subcommand.
// It starts the configured gRPC listeners and BGP speaker.
func Run(cfg *config.Config, options config.AgentSection) error {
	socketPath := &options.SocketPath
	tcpAddr := &options.TCPAddr
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
	runID := tracing.NewID()
	logger = logger.With(slog.String("run_id", runID), slog.String(tracing.ComponentKey, "agent"))

	logger.Info(
		"mwan-agent starting",
		"detail",
		version.BuildVersionString(),
		"socket_path",
		*socketPath,
		"tcp_addr",
		*tcpAddr,
	)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tcpLis, err := net.Listen("tcp", *tcpAddr)
	if err != nil {
		logger.Error("tcp listen", "error", err, "tcp_addr", *tcpAddr)
		return fmt.Errorf("tcp listen %s: %w", *tcpAddr, err)
	}
	defer tcpLis.Close()

	var unixLis net.Listener
	var closeUnix func()
	if *socketPath != "" {
		unixLis, closeUnix, err = listenUnix(logger, *socketPath)
		if err != nil {
			return err
		}
		defer closeUnix()
	}

	bgpSpeaker, err := newBGPSpeaker(cfg, logger)
	if err != nil {
		return err
	}
	if bgpSpeaker != nil {
		if err := configureBGPFIB(
			runCtx,
			cfg,
			bgpSpeaker,
			logger,
			networkjson.DefaultPath,
			networkjson.DefaultSchemaDir,
		); err != nil {
			return err
		}
		if err := bgpSpeaker.Start(runCtx); err != nil {
			logger.Error("bgp speaker start failed", "error", err)
			return fmt.Errorf("bgp speaker start: %w", err)
		}
		logger.Info("bgp speaker started", "asn", cfg.BGP.ASN, "router_id", cfg.BGP.RouterID)
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(newTraceStatsHandler(logger, &mwanv1.MWANAgent_ServiceDesc)),
	)
	agentServer := NewServer(logger, bgpSpeaker)
	if bgpSpeaker != nil && cfg.BGP.ForwardingReadinessEnabled() {
		readiness := cfg.BGP.ForwardingReadiness
		agentServer.SetForwardingReady(
			readiness.SocketPath,
			readiness.PollInterval(),
			readiness.ReadTimeout(),
			len(cfg.BGP.Announce.IPv4) > 0,
			len(cfg.BGP.Announce.IPv6) > 0,
			true,
		)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.ErrorContext(
						runCtx,
						"forwarding readiness poll panic",
						"error",
						recovered,
					)
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
	if unixLis != nil {
		serveCount++
	}

	errCh := make(chan error, serveCount)

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
	if unixLis != nil {
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					recoveredErr := fmt.Errorf("panic: %v", recovered)
					logger.Error("grpc serve panic", "listener", "unix", "error", recoveredErr)
					errCh <- fmt.Errorf("grpc serve unix panic: %v", recovered)
				}
			}()
			errCh <- grpcServer.Serve(unixLis)
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

func listenUnix(logger *slog.Logger, path string) (net.Listener, func(), error) {
	listener, err := net.Listen("unix", path)
	if err != nil {
		logger.Error("unix listen failed", "error", err, "socket_path", path)
		return nil, nil, fmt.Errorf("unix listen %s: %w", path, err)
	}

	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		err := fmt.Errorf("listener for %q is not a Unix socket", path)
		logger.Error("unix listener setup failed", "error", err, "socket_path", path)
		return nil, nil, err
	}
	unixListener.SetUnlinkOnClose(false)

	created, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		wrappedErr := fmt.Errorf("inspect created socket: %w", err)
		logger.Error("unix listener setup failed", "error", wrappedErr, "socket_path", path)
		return nil, nil, wrappedErr
	}

	closeListener := func() {
		_ = listener.Close()
		current, statErr := os.Lstat(path)
		if statErr == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(created, current) {
			_ = os.Remove(path)
		}
	}

	return listener, closeListener, nil
}
