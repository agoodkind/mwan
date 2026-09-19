package statuspush

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mdlayher/vsock"

	internalclock "goodkind.io/mwan/internal/clock"
)

// failureLogInterval rate-limits the sender's failure log. A hypervisor
// with no listener fails every cycle, and at a ten-second cadence that is
// over eight thousand identical lines a day for one fact the first line
// already carried.
const failureLogInterval = 5 * time.Minute

// DialFunc opens one connection to the receiver. Production dials vsock; a test
// dials a loopback socket, because vsock needs a hypervisor.
type DialFunc func(ctx context.Context) (net.Conn, error)

// Sender writes one Status per call over a fresh connection. It reports no
// error: the push is advisory, and a probe cycle that had to handle a delivery
// failure would be a probe cycle whose verdict depended on the hypervisor.
//
// The Sender is linux-only because its only caller, the health module, is
// linux-only; a gateway that pushes its status only ever runs there.
type Sender struct {
	dial  DialFunc
	log   *slog.Logger
	clock internalclock.Clock

	mu             sync.Mutex
	lastFailureLog time.Time
	suppressed     int
}

// NewSender returns a Sender that dials the hypervisor at cid on port.
func NewSender(cid uint32, port uint32, log *slog.Logger) *Sender {
	return NewSenderWithDial(func(ctx context.Context) (net.Conn, error) {
		conn, err := vsock.Dial(cid, port, nil)
		if err != nil {
			log.WarnContext(ctx, "statuspush: vsock dial failed",
				"cid", cid, "port", port, "err", err)
			return nil, fmt.Errorf("vsock dial cid %d port %d: %w", cid, port, err)
		}
		return conn, nil
	}, log)
}

// NewSenderWithDial is NewSender with the transport named, so a test drives the
// real encoder and framing over a socket it can create.
func NewSenderWithDial(dial DialFunc, log *slog.Logger) *Sender {
	return &Sender{
		dial:           dial,
		log:            log,
		clock:          internalclock.Real{},
		mu:             sync.Mutex{},
		lastFailureLog: time.Time{},
		suppressed:     0,
	}
}

// Send delivers status, or gives up quietly. Every failure path logs at debug
// under the rate limit and returns.
func (s *Sender) Send(ctx context.Context, status Status) {
	if s == nil || s.dial == nil {
		return
	}
	payload, err := json.Marshal(status)
	if err != nil {
		s.log.WarnContext(ctx, "statuspush: marshal status failed", "err", err)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()

	conn, err := s.dial(sendCtx)
	if err != nil {
		s.noteFailure(ctx, "dial", err)
		return
	}
	defer func() {
		_ = conn.Close()
	}()
	deadline, hasDeadline := sendCtx.Deadline()
	if hasDeadline {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			s.noteFailure(ctx, "set write deadline", err)
			return
		}
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		s.noteFailure(ctx, "write", err)
		return
	}
	s.noteSuccess()
}

// noteFailure logs at most one line per failureLogInterval and counts what it
// swallowed, so the next line says how long the channel has been down.
func (s *Sender) noteFailure(ctx context.Context, operation string, err error) {
	s.mu.Lock()
	now := s.clock.Now()
	suppressed := s.suppressed
	report := s.lastFailureLog.IsZero() ||
		now.Sub(s.lastFailureLog) >= failureLogInterval
	if report {
		s.lastFailureLog = now
		s.suppressed = 0
	} else {
		s.suppressed++
	}
	s.mu.Unlock()

	if !report {
		return
	}
	s.log.DebugContext(
		ctx,
		"statuspush: send failed",
		"operation", operation,
		"suppressed_since_last", suppressed,
		"err", err,
	)
}

// noteSuccess clears the failure window, so a later outage logs its first line
// at once rather than waiting out an interval that started before the recovery.
func (s *Sender) noteSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFailureLog = time.Time{}
	s.suppressed = 0
}
