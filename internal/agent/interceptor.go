package agent

import (
	"context"
	"log/slog"
	"path"
	"strings"

	"goodkind.io/mwan/internal/tracing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

var traceMetadataKeys = []string{
	"x-trace-id",
	"trace-id",
	"trace_id",
}

func unaryTraceInterceptor(
	logger *slog.Logger,
) grpc.UnaryServerInterceptor {
	clock := realClock{}
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		methodName := path.Base(info.FullMethod)
		traceID := incomingTraceID(ctx)
		if traceID == "" {
			traceID = tracing.NewID()
		}

		ctx = tracing.WithTraceID(ctx, traceID)
		ctx = tracing.WithOperation(ctx, methodName)
		ctx, _ = tracing.StartTrace(ctx, "", methodName)
		log := tracing.Logger(ctx, logger)

		peerAddr := ""
		transport := ""
		if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
			peerAddr = peerInfo.Addr.String()
			transport = peerInfo.Addr.Network()
		}

		startTime := clock.Now()
		log.InfoContext(
			ctx,
			"grpc request started",
			"rpc_method", info.FullMethod,
			"peer_addr", peerAddr,
			"transport", transport,
		)

		resp, err := handler(ctx, req)
		code := status.Code(err).String()
		log.InfoContext(
			ctx,
			"grpc request finished",
			"rpc_method", info.FullMethod,
			"grpc_code", code,
			"duration_ms", clock.Now().Sub(startTime).Milliseconds(),
		)
		return resp, err
	}
}

func incomingTraceID(ctx context.Context) string {
	metadataMap, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, key := range traceMetadataKeys {
		values := metadataMap.Get(key)
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
