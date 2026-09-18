package netif

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
)

// DHCPConfig configures the async DHCPv4 client.
type DHCPConfig struct {
	Iface           string        // Interface to bind on (e.g. "mbrains")
	InitialBackoff  time.Duration // First retry delay after a failure
	MaxBackoff      time.Duration // Cap on retry backoff
	DiscoverTimeout time.Duration // Per-attempt deadline for DISCOVER
	RequestTimeout  time.Duration // Per-attempt deadline for REQUEST
	RenewTimeout    time.Duration // Per-attempt deadline for RENEW
}

// LeaseState is the simplified RFC 2131 client state machine.
type LeaseState int

// LeaseState values mirror the DHCPv4 client state transitions we emit.
const (
	LeaseInit LeaseState = iota
	LeaseSelecting
	LeaseRequesting
	LeaseBound
	LeaseRenewing
	LeaseExpired
)

// String returns the stable log-friendly name of the lease state.
func (s LeaseState) String() string {
	switch s {
	case LeaseInit:
		return "INIT"
	case LeaseSelecting:
		return "SELECTING"
	case LeaseRequesting:
		return "REQUESTING"
	case LeaseBound:
		return "BOUND"
	case LeaseRenewing:
		return "RENEWING"
	case LeaseExpired:
		return "EXPIRED"
	default:
		return "UNKNOWN"
	}
}

// LeaseInfo is one snapshot of the DHCP client state. The daemon consumes
// LeaseInfo events from DHCPClient.Events and reconciles kernel state
// (address on iface, default route in oob table).
type LeaseInfo struct {
	State      LeaseState
	IP         net.IP        // YourIPAddr from ACK; CIDR prefix from SubnetMask
	PrefixLen  int           // bits of subnet mask; 0 when unknown
	Gateway    net.IP        // first router from option 3; nil if absent
	Server     net.IP        // DHCP server identifier
	LeaseTime  time.Duration // option 51
	AcquiredAt time.Time     // ACK reception time
	Err        error         // populated when State is non-bound and we hit an error
}

// String returns a compact representation suitable for log fields.
func (l LeaseInfo) String() string {
	if l.IP == nil {
		return fmt.Sprintf("state=%s err=%v", l.State, l.Err)
	}
	return fmt.Sprintf("state=%s ip=%s/%d gw=%v server=%v lease=%s",
		l.State, l.IP, l.PrefixLen, l.Gateway, l.Server, l.LeaseTime)
}

// DHCPClient runs a long-lived DHCPv4 state machine in its own goroutine
// and emits LeaseInfo on Events whenever the state changes.
type DHCPClient struct {
	cfg   DHCPConfig
	log   *slog.Logger
	clock clock
	mu    sync.Mutex
	last  LeaseInfo

	Events chan LeaseInfo
}

// StartDHCPClient returns a DHCPClient running in its own goroutine.
// Cancel ctx to stop it. The first event is emitted as soon as the
// initial DORA succeeds (or as INIT/Expired if the bind fails).
func StartDHCPClient(
	ctx context.Context, log *slog.Logger, cfg DHCPConfig,
) *DHCPClient {
	if cfg.InitialBackoff == 0 {
		cfg.InitialBackoff = 5 * time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.DiscoverTimeout == 0 {
		cfg.DiscoverTimeout = 10 * time.Second
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	if cfg.RenewTimeout == 0 {
		cfg.RenewTimeout = 10 * time.Second
	}

	c := &DHCPClient{
		cfg:   cfg,
		log:   log.With("component", "dhcp", "iface", cfg.Iface),
		clock: realClock{},
		mu:    sync.Mutex{},
		last: LeaseInfo{
			State:      LeaseInit,
			IP:         nil,
			PrefixLen:  0,
			Gateway:    nil,
			Server:     nil,
			LeaseTime:  0,
			AcquiredAt: time.Time{},
			Err:        nil,
		},
		Events: make(chan LeaseInfo, 8),
	}
	go func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			c.log.ErrorContext(ctx, "dhcp: run panicked", "err", fmt.Sprint(recovered))
		}()
		c.run(ctx)
	}()
	return c
}

// LastLease returns the most recently observed LeaseInfo, or zero value if
// none yet. Useful for status endpoints / health checks.
func (c *DHCPClient) LastLease() LeaseInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *DHCPClient) emit(info LeaseInfo) {
	c.mu.Lock()
	c.last = info
	c.mu.Unlock()
	c.log.Debug("dhcp: state transition", "info", info.String())
	select {
	case c.Events <- info:
	default:
		c.log.Warn("dhcp: events channel full, dropping update",
			"info", info.String())
	}
}

