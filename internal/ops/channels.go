// Package ops is the watchdog's access to everything outside its own process:
// the guest agent, the hypervisor's `qm` command, and the Proxmox REST API.
// The SysOps interface is the whole surface, so a test or a fault injector can
// stand in for all of it.
//
// Reaching the guest has three independent channels, tried in order, because
// the failures this daemon handles are exactly the ones that take a channel
// away. ChannelTracker records which ones are working so an alert can say so.
package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ChannelName identifies one of the ways the watchdog reaches the gateway VM.
// The value is what the health summary and the log lines print, so it is
// stable rather than an index.
type ChannelName string

const (
	// ChanVsock is the hypervisor-local virtio socket to the guest agent. It
	// works while the guest has no working network.
	ChanVsock ChannelName = "vsock"
	// ChanTCP is the gRPC connection to the guest agent over the management
	// network.
	ChanTCP ChannelName = "tcp_mgmt"
	// ChanPVE is the Proxmox REST API, which acts on the VM from outside
	// rather than talking to anything inside it.
	ChanPVE ChannelName = "pve_rest"
)

type channelHealth struct {
	lastSuccess      time.Time
	lastFailure      time.Time
	lastError        string
	consecutiveFails int
	healthy          bool
}

// ChannelTracker records the last outcome on each channel so an alert can
// report which paths to the guest still work. That distinction is what tells
// an operator whether the guest is unreachable or merely off the network.
//
// It is written from the watchdog loop and read when an alert is rendered, so
// every method takes the lock.
type ChannelTracker struct {
	mu       sync.Mutex
	channels map[ChannelName]*channelHealth
	now      func() time.Time
}

// NewChannelTracker returns a tracker on the wall clock.
func NewChannelTracker() *ChannelTracker {
	return NewChannelTrackerWithClock(time.Now)
}

// NewChannelTrackerWithClock returns a tracker that reads time from now, which
// lets a test assert on the recorded timestamps. A nil now falls back to the
// wall clock rather than panicking on first use.
func NewChannelTrackerWithClock(now func() time.Time) *ChannelTracker {
	if now == nil {
		now = time.Now
	}
	// Every channel is present from the start, so recordSuccess and
	// recordFailure can index the map without checking.
	return &ChannelTracker{
		mu: sync.Mutex{},
		channels: map[ChannelName]*channelHealth{
			ChanVsock: {},
			ChanTCP:   {},
			ChanPVE:   {},
		},
		now: now,
	}
}

func (t *ChannelTracker) recordSuccess(ch ChannelName) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.channels[ch]
	h.lastSuccess = t.now()
	h.consecutiveFails = 0
	h.lastError = ""
	h.healthy = true
}

func (t *ChannelTracker) recordFailure(ch ChannelName, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.channels[ch]
	h.lastFailure = t.now()
	h.consecutiveFails++
	if err != nil {
		h.lastError = err.Error()
	}
	h.healthy = false
}

// Summary returns a multi-line human-readable status of all three channels
// for inclusion in alert emails.
func (t *ChannelTracker) Summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sb strings.Builder
	for _, name := range []ChannelName{ChanVsock, ChanTCP, ChanPVE} {
		h := t.channels[name]
		status := "NEVER_USED"
		if !h.lastSuccess.IsZero() || !h.lastFailure.IsZero() {
			if h.healthy {
				status = fmt.Sprintf("OK  (last_success=%s)", h.lastSuccess.Format(time.RFC3339))
			} else {
				status = fmt.Sprintf("FAIL(consecutive=%d last_err=%q last_failure=%s)",
					h.consecutiveFails, h.lastError, h.lastFailure.Format(time.RFC3339))
			}
		}
		fmt.Fprintf(&sb, "  %-10s %s\n", name, status)
	}
	return sb.String()
}

// LogAll emits one slog line per channel at DEBUG level.
func (t *ChannelTracker) LogAll(ctx context.Context, log *slog.Logger) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range []ChannelName{ChanVsock, ChanTCP, ChanPVE} {
		h := t.channels[name]
		log.DebugContext(ctx, "channel health",
			"channel", name,
			"healthy", h.healthy,
			"consecutive_fails", h.consecutiveFails,
			"last_error", h.lastError,
			"last_success", h.lastSuccess,
			"last_failure", h.lastFailure,
		)
	}
}
