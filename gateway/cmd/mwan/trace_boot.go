package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// bootIDPath is the kernel's per-boot identifier, the stable component of the
// boot trace id.
const bootIDPath = "/proc/sys/kernel/random/boot_id"

// runTraceBoot gives the boot a stable trace id: it writes
// /run/mwan-trace-id and /var/lib/mwan/trace-id and logs the id, so events
// across services correlate end to end. It replaces mwan-trace-boot.sh and
// runs from mwan-trace-boot.service early in boot.
func runTraceBoot() int {
	raw, err := os.ReadFile(bootIDPath)
	bootID := "unknown"
	if err == nil {
		cleaned := strings.TrimSpace(string(raw))
		if len(cleaned) >= 8 {
			bootID = cleaned[:8]
		}
	} else {
		slog.Warn("read boot id failed", "path", bootIDPath, "err", err)
	}
	traceID := fmt.Sprintf("%s-boot-%s", time.Now().Format("20060102-150405"), bootID)
	if writeErr := writeTraceID(traceID); writeErr != nil {
		slog.Error("write boot trace id failed", "err", writeErr)
		fmt.Fprintf(os.Stderr, "mwan trace-boot: %v\n", writeErr)
		return 1
	}
	slog.Info("boot trace id written", "trace_id", traceID)
	fmt.Println("traceId=" + traceID)
	return 0
}
