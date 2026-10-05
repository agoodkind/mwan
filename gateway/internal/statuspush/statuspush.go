// Package statuspush sends provider verdicts from the gateway to the watchdog.
//
// One message is one connection: dial, write a single JSON line, close. There
// is no session to resynchronise after a hypervisor restart, and a hypervisor
// running an older watchdog with no listener costs the gateway one failed dial
// per probe cycle and nothing else.
//
// This file carries no build constraint: the Listener runs on the hypervisor
// watchdog, which is untagged and builds on every release platform. The Sender
// lives in statuspush_sender.go under a linux build tag, because its only
// caller is the linux-only health module; an untagged Sender would be dead
// code in a non-linux watchdog binary, which the deadcode gate correctly
// refuses to ship.
package statuspush

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mdlayher/vsock"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/netif"
)

const (
	// DefaultPort is the watchdog's vsock listener port.
	DefaultPort uint32 = 50053

	// HostCID selects the local hypervisor for vsock connections.
	HostCID uint32 = 2

	transferTimeout = 2 * time.Second

	maxMessageBytes = 64 * 1024
)

// Status stores provider verdicts and a timestamp.
// Interpret ActiveTier only when Providers includes a healthy provider.
type Status struct {
	SentAt     time.Time         `json:"sent_at"`
	ActiveTier uint8             `json:"active_tier"`
	Providers  map[string]string `json:"providers"`
}

// NewStatus converts unrecognized provider states to unknown.
func NewStatus(
	sentAt time.Time,
	members []netif.TierMember,
	states netif.HealthStates,
) Status {
	verdicts := make(netif.HealthStates, len(members))
	for _, member := range members {
		verdict := states.State(member.Name)
		switch verdict {
		case netif.HealthStateHealthy, netif.HealthStateUnhealthy, netif.HealthStateUnknown:
		default:
			verdict = netif.HealthStateUnknown
		}
		verdicts[member.Name] = verdict
	}
	// ActiveTier is meaningful only when at least one provider is healthy.
	activeTier, _ := netif.ActiveTier(members, verdicts)
	return Status{
		SentAt:     sentAt,
		ActiveTier: activeTier,
		Providers:  verdicts,
	}
}

// UnmarshalStatus returns an error when the input is not valid Status JSON.
func UnmarshalStatus(line []byte) (Status, error) {
	var status Status
	if err := json.Unmarshal(line, &status); err != nil {
		slog.Warn("statuspush: unmarshal status failed", "err", err)
		return Status{SentAt: time.Time{}, ActiveTier: 0, Providers: nil},
			fmt.Errorf("decode status: %w", err)
	}
	return status, nil
}

// ListenFunc opens the socket the Listener accepts on.
type ListenFunc func() (net.Listener, error)

// Listener replaces its stored Status after decoding each status message.
type Listener struct {
	listen ListenFunc
	log    *slog.Logger
	clock  internalclock.Clock

	mu       sync.Mutex
	latest   Status
	received time.Time
	seen     bool
	address  string
}

// NewListener configures a vsock listener. Run opens the socket.
func NewListener(port uint32, log *slog.Logger) *Listener {
	return NewListenerWithListen(func() (net.Listener, error) {
		listener, err := vsock.Listen(port, nil)
		if err != nil {
			log.Warn("statuspush: vsock listen failed", "port", port, "err", err)
			return nil, fmt.Errorf("vsock listen port %d: %w", port, err)
		}
		return listener, nil
	}, log)
}

// NewListenerWithListen defers socket creation until Run starts.
func NewListenerWithListen(listen ListenFunc, log *slog.Logger) *Listener {
	return &Listener{
		listen:   listen,
		log:      log,
		clock:    internalclock.Real{},
		mu:       sync.Mutex{},
		latest:   Status{SentAt: time.Time{}, ActiveTier: 0, Providers: nil},
		received: time.Time{},
		seen:     false,
		address:  "",
	}
}

// Latest returns the last decoded status, its receipt time, and whether a status was received.
func (l *Listener) Latest() (Status, time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latest, l.received, l.seen
}

// Address returns an empty string until Run opens the listener socket.
func (l *Listener) Address() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.address
}

// Run accepts connections until ctx is canceled or the listener socket fails.
func (l *Listener) Run(ctx context.Context) error {
	socket, err := l.listen()
	if err != nil {
		l.log.WarnContext(ctx, "statuspush: listen failed", "err", err)
		return fmt.Errorf("statuspush: listen: %w", err)
	}
	l.mu.Lock()
	l.address = socket.Addr().String()
	l.mu.Unlock()

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				l.log.ErrorContext(ctx, "statuspush: shutdown goroutine panic",
					"err", fmt.Errorf("panic: %v", recovered))
			}
		}()
		<-ctx.Done()
		_ = socket.Close()
	}()

	for {
		conn, acceptErr := socket.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("statuspush: listener stopping: %w", ctx.Err())
			}
			l.log.WarnContext(ctx, "statuspush: accept failed", "err", acceptErr)
			return fmt.Errorf("statuspush: accept: %w", acceptErr)
		}
		l.readOne(ctx, conn)
	}
}

// Do not parallelize status reads. Each decoded message replaces Listener.latest.
func (l *Listener) readOne(ctx context.Context, conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()
	if err := conn.SetReadDeadline(l.clock.Now().Add(transferTimeout)); err != nil {
		l.log.WarnContext(ctx, "statuspush: set read deadline failed", "err", err)
		return
	}
	reader := bufio.NewReader(io.LimitReader(conn, maxMessageBytes))
	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		l.log.WarnContext(ctx, "statuspush: read status failed", "err", err)
		return
	}
	if len(line) == 0 {
		return
	}
	status, err := UnmarshalStatus(line)
	if err != nil {
		l.log.WarnContext(ctx, "statuspush: decode status failed", "err", err)
		return
	}
	l.mu.Lock()
	l.latest = status
	l.received = l.clock.Now()
	l.seen = true
	l.mu.Unlock()

	l.log.DebugContext(
		ctx,
		"statuspush: status received",
		"active_tier", status.ActiveTier,
		"provider_count", len(status.Providers),
	)
}
