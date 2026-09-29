package netif

import (
	"bytes"
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
	ClientID        []byte        // Complete DHCP option 61 payload
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
	LeaseRebinding
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
	case LeaseRebinding:
		return "REBINDING"
	case LeaseExpired:
		return "EXPIRED"
	default:
		return "UNKNOWN"
	}
}

// LeaseRoute is one route accepted from a DHCPv4 ACK.
type LeaseRoute struct {
	Destination *net.IPNet
	Gateway     net.IP
}

// LeaseInfo is one snapshot of the DHCP client state.
type LeaseInfo struct {
	State             LeaseState
	LinkIndex         int
	LinkHardwareAddr  net.HardwareAddr
	InvalidationEpoch uint64
	IP                net.IP // YourIPAddr from ACK; CIDR prefix from SubnetMask
	PrefixLen         int    // bits of subnet mask; 0 when unknown
	Gateway           net.IP // default router from option 121 or option 3
	Routes            []LeaseRoute
	Server            net.IP        // DHCP server identifier
	LeaseTime         time.Duration // option 51
	AcquiredAt        time.Time     // ACK reception time
	RenewAt           time.Time
	RebindAt          time.Time
	ExpiresAt         time.Time
	Err               error // populated when State is non-bound and we hit an error
}

func leaseState(state LeaseState, acquired time.Time, err error) LeaseInfo {
	var info LeaseInfo
	info.State = state
	info.AcquiredAt = acquired
	info.Err = err
	return info
}

// MatchesLink reports whether this lease originated on the current interface.
func (l LeaseInfo) MatchesLink(iface string) (bool, error) {
	link, err := net.InterfaceByName(iface)
	if err != nil {
		slog.Warn("dhcp: interface lookup failed", "iface", iface, "err", err)
		return false, fmt.Errorf("find DHCP interface %s: %w", iface, err)
	}
	return l.LinkIndex > 0 && l.LinkIndex == link.Index &&
		bytes.Equal(l.LinkHardwareAddr, link.HardwareAddr), nil
}

// String returns a compact representation suitable for log fields.
func (l LeaseInfo) String() string {
	if l.IP == nil {
		return fmt.Sprintf("state=%s err=%v", l.State, l.Err)
	}
	return fmt.Sprintf("state=%s ip=%s/%d gw=%v server=%v lease=%s",
		l.State, l.IP, l.PrefixLen, l.Gateway, l.Server, l.LeaseTime)
}

// DHCPClient runs a long-lived DHCPv4 state machine in its own goroutine.
// Events publishes the current assignment or its withdrawal.
type DHCPClient struct {
	cfg    DHCPConfig
	log    *slog.Logger
	clock  clock
	mu     sync.Mutex
	last   LeaseInfo
	origin LeaseInfo
	epoch  uint64

	Events chan LeaseInfo
}

var errDHCPInterfaceChanged = errors.New("DHCP interface changed during renewal")

// StartDHCPClient returns a DHCPClient running in its own goroutine.
// Cancel ctx to stop it. The first assignment event follows a valid ACK.
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
	cfg.ClientID = append([]byte(nil), cfg.ClientID...)

	c := &DHCPClient{
		cfg:    cfg,
		log:    log.With("component", "dhcp", "iface", cfg.Iface),
		clock:  realClock{},
		mu:     sync.Mutex{},
		last:   leaseState(LeaseInit, time.Time{}, nil),
		origin: leaseState(LeaseInit, time.Time{}, nil),
		epoch:  0,
		Events: make(chan LeaseInfo, 1),
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
	last := c.last
	last.LinkHardwareAddr = append(net.HardwareAddr(nil), last.LinkHardwareAddr...)
	return last
}

func (c *DHCPClient) emit(info LeaseInfo) {
	c.mu.Lock()
	if info.State == LeaseExpired {
		c.epoch++
	}
	info.InvalidationEpoch = c.epoch
	info.LinkIndex = c.origin.LinkIndex
	info.LinkHardwareAddr = append(net.HardwareAddr(nil), c.origin.LinkHardwareAddr...)
	c.last = info
	if info.State == LeaseBound || info.State == LeaseRenewing ||
		info.State == LeaseRebinding || info.State == LeaseExpired {
		select {
		case c.Events <- info:
		default:
			select {
			case <-c.Events:
			default:
			}
			select {
			case c.Events <- info:
			default:
			}
		}
	}
	c.mu.Unlock()
	c.log.Debug("dhcp: state transition", "info", info.String())
}

