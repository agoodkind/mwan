package watchdog

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"goodkind.io/mwan/internal/configstate"
	"goodkind.io/mwan/internal/ops"
)

// GuestConfigStateSource reads configuration state through GuestExec.
type GuestConfigStateSource struct {
	gateway ops.GatewayOps
	vmid    string
	argv    []string
	log     *slog.Logger
}

// NewGuestConfigStateSource requires a command that prints JSON compatible
// with configstate.State.
func NewGuestConfigStateSource(
	gateway ops.GatewayOps,
	vmid string,
	argv []string,
	log *slog.Logger,
) *GuestConfigStateSource {
	return &GuestConfigStateSource{
		gateway: gateway,
		vmid:    vmid,
		argv:    argv,
		log:     log,
	}
}

// Latest returns a zero configstate.State and false if guest execution or
// JSON decoding fails, or the command exits with a nonzero status.
func (s *GuestConfigStateSource) Latest(ctx context.Context) (configstate.State, bool) {
	none := configstate.State{ConfigHash: "", ConfigManifest: "", LastDeployEpoch: 0}
	result, err := s.gateway.GuestExec(ctx, s.vmid, s.argv...)
	if err != nil {
		s.log.WarnContext(ctx, "config state command execution failed", "vmid", s.vmid, "err", err)
		return none, false
	}
	if result.ExitCode != 0 {
		s.log.WarnContext(ctx, "config state command exited with a nonzero status",
			"vmid", s.vmid, "exit_code", result.ExitCode,
			"stderr", strings.TrimSpace(result.Stderr))
		return none, false
	}
	var state configstate.State
	if err := json.Unmarshal([]byte(result.Stdout), &state); err != nil {
		s.log.WarnContext(ctx, "config state command output decoding failed", "vmid", s.vmid, "err", err)
		return none, false
	}
	return state, true
}
