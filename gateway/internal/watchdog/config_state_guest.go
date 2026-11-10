package watchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/mwan/internal/configstate"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/watchdogconfig"
)

type configStateSource interface {
	Latest(ctx context.Context) (configstate.State, string, error)
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

func (s *rpcConfigStateSource) Latest(ctx context.Context) (configstate.State, string, error) {
	resp, usedChannel, err := s.gateway.GetConfigState(ctx, s.vmid)
	if err != nil {
		none := configstate.State{ConfigHash: "", ConfigManifest: "", LastDeployEpoch: 0}
		log := tracing.Logger(ctx, s.log)
		log.WarnContext(ctx, "checkConfigHash getConfigState", "err", err)
		return none, "", fmt.Errorf("get config state: %w", err)
	}
	state := configstate.State{
		ConfigHash:      resp.GetConfigHash(),
		ConfigManifest:  resp.GetConfigManifest(),
		LastDeployEpoch: resp.GetLastDeployEpoch(),
	}
	return state, usedChannel, nil
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

// Latest returns decoded state, ops.ChanPVE, and nil on success.
// Guest execution or JSON decoding failures return a wrapped error.
// A nonzero exit status returns an error with the status and stderr.
// Every failure returns a zero configstate.State and an empty channel.
func (s *GuestConfigStateSource) Latest(ctx context.Context) (configstate.State, string, error) {
	log := tracing.Logger(ctx, s.log)
	none := configstate.State{ConfigHash: "", ConfigManifest: "", LastDeployEpoch: 0}
	result, err := s.gateway.GuestExec(ctx, s.vmid, s.argv...)
	if err != nil {
		log.WarnContext(ctx, "config state command execution failed", "vmid", s.vmid, "err", err)
		return none, "", fmt.Errorf("execute config state command: %w", err)
	}
	if result.ExitCode != 0 {
		stderr := strings.TrimSpace(result.Stderr)
		log.WarnContext(ctx, "config state command exited with a nonzero status",
			"vmid", s.vmid, "exit_code", result.ExitCode,
			"stderr", stderr)
		return none, "", fmt.Errorf("config state command exited with status %d: %s", result.ExitCode, stderr)
	}
	var state configstate.State
	if err := json.Unmarshal([]byte(result.Stdout), &state); err != nil {
		log.WarnContext(ctx, "config state command output decoding failed", "vmid", s.vmid, "err", err)
		return none, "", fmt.Errorf("decode config state command output: %w", err)
	}
	return state, string(ops.ChanPVE), nil
}