func (c *DHCPClient) captureOrigin() error {
	link, err := net.InterfaceByName(c.cfg.Iface)
	if err != nil {
		c.log.Warn("dhcp: interface lookup failed", "err", err)
		return fmt.Errorf("find DHCP interface: %w", err)
	}
	c.mu.Lock()
	c.origin.LinkIndex = link.Index
	c.origin.LinkHardwareAddr = append(net.HardwareAddr(nil), link.HardwareAddr...)
	c.mu.Unlock()
	return nil
}

func (c *DHCPClient) originMatches() bool {
	c.mu.Lock()
	origin := c.origin
	c.mu.Unlock()
	matches, err := origin.MatchesLink(c.cfg.Iface)
	return err == nil && matches
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
			c.emit(leaseState(LeaseSelecting, time.Time{}, err))
			sleepOrCancel(ctx, backoff)
			backoff = nextBackoff(backoff, c.cfg.MaxBackoff)
			continue
		}
		backoff = c.cfg.InitialBackoff

		c.bound(ctx, logger, lease)
	}
}

// acquire performs full DORA. On success returns a Lease; on failure
// returns wrapped error.
func (c *DHCPClient) acquire(ctx context.Context) (*nclient4.Lease, error) {
	c.mu.Lock()
	c.origin = leaseState(LeaseInit, time.Time{}, nil)
	c.mu.Unlock()
	if err := c.captureOrigin(); err != nil {
		return nil, fmt.Errorf("DHCP interface: %w", err)
	}
	c.emit(leaseState(LeaseInit, time.Time{}, nil))

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
	if !c.originMatches() || !bytes.Equal(client.InterfaceAddr(), c.origin.LinkHardwareAddr) {
		return nil, errors.New("DHCP interface changed during socket open")
	}

	c.log.DebugContext(ctx, "dhcp: DISCOVER")
	c.emit(leaseState(LeaseSelecting, time.Time{}, nil))

	dctx, cancel := context.WithTimeout(ctx, c.cfg.DiscoverTimeout)
	offer, err := client.DiscoverOffer(dctx, c.requestModifiers()...)
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
	c.emit(leaseState(LeaseRequesting, time.Time{}, nil))

	rctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	lease, err := client.RequestFromOffer(rctx, offer, c.requestModifiers()...)
	cancel()
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: RequestFromOffer failed", "err", err)
		return nil, fmt.Errorf("RequestFromOffer: %w", err)
	}
	if err := validateLease(lease); err != nil {
		return nil, fmt.Errorf("invalid DHCP ACK: %w", err)
	}
	if !c.originMatches() {
		return nil, errors.New("DHCP interface changed during acquisition")
	}
	c.log.DebugContext(
		ctx, "dhcp: ACK received",
		"yiaddr", lease.ACK.YourIPAddr.String(),
		"lease_time", lease.ACK.IPAddressLeaseTime(0).String(),
	)
	return lease, nil
}

// bound retains the assignment until a NAK or its original expiry.
func (c *DHCPClient) bound(
	ctx context.Context, logger *slog.Logger, lease *nclient4.Lease,
) {
	current := leaseToInfo(LeaseBound, lease, c.clock.Now())
	c.emit(current)
	current = c.LastLease()
	state := LeaseBound
	for {
		now := c.clock.Now()
		if !now.Before(current.ExpiresAt) {
			c.emit(leaseState(LeaseExpired, current.AcquiredAt, errors.New("DHCP lease expired")))
			return
		}
		deadline := c.renewalWaitDeadline(now, current, state)
		wait := max(deadline.Sub(c.clock.Now()), 0)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		now = c.clock.Now()
		if !now.Before(current.ExpiresAt) {
			continue
		}
		if !c.originMatches() {
			c.emit(leaseState(LeaseExpired, current.AcquiredAt, errors.New("DHCP interface changed")))
			return
		}
		if !now.Before(current.RebindAt) {
			state = LeaseRebinding
		} else {
			state = LeaseRenewing
		}
		active := current
		active.State = state
		active.Err = nil
		c.emit(active)
		newLease, err := c.renewAttempt(ctx, lease, current, state, now)
		if errors.Is(err, errDHCPInterfaceChanged) {
			c.emit(leaseState(LeaseExpired, current.AcquiredAt, err))
			return
		}
		if err != nil {
			if _, isNAK := errors.AsType[*nclient4.ErrNak](err); isNAK {
				c.emit(leaseState(LeaseExpired, current.AcquiredAt, err))
				return
			}
			logger.WarnContext(ctx, "dhcp: renewal attempt failed", "state", state, "err", err)
			active.Err = err
			c.emit(active)
			continue
		}
		if err := validateLease(newLease); err != nil {
			logger.WarnContext(ctx, "dhcp: invalid renewal ACK", "err", err)
			active.Err = err
			c.emit(active)
			continue
		}
		lease = newLease
		current = leaseToInfo(LeaseBound, lease, c.clock.Now())
		c.emit(current)
		current = c.LastLease()
		state = LeaseBound
	}
}

