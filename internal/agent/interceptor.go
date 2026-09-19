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
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

var traceMetadataKeys = []string{
	"x-trace-id",
	"trace-id",
	"trace_id",
}

// tracedRPCKey marks a context whose RPC traceStatsHandler traced. Its value
// is the RPC's full method name.
type tracedRPCKey struct{}

// traceStatsHandler gives every unary RPC of the registered services a trace
// context and logs when the RPC starts and finishes. The context TagRPC
// returns is the one grpc hands the method handler, so the handler logs with
// the same trace ID as these two records.
//
// It is a grpc stats handler rather than a unary interceptor because
// grpc.UnaryServerInterceptor is declared in terms of any, and the
// no_any_or_empty_interface analyzer rejects any in every signature this
// module writes. Streaming RPCs and methods outside the registered services
// are left untouched, which is the set a unary interceptor never saw.
type traceStatsHandler struct {
	logger       *slog.Logger
	unaryMethods map[string]struct{}
}

// newTraceStatsHandler builds the handler for the unary methods of services.
func newTraceStatsHandler(
	logger *slog.Logger, services ...*grpc.ServiceDesc,
) *traceStatsHandler {
	unaryMethods := make(map[string]struct{})
	for _, service := range services {
		for _, method := range service.Methods {
			unaryMethods["/"+service.ServiceName+"/"+method.MethodName] = struct{}{}
		}
	}
	return &traceStatsHandler{logger: logger, unaryMethods: unaryMethods}
}

// TagRPC attaches the trace ID from the incoming metadata, or a new one, to a
// unary RPC's context.
func (h *traceStatsHandler) TagRPC(
	ctx context.Context, info *stats.RPCTagInfo,
) context.Context {
	if _, unary := h.unaryMethods[info.FullMethodName]; !unary {
		return ctx
	}
	methodName := path.Base(info.FullMethodName)
	traceID := incomingTraceID(ctx)
	if traceID == "" {
		traceID = tracing.NewID()
	}
	ctx = tracing.WithTraceID(ctx, traceID)
	ctx = tracing.WithOperation(ctx, methodName)
	ctx, _ = tracing.StartTrace(ctx, "", methodName)
	return context.WithValue(ctx, tracedRPCKey{}, info.FullMethodName)
}

// HandleRPC logs the start and the finish of an RPC that TagRPC traced.
func (h *traceStatsHandler) HandleRPC(ctx context.Context, rpcStats stats.RPCStats) {
	fullMethod, traced := ctx.Value(tracedRPCKey{}).(string)
	if !traced {
		return
	}
	log := tracing.Logger(ctx, h.logger)
	switch event := rpcStats.(type) {
	case *stats.Begin:
		peerAddr := ""
		transport := ""
		if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
			peerAddr = peerInfo.Addr.String()
			transport = peerInfo.Addr.Network()
		}
		log.InfoContext(
			ctx,
			"grpc request started",
			"rpc_method", fullMethod,
			"peer_addr", peerAddr,
			"transport", transport,
		)
	case *stats.End:
		log.InfoContext(
			ctx,
			"grpc request finished",
			"rpc_method", fullMethod,
			"grpc_code", status.Code(event.Error).String(),
			"duration_ms", event.EndTime.Sub(event.BeginTime).Milliseconds(),
		)
	}
}

// TagConn returns ctx unchanged; the handler traces RPCs, not connections.
func (h *traceStatsHandler) TagConn(
	ctx context.Context, _ *stats.ConnTagInfo,
) context.Context {
	return ctx
}

// HandleConn records nothing; the handler traces RPCs, not connections.
func (h *traceStatsHandler) HandleConn(context.Context, stats.ConnStats) {}

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
