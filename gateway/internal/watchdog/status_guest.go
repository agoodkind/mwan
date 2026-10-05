package watchdog

import (
	"context"
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/statuspush"
)

type GuestStatusSource struct {
	gateway ops.GatewayOps
	vmid    string
	argv    []string
	log     *slog.Logger
	now     func() time.Time
}

func NewGuestStatusSource(
	gateway ops.GatewayOps,
	vmid string,
	argv []string,
	log *slog.Logger,
) *GuestStatusSource {
	return &GuestStatusSource{
		gateway: gateway,
		vmid:    vmid,
		argv:    argv,
		log:     log,
		now:     time.Now,
	}
}

// Latest runs the status command on every call. Only the diagnosis path calls
// it, and that path runs after connectivity has already degraded.
func (s *GuestStatusSource) Latest() (statuspush.Status, time.Time, bool) {
	ctx := context.Background()
	none := statuspush.Status{SentAt: time.Time{}, ActiveTier: 0, Providers: nil}
	result, err := s.gateway.GuestExec(ctx, s.vmid, s.argv...)
	if err != nil {
		s.log.WarnContext(ctx, "guest status command failed", "vmid", s.vmid, "err", err)
		return none, time.Time{}, false
	}
	if result.ExitCode != 0 {
		s.log.WarnContext(ctx, "guest status command exited non-zero",
			"vmid", s.vmid, "exit_code", result.ExitCode)
		return none, time.Time{}, false
	}
	status, err := statuspush.UnmarshalStatus([]byte(result.Stdout))
	if err != nil {
		s.log.WarnContext(ctx, "guest status output rejected", "vmid", s.vmid, "err", err)
		return none, time.Time{}, false
	}
	return status, s.now(), true
}
