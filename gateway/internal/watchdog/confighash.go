package watchdog

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"goodkind.io/mwan/internal/notify"
)

// routeChannelFallback bounds the per-cycle TCP-fallback warning to one
// email per state transition plus one per repeat-cadence window. The
// vsock-unavailable path used to fire WARN every 5-minute cycle on
// testbed because VM 950 has no vhost-vsock-pci device. When the vsock
// channel returns successfully, Resolve fires the recovery line.
func (w *watchdog) routeChannelFallback(ctx context.Context, usedChannel string) {
	notifier := w.notifierOrNull()
	key := w.cfg.MwanVMID + ":" + strconv.FormatUint(uint64(w.cfg.Watchdog.VsockPort), 10)
	if usedChannel == "tcp" {
		notifier.Notify(ctx, notify.Event{
			Now:     w.now(),
			Level:   slog.LevelWarn,
			Kind:    "vsock-fallback",
			Key:     key,
			Message: "getConfigState: vsock unavailable, used TCP fallback",
			Fields: []slog.Attr{
				slog.String("channel", usedChannel),
				slog.String("vm_id", w.cfg.MwanVMID),
			},
			IsRecovery: false,
		})
		return
	}
	if usedChannel == "vsock" {
		notifier.Resolve(ctx, "vsock-fallback", key,
			"getConfigState: vsock channel restored",
			slog.String("channel", usedChannel),
			slog.String("vm_id", w.cfg.MwanVMID),
		)
	}
}

// parseManifest parses a manifest in sha256sum(1) format: "<hash>  <path>\n".
// Returns a map of path -> sha256hex. Lines that don't match the format are
// silently skipped.
func parseManifest(raw string) map[string]string {
	m := make(map[string]string)
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// sha256sum format: 64 hex chars, two spaces, then the path.
		if len(line) < 66 || line[64] != ' ' || line[65] != ' ' {
			continue
		}
		hash := line[:64]
		path := line[66:]
		if path != "" {
			m[path] = hash
		}
	}
	return m
}

// categorizeManifestChanges compares two path->sha256hex maps and returns
// sorted lists of changed, added, and removed paths.
func categorizeManifestChanges(prev, curr map[string]string) (changed, added, removed []string) {
	for path, hash := range curr {
		if oldHash, ok := prev[path]; !ok {
			added = append(added, path)
		} else if hash != oldHash {
			changed = append(changed, path)
		}
	}
	for path := range prev {
		if _, ok := curr[path]; !ok {
			removed = append(removed, path)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	return changed, added, removed
}

// manifestDiff compares two path->sha256hex maps and returns a formatted
// summary of changed, added, and removed files for inclusion in an email.
func manifestDiff(prev, curr map[string]string) string {
	if len(curr) == 0 {
		return "  (manifest unavailable for current state)\n"
	}
	if len(prev) == 0 {
		var lines []string
		for path := range curr {
			lines = append(lines, "  "+path)
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n") + "\n"
	}

	changed, added, removed := categorizeManifestChanges(prev, curr)
	if len(changed) == 0 && len(added) == 0 && len(removed) == 0 {
		return "  (no per-file diff available; composite hash changed)\n"
	}
	var sb strings.Builder
	for _, p := range changed {
		sb.WriteString("  modified: ")
		sb.WriteString(p)
		sb.WriteByte('\n')
	}
	for _, p := range added {
		sb.WriteString("  added:    ")
		sb.WriteString(p)
		sb.WriteByte('\n')
	}
	for _, p := range removed {
		sb.WriteString("  removed:  ")
		sb.WriteString(p)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (w *watchdog) checkConfigHash(ctx context.Context) {
	log := w.tracedLogger(ctx)
	resp, usedChannel, err := w.ops.GetConfigState(ctx, w.cfg.MwanVMID)
	if err != nil {
		log.WarnContext(ctx, "checkConfigHash getConfigState", "err", err)
		w.lastHashCheckOK = false
		return
	}
	w.routeChannelFallback(ctx, usedChannel)
	h := strings.TrimSpace(resp.GetConfigHash())
	if h == "" {
		return
	}
	currentManifest := parseManifest(resp.GetConfigManifest())

	if w.lastConfigHash != "" && h != w.lastConfigHash {
		if !w.postRollbackGraceUntil.IsZero() &&
			w.now().Before(w.postRollbackGraceUntil) {
			log.InfoContext(ctx,
				"Post-rollback hash change suppressed",
				"old_hash", w.lastConfigHash,
				"new_hash", h,
				"grace_until", w.postRollbackGraceUntil,
			)
		} else {
			w.hashChangeWindowStart = w.now().Unix()
			diffSection := manifestDiff(w.lastManifest, currentManifest)
			log.WarnContext(ctx,
				"config hash drift detected",
				"old_hash", w.lastConfigHash,
				"new_hash", resp.GetConfigHash(),
				"changed_files", diffSection,
				"vm_id", w.cfg.MwanVMID,
				"node", w.cfg.PVE.Node,
				"change_window_minutes", w.cfg.Watchdog.DeployWindowMinutes,
			)
		}
	} else {
		log.DebugContext(ctx,
			"config hash check: no drift",
			"hash", h,
			"channel", usedChannel,
		)
		w.lastHashCheckOK = true
	}
	w.lastConfigHash = h
	w.lastManifest = currentManifest
}
