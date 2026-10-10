package watchdog

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"goodkind.io/mwan/internal/configstate"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/watchdogconfig"
)

type configStateSource interface {
	Latest(ctx context.Context) (configstate.State, string, bool)
}

func newConfigStateSource(
	cfg *watchdogconfig.Config,
	gateway ops.GatewayOps,
	log *slog.Logger,
) configStateSource {
	if len(cfg.Watchdog.ConfigStateCommand) > 0 {
		return NewGuestConfigStateSource(
			gateway,
			cfg.MwanVMID,
			cfg.Watchdog.ConfigStateCommand,
			log,
		)
	}
	return &rpcConfigStateSource{
		gateway: gateway,
		vmid:    cfg.MwanVMID,
		log:     log,
	}
}

type rpcConfigStateSource struct {
	gateway ops.GatewayOps
	vmid    string
	log     *slog.Logger
}

func (s *rpcConfigStateSource) Latest(ctx context.Context) (configstate.State, string, bool) {
	resp, usedChannel, err := s.gateway.GetConfigState(ctx, s.vmid)
	if err != nil {
		tracing.Logger(ctx, s.log).WarnContext(ctx, "checkConfigHash getConfigState", "err", err)
		return configstate.State{ConfigHash: "", ConfigManifest: "", LastDeployEpoch: 0}, "", false
	}
	state := configstate.State{
		ConfigHash:      resp.GetConfigHash(),
		ConfigManifest:  resp.GetConfigManifest(),
		LastDeployEpoch: resp.GetLastDeployEpoch(),
	}
	return state, usedChannel, true
}

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

// Latest returns decoded state, channel ops.ChanPVE, and true on success.
// Guest execution errors, nonzero exit status, or JSON decoding errors
// return a zero configstate.State, an empty channel, and false.
func (s *GuestConfigStateSource) Latest(ctx context.Context) (configstate.State, string, bool) {
	log := tracing.Logger(ctx, s.log)
	none := configstate.State{ConfigHash: "", ConfigManifest: "", LastDeployEpoch: 0}
	result, err := s.gateway.GuestExec(ctx, s.vmid, s.argv...)
	if err != nil {
		log.WarnContext(ctx, "config state command execution failed", "vmid", s.vmid, "err", err)
		return none, "", false
	}
	if result.ExitCode != 0 {
		log.WarnContext(ctx, "config state command exited with a nonzero status",
			"vmid", s.vmid, "exit_code", result.ExitCode,
			"stderr", strings.TrimSpace(result.Stderr))
		return none, "", false
	}
	var state configstate.State
	if err := json.Unmarshal([]byte(result.Stdout), &state); err != nil {
		log.WarnContext(ctx, "config state command output decoding failed", "vmid", s.vmid, "err", err)
		return none, "", false
	}
	return state, string(ops.ChanPVE), true
}