func (c *DHCPClient) renewalWaitDeadline(now time.Time, current LeaseInfo, state LeaseState) time.Time {
	deadline := current.RenewAt
	if state != LeaseBound {
		deadline = now.Add(c.cfg.RenewTimeout)
	}
	deadline = earlier(deadline, current.ExpiresAt)
	if state != LeaseRebinding {
		deadline = earlier(deadline, current.RebindAt)
	}
	return deadline
}

func (c *DHCPClient) renewAttempt(
	ctx context.Context, lease *nclient4.Lease, current LeaseInfo, state LeaseState, now time.Time,
) (*nclient4.Lease, error) {
	options := []nclient4.ClientOpt{
		nclient4.WithTimeout(c.cfg.RenewTimeout),
		nclient4.WithLogger(newSlogDHCPLogger(c.log)),
	}
	if state == LeaseRenewing {
		options = append(
			options,
			nclient4.WithUnicast(&net.UDPAddr{IP: current.IP, Port: nclient4.ClientPort}),
			nclient4.WithServerAddr(&net.UDPAddr{IP: current.Server, Port: nclient4.ServerPort}),
		)
	}
	client, err := nclient4.New(c.cfg.Iface, options...)
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: renewal socket open failed", "err", err)
		return nil, fmt.Errorf("open DHCP renewal client: %w", err)
	}
	defer client.Close()
	if !c.originMatches() || !bytes.Equal(client.InterfaceAddr(), current.LinkHardwareAddr) {
		return nil, errDHCPInterfaceChanged
	}
	deadline := earlier(now.Add(c.cfg.RenewTimeout), current.ExpiresAt)
	if state == LeaseRenewing {
		deadline = earlier(deadline, current.RebindAt)
	}
	rctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var renewed *nclient4.Lease
	if state == LeaseRebinding {
		renewed, err = c.rebind(rctx, client, lease)
	} else {
		renewed, err = client.Renew(rctx, lease, c.requestModifiers()...)
	}
	if !c.originMatches() {
		return nil, errDHCPInterfaceChanged
	}
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: renewal request failed", "err", err)
		return nil, fmt.Errorf("DHCP renewal request: %w", err)
	}
	return renewed, nil
}

func earlier(a, b time.Time) time.Time {
	if a.After(b) {
		return b
	}
	return a
}

func (c *DHCPClient) rebind(ctx context.Context, client *nclient4.Client, lease *nclient4.Lease) (*nclient4.Lease, error) {
	modifiers := append([]dhcpv4.Modifier{dhcpv4.WithBroadcast(true)}, c.requestModifiers()...)
	request, err := dhcpv4.NewRenewFromAck(lease.ACK, modifiers...)
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: rebinding request build failed", "err", err)
		return nil, fmt.Errorf("build DHCP rebinding request: %w", err)
	}
	response, err := client.SendAndRead(ctx, nclient4.DefaultServers, request,
		nclient4.IsMessageType(dhcpv4.MessageTypeAck, dhcpv4.MessageTypeNak))
	if err != nil {
		c.log.WarnContext(ctx, "dhcp: rebinding response read failed", "err", err)
		return nil, fmt.Errorf("read DHCP rebinding response: %w", err)
	}
	if response.MessageType() == dhcpv4.MessageTypeNak {
		return nil, &nclient4.ErrNak{Offer: lease.Offer, Nak: response}
	}
	return &nclient4.Lease{Offer: response, ACK: response, CreationTime: c.clock.Now()}, nil
}

func (c *DHCPClient) requestModifiers() []dhcpv4.Modifier {
	modifiers := []dhcpv4.Modifier{requestClasslessRouteFirst}
	if len(c.cfg.ClientID) > 0 {
		modifiers = append(modifiers, dhcpv4.WithOption(dhcpv4.OptClientIdentifier(c.cfg.ClientID)))
	}
	return modifiers
}

