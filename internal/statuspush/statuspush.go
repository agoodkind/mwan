// Package statuspush carries the gateway's provider verdict to the hypervisor
// watchdog. The gateway is the only process that probes every provider, so the
// watchdog is told what it needs rather than made to rediscover it by pinging
// through a list of interface names typed into its own configuration.
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
)

const (
	// DefaultPort is the vsock port the hypervisor watchdog listens on and the
	// gateway dials. It sits beside the agent's 50051 and its TCP fallback
	// 50052, which are the only other ports in this family.
	DefaultPort uint32 = 50053

	// HostCID is VMADDR_CID_HOST, the context id every guest reaches its own
	// hypervisor on. A guest therefore needs no knowledge of which machine it
	// runs on to find the watchdog.
	HostCID uint32 = 2

	// transferTimeout bounds one push and one receive. Both ends are on the
	// same machine over a virtio transport, so a transfer that has not finished
	// in this long is not going to.
	transferTimeout = 2 * time.Second

	// maxMessageBytes caps one status line. The real message is a few hundred
	// bytes; the cap stops a wedged or hostile writer from growing the read
	// buffer without bound.
	maxMessageBytes = 64 * 1024
)

// Status is what the gateway knows about its providers at one instant. When no
// provider is healthy anywhere, ActiveTier carries no meaning and every entry
// in Providers reads unhealthy, which is what a reader checks.
type Status struct {
	SentAt     time.Time         `json:"sent_at"`
	ActiveTier uint8             `json:"active_tier"`
	Providers  map[string]string `json:"providers"`
}

// ListenFunc opens the socket the Listener accepts on.
type ListenFunc func() (net.Listener, error)

// Listener keeps the most recent Status the gateway pushed, with the time it
// arrived. Nothing is queued: a diagnosis wants what is true now, and an older
// status is never the answer.
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

// NewListener returns a Listener bound to this host's vsock context on port.
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

// NewListenerWithListen is NewListener with the socket named, so a test drives
// the real accept loop and decoder over a socket it can create.
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

// Latest returns the last status received, when it arrived, and whether one
// ever has.
func (l *Listener) Latest() (Status, time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latest, l.received, l.seen
}

// Address reports the socket the listener bound, once Run has bound it. It is
// empty before that and on a listener whose socket failed.
func (l *Listener) Address() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.address
}

// Run accepts connections until ctx is cancelled or the socket fails. The
// caller runs it in a goroutine and treats a returned error as the channel
// being down, not as a reason to stop watching connectivity.
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

// readOne reads one status line and records it. Connections are served one at a
// time on purpose: a push is one short line, and a serial loop means the stored
// status is the last one that arrived rather than whichever goroutine won a
// race. The read deadline keeps a client that connects and says nothing from
// holding the loop.
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
	var status Status
	if err := json.Unmarshal(line, &status); err != nil {
		// A rejected line leaves the stored status alone. The watchdog would
		// rather diagnose against a slightly older verdict than against none.
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
