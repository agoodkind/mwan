// Package rollback records whether the watchdog has already rolled the gateway
// back for a given deploy, and reads `qm listsnapshot` output to decide which
// snapshot to roll back to. The state is a plain key-value file so an operator
// can read it during an incident without the daemon running.
package rollback

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// PreDeploySnapRE matches the snapshot the deploy takes before it
	// changes anything, which is the ordinary rollback target.
	PreDeploySnapRE = regexp.MustCompile(`pre-deploy-[^\s]+`)
	// KnownGoodSnapRE matches a snapshot the watchdog took after a run of
	// healthy cycles. It is the fallback target when no pre-deploy snapshot
	// survives.
	KnownGoodSnapRE = regexp.MustCompile(`known-good-[^\s]+`)
)

// ExtractLatestSnapshot returns the snapshot to roll back to, given the output
// of `qm listsnapshot`. It prefers the newest pre-deploy snapshot, because that
// is the state immediately before the change under suspicion, and falls back to
// the newest known-good snapshot. It returns the empty string when the guest
// has neither, which leaves the caller with no rollback target.
func ExtractLatestSnapshot(qmOutput []byte) string {
	s := string(qmOutput)
	pre := PreDeploySnapRE.FindAllString(s, -1)
	if len(pre) > 0 {
		return pre[len(pre)-1]
	}
	kg := KnownGoodSnapRE.FindAllString(s, -1)
	if len(kg) > 0 {
		return kg[len(kg)-1]
	}
	return ""
}

// SnapshotsAfter returns snapshot names that appear AFTER targetSnap in
// qm listsnapshot output (they are children/descendants of targetSnap and
// must be deleted before rolling back to it).
// It returns them in the order they appear, which is oldest-to-newest;
// callers should delete in reverse order (newest first).
func SnapshotsAfter(qmOutput []byte, targetSnap string) []string {
	lines := strings.Split(string(qmOutput), "\n")
	var result []string
	past := false
	for _, line := range lines {
		// qm listsnapshot lines look like: ` `-> snapname   timestamp   desc`
		// The name is the first non-space/arrow token after whitespace.
		trimmed := strings.TrimLeft(line, " `->|")
		if trimmed == "" {
			continue
		}
		// Extract just the snapshot name (first field).
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if name == "current" {
			continue
		}
		if !past {
			if name == targetSnap {
				past = true
			}
			continue
		}
		// Only collect watchdog-managed snapshots; never touch user snapshots.
		if PreDeploySnapRE.MatchString(name) || KnownGoodSnapRE.MatchString(name) {
			result = append(result, name)
		}
	}
	return result
}

// parseRollbackStateFile reads the rollback state file and returns its fields
// as they were written. AlreadyDone interprets them, including treating a
// missing file as "no rollback has been attempted".
func parseRollbackStateFile(
	path string,
) (deployTS string, status string, snapshot string, attempts string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// A missing file is the ordinary case on a host that has never
		// rolled back, so only a real read failure is worth a line.
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("rollback: read state file failed", "path", path, "err", err)
		}
		return "", "", "", "", fmt.Errorf("read rollback state %s: %w", path, err)
	}
	kv := make(map[string]string)
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		kv[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	// rollback_done may be "true" (legacy), "done", "failed", or "exhausted"
	st := kv["rollback_done"]
	if st == "true" {
		st = "done"
	}
	return kv["deploy_timestamp"], st, kv["snapshot"], kv["rollback_attempts"], nil
}

// AlreadyDone reports whether a rollback for this deploy timestamp has already
// finished, and how many attempts the state file records. A missing state file
// is not an error: it means no rollback has been attempted, so the caller gets
// false and zero.
//
// A rollback counts as finished when it succeeded or when it exhausted its
// attempts, because retrying an exhausted rollback would loop.
func AlreadyDone(
	statePath string, deployTS int64,
) (done bool, attempts int, err error) {
	ds := strconv.FormatInt(deployTS, 10)
	deployInFile, status, _, attStr, err := parseRollbackStateFile(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, err
	}
	if deployInFile != ds {
		return false, 0, nil
	}
	att, _ := strconv.Atoi(attStr)
	return status == "done" || status == "exhausted", att, nil
}

// WriteState writes the rollback state to a file.
func WriteState(
	path string, deployTS int64, snapshot string,
	attempts int, success bool, timestamp time.Time,
) error {
	status := "failed"
	if success {
		status = "done"
	}
	slog.Info("rollback: WriteState",
		"path", path,
		"deploy_ts", deployTS,
		"snapshot", snapshot,
		"attempts", attempts,
		"status", status)
	content := fmt.Sprintf(
		"deploy_timestamp=%d\nrollback_done=%s\nrollback_timestamp=%d\nsnapshot=%s\nrollback_attempts=%d\n",
		deployTS,
		status,
		timestamp.Unix(),
		snapshot,
		attempts,
	)
	// The watchdog runs as root and is the only reader, so the file needs no
	// group or world access.
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		slog.Error("rollback: write state failed", "path", path, "err", err)
		return fmt.Errorf("write rollback state %s: %w", path, err)
	}
	return nil
}
