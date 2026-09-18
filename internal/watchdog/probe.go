package watchdog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"goodkind.io/mwan/internal/ops"
)

func (w *watchdog) guestExecProbe(ctx context.Context, args ...string) (bool, bool) {
	log := w.tracedLogger(ctx)
	parsed, err := w.ops.GuestExec(ctx, w.cfg.MwanVMID, args...)
	if err != nil {
		if errors.Is(err, ops.ErrGuestExecUnavailable) {
			log.WarnContext(ctx,
				"guestExec unavailable",
				"args", strings.Join(args, " "),
				"err", err,
			)
			return false, true
		}
		log.ErrorContext(ctx,
			"guestExec error",
			"args", strings.Join(args, " "),
			"err", err,
		)
		return false, false
	}
	if parsed.ExitCode != 0 {
		log.InfoContext(ctx,
			"guestExec non-zero exit",
			"args", strings.Join(args, " "),
			"exit_code", parsed.ExitCode,
		)
		return false, false
	}
	return true, false
}

// probeConnectivity pings the configured IPv4 and IPv6 targets from the host.
func (w *watchdog) probeConnectivity(ctx context.Context) (v4ok, v6ok bool) {
	log := w.tracedLogger(ctx)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(2)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "IPv4 probe panic", "err", fmt.Errorf("panic: %v", recovered))
			}
		}()
		defer wg.Done()
		ok := w.ops.Ping(ctx, "ping", w.cfg.Network.PingTargetIPv4)
		mu.Lock()
		v4ok = ok
		mu.Unlock()
	}()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "IPv6 probe panic", "err", fmt.Errorf("panic: %v", recovered))
			}
		}()
		defer wg.Done()
		ok := w.ops.Ping(ctx, "ping6", w.cfg.Network.PingTargetIPv6)
		mu.Lock()
		v6ok = ok
		mu.Unlock()
	}()
	wg.Wait()

	v4str := "OK"
	if !v4ok {
		v4str = "FAIL"
	}
	v6str := "OK"
	if !v6ok {
		v6str = "FAIL"
	}
	log.InfoContext(ctx,
		"probe",
		"ipv4_target", w.cfg.Network.PingTargetIPv4,
		"ipv4", v4str,
		"ipv6_target", w.cfg.Network.PingTargetIPv6,
		"ipv6", v6str,
	)
	w.appendProbe(fmt.Sprintf(
		"Host probe: IPv4 %s (%s), IPv6 %s (%s)",
		w.cfg.Network.PingTargetIPv4, v4str,
		w.cfg.Network.PingTargetIPv6, v6str,
	))
	return v4ok, v6ok
}

// testVMConnectivity pings through the VM's default route to distinguish a
// MWAN routing failure from a Proxmox-side issue.
func (w *watchdog) testVMConnectivity(ctx context.Context) bool {
	log := w.tracedLogger(ctx)
	log.InfoContext(ctx,
		"Testing VM default-route connectivity",
		"vmid", w.cfg.MwanVMID,
		"ping6_target", w.cfg.Network.PingTargetIPv6,
		"ping_target", w.cfg.Network.PingTargetIPv4,
	)
	v6ok, v6Unavailable := w.guestExecProbe(
		ctx, "ping6", "-c", "2", "-W", "3", w.cfg.Network.PingTargetIPv6,
	)
	if v6ok {
		log.InfoContext(ctx,
			"VM default-route IPv6 ping OK -> issue is Proxmox-side",
			"vmid", w.cfg.MwanVMID,
		)
		w.appendProbe(fmt.Sprintf(
			"VM %s default-route: IPv6 OK (Proxmox-side issue confirmed)",
			w.cfg.MwanVMID,
		))
		return true
	}
	v4ok, v4Unavailable := w.guestExecProbe(
		ctx, "ping", "-c", "2", "-W", "3", w.cfg.Network.PingTargetIPv4,
	)
	if v4ok {
		log.InfoContext(ctx,
			"VM default-route IPv4 ping OK -> issue is Proxmox-side",
			"vmid", w.cfg.MwanVMID,
		)
		w.appendProbe(fmt.Sprintf(
			"VM %s default-route: IPv4 OK (Proxmox-side issue confirmed)",
			w.cfg.MwanVMID,
		))
		return true
	}
	if v6Unavailable || v4Unavailable {
		log.WarnContext(ctx,
			"VM default-route probes unavailable due to guest-exec transport",
			"vmid", w.cfg.MwanVMID,
		)
		w.appendProbe(fmt.Sprintf(
			"VM %s default-route probes unavailable (guest-exec transport)",
			w.cfg.MwanVMID,
		))
	}
	log.InfoContext(ctx,
		"VM default-route: both IPv4 and IPv6 FAILED",
		"vmid", w.cfg.MwanVMID,
	)
	w.appendProbe(fmt.Sprintf(
		"VM %s default-route: IPv4 FAIL, IPv6 FAIL",
		w.cfg.MwanVMID,
	))
	return false
}