func (c *DHCPClient) run(ctx context.Context) {
	logger := c.log.With("goroutine", "dhcp")
	backoff := c.cfg.InitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		lease, err := c.acquire(ctx)
		if err != nil {
			logger.WarnContext(ctx, "dhcp: acquire failed; will retry",
				"err", err, "backoff", backoff.String())
			c.emit(LeaseInfo{
				State:      LeaseSelecting,
				IP:         nil,
				PrefixLen:  0,
				Gateway:    nil,
				Server:     nil,
				LeaseTime:  0,
				AcquiredAt: time.Time{},
				Err:        err,
			})
			sleepOrCancel(ctx, backoff)
			backoff = nextBackoff(backoff, c.cfg.MaxBackoff)
			continue
		}
		backoff = c.cfg.InitialBackoff

		// Schedule renewal at T1 (lease/2).
		c.bound(ctx, logger, lease)
	}
}

// acquire performs full DORA. On success returns a Lease; on failure
// returns wrapped error.
func (c *DHCPClient) acquire(ctx context.Context) (*nclient4.Lease, error) {
	c.emit(LeaseInfo{
		State:      LeaseInit,
		IP:         nil,
		PrefixLen:  0,
		Gateway:    nil,
		Server:     nil,
		LeaseTime:  0,
		AcquiredAt: time.Time{},
		Err:        nil,
	})

	client, err := nclient4.New(
		c.cfg.Iface,
		nclient4.WithTimeout(c.cfg.DiscoverTimeout),
		nclient4.WithLogger(newSlogDHCPLogger(c.log)),
	)
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: nclient4.New failed", "err", err)
		return nil, fmt.Errorf("nclient4.New: %w", err)
	}
	defer client.Close()

	c.log.DebugContext(ctx, "dhcp: DISCOVER")
	c.emit(LeaseInfo{
		State:      LeaseSelecting,
		IP:         nil,
		PrefixLen:  0,
		Gateway:    nil,
		Server:     nil,
		LeaseTime:  0,
		AcquiredAt: time.Time{},
		Err:        nil,
	})

	dctx, cancel := context.WithTimeout(ctx, c.cfg.DiscoverTimeout)
	offer, err := client.DiscoverOffer(dctx)
	cancel()
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: DiscoverOffer failed", "err", err)
		return nil, fmt.Errorf("DiscoverOffer: %w", err)
	}

	c.log.DebugContext(
		ctx, "dhcp: OFFER received",
		"yiaddr", offer.YourIPAddr.String(),
		"siaddr", offer.ServerIdentifier(),
	)
	c.emit(LeaseInfo{
		State:      LeaseRequesting,
		IP:         nil,
		PrefixLen:  0,
		Gateway:    nil,
		Server:     nil,
		LeaseTime:  0,
		AcquiredAt: time.Time{},
		Err:        nil,
	})

	rctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	lease, err := client.RequestFromOffer(rctx, offer)
	cancel()
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: RequestFromOffer failed", "err", err)
		return nil, fmt.Errorf("RequestFromOffer: %w", err)
	}
	c.log.DebugContext(
		ctx, "dhcp: ACK received",
		"yiaddr", lease.ACK.YourIPAddr.String(),
		"lease_time", lease.ACK.IPAddressLeaseTime(0).String(),
	)
	return lease, nil
}