func requestClasslessRouteFirst(packet *dhcpv4.DHCPv4) {
	requested := dhcpv4.OptionCodeList{dhcpv4.OptionClasslessStaticRoute}
	for _, code := range packet.ParameterRequestList() {
		requested.Add(code)
	}
	packet.UpdateOption(dhcpv4.OptParameterRequestList(requested...))
}

// leaseToInfo extracts daemon-relevant fields from a DHCPv4 ACK.
// Pure function for unit-testing.
func leaseToInfo(state LeaseState, lease *nclient4.Lease, acquired time.Time) LeaseInfo {
	info := leaseState(state, acquired, nil)
	if lease == nil || lease.ACK == nil {
		return info
	}
	ack := lease.ACK
	info.IP = ack.YourIPAddr
	mask := ack.SubnetMask()
	if mask != nil {
		info.PrefixLen, _ = mask.Size()
	}
	if ack.Options.Has(dhcpv4.OptionClasslessStaticRoute) {
		info.Routes, _ = classlessRoutes(ack)
		for _, route := range info.Routes {
			if ones, _ := route.Destination.Mask.Size(); ones == 0 {
				info.Gateway = route.Gateway
				break
			}
		}
	} else if routers := ack.Router(); len(routers) > 0 {
		info.Gateway = routers[0]
		info.Routes = []LeaseRoute{{
			Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			Gateway:     routers[0],
		}}
	}
	info.Server = ack.ServerIdentifier()
	info.LeaseTime = ack.IPAddressLeaseTime(0)
	t1 := ack.IPAddressRenewalTime(0)
	t2 := ack.IPAddressRebindingTime(0)
	if t1 <= 0 || t1 >= info.LeaseTime {
		t1 = info.LeaseTime / 2
	}
	if t2 <= t1 || t2 >= info.LeaseTime {
		t2 = info.LeaseTime * 7 / 8
	}
	if t1 >= t2 {
		t1 = info.LeaseTime / 2
		t2 = info.LeaseTime * 7 / 8
	}
	info.RenewAt = acquired.Add(t1)
	info.RebindAt = acquired.Add(t2)
	info.ExpiresAt = acquired.Add(info.LeaseTime)
	return info
}

func validateLease(lease *nclient4.Lease) error {
	if lease == nil || lease.ACK == nil {
		return errors.New("missing DHCP ACK")
	}
	ack := lease.ACK
	address := ack.YourIPAddr.To4()
	if address == nil || address.IsUnspecified() || address.IsMulticast() || address.Equal(net.IPv4bcast) {
		slog.Warn("dhcp: invalid ACK address", "address", ack.YourIPAddr)
		return fmt.Errorf("invalid assigned address %v", ack.YourIPAddr)
	}
	mask := ack.SubnetMask()
	if mask == nil {
		return errors.New("missing subnet mask")
	}
	if _, bits := mask.Size(); bits != net.IPv4len*8 {
		return errors.New("invalid subnet mask")
	}
	server := ack.ServerIdentifier().To4()
	if server == nil || server.IsUnspecified() || server.IsMulticast() || server.Equal(net.IPv4bcast) {
		slog.Warn("dhcp: invalid ACK server identifier", "server", ack.ServerIdentifier())
		return fmt.Errorf("invalid server identifier %v", ack.ServerIdentifier())
	}
	if ack.IPAddressLeaseTime(0) <= 0 {
		return errors.New("missing or zero lease time")
	}
	if ack.Options.Has(dhcpv4.OptionClasslessStaticRoute) {
		if _, err := classlessRoutes(ack); err != nil {
			return err
		}
	}
	return nil
}

func classlessRoutes(ack *dhcpv4.DHCPv4) ([]LeaseRoute, error) {
	data := ack.Options.Get(dhcpv4.OptionClasslessStaticRoute)
	if len(data) == 0 {
		return nil, errors.New("empty classless route option")
	}
	var parsed dhcpv4.Routes
	if err := parsed.FromBytes(data); err != nil {
		slog.Warn("dhcp: invalid ACK classless routes", "err", err)
		return nil, fmt.Errorf("invalid classless route option: %w", err)
	}
	routes := make([]LeaseRoute, 0, len(parsed))
	for _, route := range parsed {
		destination := &net.IPNet{
			IP:   route.Dest.IP.Mask(route.Dest.Mask),
			Mask: append(net.IPMask(nil), route.Dest.Mask...),
		}
		routes = append(routes, LeaseRoute{
			Destination: destination,
			Gateway:     append(net.IP(nil), route.Router...),
		})
	}
	return routes, nil
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
