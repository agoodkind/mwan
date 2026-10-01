package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/mdlayher/ndp"
	"golang.org/x/net/ipv6"
)

// RAClient sends Router Solicitations and reads Router Advertisements on
// one interface, in-process via mdlayher/ndp. Replaces the prior shellout
// to /usr/bin/rdisc6.
//
// The connection joins the all-routers multicast group (ff02::2) on the
// interface's link-local source address at construction. Operations are
// bounded by per-call timeouts; the connection itself is long-lived and
// must be Close()d on shutdown.
//
// Capabilities required: CAP_NET_RAW (raw ICMPv6 socket).
type RAClient struct {
	iface   *net.Interface
	conn    *ndp.Conn
	linkLoc netip.Addr
	log     *slog.Logger
	clock   clock
	mu      sync.Mutex // serialises SolicitRA so concurrent callers don't fight on the conn
}

// NewRAClient opens a raw ICMPv6 connection on iface, finds (or waits for)
// the interface's link-local address, and joins the all-routers multicast
// group. Returns a usable RAClient ready to SolicitRA. Caller must Close.
func NewRAClient(iface string, log *slog.Logger) (client *RAClient, err error) {
	log = log.With("component", "ra", "iface", iface)
	log.Debug("ra: NewRAClient entry")
	defer func() {
		if err != nil {
			log.Warn("ra: listener setup failed", "operation", "listen", "result", "failed")
		}
	}()

	netIface, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("InterfaceByName(%q): %w", iface, err)
	}
	log.Debug("ra: InterfaceByName ok",
		"index", netIface.Index, "mtu", netIface.MTU)

	// ndp.Listen takes the netip.Addr we want to bind as source. Pass the
	// link-local; the library will wait for one to appear if the iface is
	// brought up just before this call.
	conn, ll, err := ndp.Listen(netIface, ndp.LinkLocal)
	if err != nil {
		return nil, fmt.Errorf("ndp.Listen(%q, link-local): %w", iface, err)
	}
	log.Debug("ra: ndp.Listen ok", "link_local", ll.String())
	if err := conn.SetControlMessage(ipv6.FlagHopLimit, true); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("observe RA hop limit: %w", err)
	}

	// Join the all-routers multicast group so we receive RouterAdvertisements
	// even when they are sent unsolicited (router flooding the LAN).
	allRouters := netip.MustParseAddr("ff02::2")
	if err := conn.JoinGroup(allRouters); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("JoinGroup(ff02::2): %w", err)
	}
	log.Debug("ra: joined all-routers multicast group")
	if err := conn.JoinGroup(netip.MustParseAddr("ff02::1")); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("JoinGroup(ff02::1): %w", err)
	}

	return &RAClient{
		iface:   netIface,
		conn:    conn,
		linkLoc: ll,
		log:     log,
		clock:   realClock{},
		mu:      sync.Mutex{},
	}, nil
}

// SolicitRA sends one Router Solicitation and waits up to timeout for a
// Router Advertisement. Returns the first RA received, or an error if
// the deadline elapses or the socket fails. On timeout, returns the
// wrapped [context.DeadlineExceeded] so callers can [errors.Is] the case.
//
// Concurrent callers serialise on a mutex: the underlying ndp.Conn is
// not goroutine-safe.
func (c *RAClient) SolicitRA(
	ctx context.Context, timeout time.Duration,
) (*ndp.RouterAdvertisement, error) {
	advertisement, _, err := c.ReceiveRA(ctx, timeout, true)
	return advertisement, err
}

// ReceiveRA reads an advertisement and its router, optionally sending a solicitation.
func (c *RAClient) ReceiveRA(ctx context.Context, timeout time.Duration, solicit bool) (*ndp.RouterAdvertisement, netip.Addr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	op := c.log.With("op", "SolicitRA", "timeout_ms", timeout.Milliseconds())
	op.DebugContext(ctx, "ra: SolicitRA entry")

	startTime := c.clock.Now()
	deadline := startTime.Add(timeout)
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, netip.Addr{}, fmt.Errorf("SetReadDeadline: %w", err)
	}

	rs := &ndp.RouterSolicitation{
		Options: []ndp.Option{
			&ndp.LinkLayerAddress{
				Direction: ndp.Source,
				Addr:      c.iface.HardwareAddr,
			},
		},
	}
	allRouters := netip.MustParseAddr("ff02::2")

	if solicit {
		if err := c.conn.WriteTo(rs, nil, allRouters); err != nil {
			return nil, netip.Addr{}, fmt.Errorf("send RS: %w", err)
		}
		op.DebugContext(ctx, "ra: RouterSolicitation sent", "to", allRouters.String())
	}

	for {
		select {
		case <-ctx.Done():
			ctxErr := ctx.Err()
			return nil, netip.Addr{}, fmt.Errorf("ctx cancelled while waiting for RA: %w", ctxErr)
		default:
		}
		msg, control, from, err := c.conn.ReadFrom()
		dur := c.clock.Now().Sub(startTime)
		if err != nil {
			// Treat netConn timeout as DeadlineExceeded for the caller.
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				if solicit {
					c.log.WarnContext(ctx, "ra: deadline exceeded waiting for RA",
						"op", "SolicitRA", "timeout_ms", timeout.Milliseconds(),
						"waited_ms", dur.Milliseconds())
				}
				return nil, netip.Addr{}, context.DeadlineExceeded
			}
			return nil, netip.Addr{}, fmt.Errorf("read RA: %w", err)
		}
		op.DebugContext(
			ctx,
			"ra: ICMPv6 message received",
			"from", from.String(),
			"msg_type", fmt.Sprintf("%T", msg),
			"waited_ms", dur.Milliseconds(),
		)
		ra, ok := msg.(*ndp.RouterAdvertisement)
		if !ok || !from.IsLinkLocalUnicast() || control == nil || control.HopLimit != ndp.HopLimit {
			op.DebugContext(ctx, "ra: not a RA, continuing wait")
			continue
		}
		op.DebugContext(
			ctx,
			"ra: RouterAdvertisement received",
			"from", from.String(),
			"router_lifetime_s", ra.RouterLifetime.Seconds(),
			"managed_config", ra.ManagedConfiguration,
			"other_config", ra.OtherConfiguration,
		)
		return ra, from.WithZone(""), nil
	}
}

// LinkLocal returns the link-local source address bound to this client.
// Diagnostic accessor for callers that want to log or report it.
func (c *RAClient) LinkLocal() netip.Addr { return c.linkLoc }

// Close releases the underlying ICMPv6 socket. Idempotent.
func (c *RAClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	c.log.Debug("ra: Close entry")
	err := c.conn.Close()
	c.conn = nil
	if err != nil {
		c.log.Warn("ra: Close failed", "err", err)
		return fmt.Errorf("close RA client: %w", err)
	}
	return nil
}