// bound sits in BOUND, schedules renewal at T1, and on renewal failure
// returns to allow the caller to start over.
func (c *DHCPClient) bound(
	ctx context.Context, logger *slog.Logger, lease *nclient4.Lease,
) {
	current := leaseToInfo(LeaseBound, lease, c.clock.Now(), nil)
	c.emit(current)

	leaseTime := lease.ACK.IPAddressLeaseTime(0)
	if leaseTime <= 0 {
		logger.WarnContext(ctx, "dhcp: lease time missing or zero; defaulting to 1h")
		leaseTime = time.Hour
	}
	t1 := leaseTime / 2

	for {
		logger.DebugContext(ctx, "dhcp: scheduling renewal",
			"in", t1.String(), "lease_time", leaseTime.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(t1):
		}

		c.emit(LeaseInfo{
			State:      LeaseRenewing,
			IP:         current.IP,
			PrefixLen:  current.PrefixLen,
			Gateway:    current.Gateway,
			Server:     current.Server,
			LeaseTime:  current.LeaseTime,
			AcquiredAt: current.AcquiredAt,
			Err:        nil,
		})

		client, err := nclient4.New(
			c.cfg.Iface,
			nclient4.WithTimeout(c.cfg.RenewTimeout),
			nclient4.WithLogger(newSlogDHCPLogger(c.log)),
		)
		if err != nil {
			logger.WarnContext(ctx, "dhcp: nclient4.New for renew failed", "err", err)
			c.emit(LeaseInfo{
				State:      LeaseExpired,
				IP:         nil,
				PrefixLen:  0,
				Gateway:    nil,
				Server:     nil,
				LeaseTime:  0,
				AcquiredAt: current.AcquiredAt,
				Err:        err,
			})
			return
		}
		rctx, cancel := context.WithTimeout(ctx, c.cfg.RenewTimeout)
		newLease, err := client.Renew(rctx, lease)
		cancel()
		client.Close()
		if err != nil {
			var nakErr *nclient4.ErrNak
			if errors.As(err, &nakErr) {
				logger.WarnContext(ctx, "dhcp: server NAK on renew; restarting DORA",
					"err", err)
			} else {
				logger.WarnContext(ctx, "dhcp: renew failed; lease expiring",
					"err", err)
			}
			c.emit(LeaseInfo{
				State:      LeaseExpired,
				IP:         nil,
				PrefixLen:  0,
				Gateway:    nil,
				Server:     nil,
				LeaseTime:  0,
				AcquiredAt: current.AcquiredAt,
				Err:        err,
			})
			return
		}
		lease = newLease
		current = leaseToInfo(LeaseBound, lease, c.clock.Now(), nil)
		c.emit(current)
		leaseTime = lease.ACK.IPAddressLeaseTime(0)
		if leaseTime <= 0 {
			leaseTime = time.Hour
		}
		t1 = leaseTime / 2
	}
}

// leaseToInfo extracts daemon-relevant fields from a DHCPv4 ACK.
// Pure function for unit-testing.
func leaseToInfo(
	state LeaseState, lease *nclient4.Lease, acquired time.Time, err error,
) LeaseInfo {
	info := LeaseInfo{
		State:      state,
		IP:         nil,
		PrefixLen:  0,
		Gateway:    nil,
		Server:     nil,
		LeaseTime:  0,
		AcquiredAt: acquired,
		Err:        err,
	}
	if lease == nil || lease.ACK == nil {
		return info
	}
	ack := lease.ACK
	info.IP = ack.YourIPAddr
	mask := ack.SubnetMask()
	if mask != nil {
		info.PrefixLen, _ = mask.Size()
	}
	if routers := ack.Router(); len(routers) > 0 {
		info.Gateway = routers[0]
	}
	info.Server = ack.ServerIdentifier()
	info.LeaseTime = ack.IPAddressLeaseTime(0)
	return info
}

func nextBackoff(cur, maxB time.Duration) time.Duration {
	n := cur * 2
	if n > maxB {
		return maxB
	}
	return n
}

// slogDHCPWriter forwards nclient4 text logs into slog at DEBUG.
type slogDHCPWriter struct{ base *slog.Logger }

// Write implements [io.Writer] for the standard [log.Logger] used by nclient4.
func (w slogDHCPWriter) Write(bytes []byte) (int, error) {
	w.base.Debug("dhcp: " + strings.TrimSpace(string(bytes)))
	return len(bytes), nil
}

// slogDHCPLogger adapts slog to the nclient4.Logger interface so DHCP
// packet exchanges appear in our structured logs at DEBUG.
type slogDHCPLogger struct {
	*log.Logger
	base *slog.Logger
}

func newSlogDHCPLogger(base *slog.Logger) slogDHCPLogger {
	return slogDHCPLogger{
		Logger: log.New(slogDHCPWriter{base: base}, "", 0),
		base:   base,
	}
}

// PrintMessage implements nclient4.Logger.
func (l slogDHCPLogger) PrintMessage(prefix string, message *dhcpv4.DHCPv4) {
	l.base.Debug(
		"dhcp: packet",
		"dir", prefix,
		"type", message.MessageType().String(),
		"yiaddr", message.YourIPAddr.String(),
		"server", message.ServerIdentifier(),
	)
}
