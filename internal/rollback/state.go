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
	PreDeploySnapRE = regexp.MustCompile(`pre-deploy-[^\s]+`)
	KnownGoodSnapRE = regexp.MustCompile(`known-good-[^\s]+`)
)

// Compatibility aliases (lowercase) for backward compatibility with cmd/mwan
var (
	preDeploySnapRE = PreDeploySnapRE
	knownGoodSnapRE = KnownGoodSnapRE
)

func ExtractLatestSnapshot(qmOutput []byte) string {
	s := string(qmOutput)
	pre := preDeploySnapRE.FindAllString(s, -1)
	if len(pre) > 0 {
		return pre[len(pre)-1]
	}
	kg := knownGoodSnapRE.FindAllString(s, -1)
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
		if preDeploySnapRE.MatchString(name) || knownGoodSnapRE.MatchString(name) {
			result = append(result, name)
		}
	}
	return result
}

func parseRollbackStateFile(
	path string,
) (deployTS string, status string, snapshot string, attempts string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", "", err
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
	return os.WriteFile(path, []byte(content), 0o644)
}