func (w *watchdog) readGuestUnix(ctx context.Context, path string) (int64, bool) {
	log := w.tracedLogger(ctx)
	parsed, err := w.ops.GuestExec(ctx, w.cfg.MwanVMID, "cat", path)
	if err != nil {
		if errors.Is(err, ops.ErrGuestExecUnavailable) {
			log.WarnContext(ctx,
				"PVE guest-exec unavailable; cannot read deploy timestamp; assuming no recent deploy",
				"vmid", w.cfg.MwanVMID,
			)
		} else {
			log.ErrorContext(ctx, "guestExec(cat) error", "path", path, "err", err)
		}
		return 0, false
	}
	if parsed.ExitCode != 0 {
		return 0, false
	}
	raw := strings.TrimSpace(parsed.Stdout)
	if raw == "" || raw == "null" {
		return 0, false
	}
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.ErrorContext(ctx,
			"guest timestamp parse error",
			"path", path,
			"raw", raw,
			"err", err,
		)
		return 0, false
	}
	return ts, true
}

func (w *watchdog) checkDeploy(ctx context.Context) (int64, bool) {
	log := w.tracedLogger(ctx)
	running, err := w.ops.VMStatus(ctx, w.cfg.MwanVMID)
	if err != nil {
		log.ErrorContext(ctx, "checkDeploy: vmStatus error", "err", err)
		return 0, false
	}
	if !running {
		log.InfoContext(ctx,
			"checkDeploy: VM is not running; cannot check change window",
			"vmid", w.cfg.MwanVMID,
		)
		return 0, false
	}

	log.InfoContext(ctx,
		"checkDeploy: reading change window markers",
		"last_deploy_path", w.cfg.Network.LastDeployPath,
		"last_change_path", w.cfg.Network.LastChangePath,
		"vmid", w.cfg.MwanVMID,
	)

	deployTS, dOK := w.readGuestUnix(ctx, w.cfg.Network.LastDeployPath)
	changeTS, cOK := w.readGuestUnix(ctx, w.cfg.Network.LastChangePath)

	var candidates []int64
	if dOK {
		candidates = append(candidates, deployTS)
	}
	if cOK {
		candidates = append(candidates, changeTS)
	}
	if w.hashChangeWindowStart > 0 {
		candidates = append(candidates, w.hashChangeWindowStart)
	}
	if len(candidates) == 0 {
		log.InfoContext(ctx, "checkDeploy: no change markers or hash window")
		return 0, false
	}
	effective := candidates[0]
	for _, t := range candidates[1:] {
		if t > effective {
			effective = t
		}
	}

	ageMin := (w.now().Unix() - effective) / 60
	log.InfoContext(ctx,
		"checkDeploy: change window",
		"deploy_ts", deployTS,
		"deploy_ok", dOK,
		"change_ts", changeTS,
		"change_ok", cOK,
		"hash_window_ts", w.hashChangeWindowStart,
		"effective_ts", effective,
		"age_minutes", ageMin,
		"window_minutes", w.cfg.Watchdog.DeployWindowMinutes,
	)
	if ageMin > int64(w.cfg.Watchdog.DeployWindowMinutes) {
		log.InfoContext(ctx,
			"checkDeploy: change window stale",
			"age_minutes", ageMin,
			"window_minutes", w.cfg.Watchdog.DeployWindowMinutes,
		)
		return 0, false
	}

	log.InfoContext(ctx,
		"checkDeploy: within change window",
		"effective_ts", effective,
		"age_minutes", ageMin,
	)
	return effective, true
}
