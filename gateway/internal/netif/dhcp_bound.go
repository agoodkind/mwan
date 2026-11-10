package netif

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
)

type boundLease struct {
	lease            *dhcpLease
	current          LeaseInfo
	state            LeaseState
	noResponse       bool
	retainedDeadline time.Time
	verifyDiscoverAt time.Time
}

// bound retains the assignment until a NAK or its original expiry.
func (c *DHCPClient) bound(
	ctx context.Context, logger *slog.Logger, lease *dhcpLease,
) {
	current := leaseToInfo(LeaseBound, lease.lease, c.clock.Now(), lease.requestedAt)
	if !c.clock.Now().Before(current.ExpiresAt) {
		c.emit(leaseState(LeaseExpired, current.AcquiredAt, errors.New("DHCP lease expired")))
		return
	}
	c.emit(current)
	held := &boundLease{
		lease: lease, current: c.LastLease(), state: LeaseBound, noResponse: false,
		retainedDeadline: time.Time{}, verifyDiscoverAt: time.Time{},
	}
	for {
		now := c.clock.Now()
		if !now.Before(held.current.ExpiresAt) {
			c.emit(leaseState(LeaseExpired, held.current.AcquiredAt, errors.New("DHCP lease expired")))
			return
		}
		deadline := held.retainedDeadline
		held.retainedDeadline = time.Time{}
		if deadline.IsZero() {
			deadline = c.renewalWaitDeadline(now, held.current, held.state, held.noResponse)
		}
		if !held.verifyDiscoverAt.IsZero() && ctx.Err() == nil && now.Before(deadline) {
			if c.continueVerification(ctx, held, deadline) {
				return
			}
			continue
		}
		wait := max(deadline.Sub(c.clock.Now()), 0)
		wake := c.waitForRenewal(ctx, wait)
		if wake == renewalWakeCancelled {
			return
		}
		now = c.clock.Now()
		if !now.Before(held.current.ExpiresAt) {
			continue
		}
		if !c.originMatches() {
			c.refreshExpiryOrigin(ctx)
			c.emit(leaseState(LeaseExpired, held.current.AcquiredAt, errors.New("DHCP interface changed")))
			return
		}
		if wake == renewalWakeReconnected {
			held.verifyDiscoverAt = now.Add(c.cfg.RestartWindow)
			held.retainedDeadline = deadline
			continue
		}
		if c.renewBoundLease(ctx, logger, held, now) {
			return
		}
	}
}

func (c *DHCPClient) continueVerification(ctx context.Context, held *boundLease, deadline time.Time) bool {
	verified, ended, discoverTried := c.verifyAfterReconnect(ctx, held.current, held.verifyDiscoverAt, deadline)
	if ended {
		return true
	}
	if verified != nil {
		held.lease = verified
		c.emit(leaseToInfo(LeaseBound, verified.lease, c.clock.Now(), verified.requestedAt))
		held.current = c.LastLease()
		held.state, held.noResponse = LeaseBound, false
		held.verifyDiscoverAt = time.Time{}
		return false
	}
	if discoverTried {
		active := held.current
		active.State = held.state
		active.Err = nil
		c.emit(active)
		if c.clock.Now().Before(deadline) {
			held.verifyDiscoverAt = time.Time{}
		}
	}
	held.retainedDeadline = deadline
	return false
}

func (c *DHCPClient) renewBoundLease(ctx context.Context, logger *slog.Logger, held *boundLease, now time.Time) bool {
	if !now.Before(held.current.RebindAt) {
		held.state = LeaseRebinding
	} else {
		held.state = LeaseRenewing
	}
	active := held.current
	active.State = held.state
	active.Err = nil
	c.emit(active)
	newLease, err := c.renewAttempt(ctx, held.lease, held.current, held.state, now)
	if errors.Is(err, errDHCPInterfaceChanged) {
		c.refreshExpiryOrigin(ctx)
		c.emit(leaseState(LeaseExpired, held.current.AcquiredAt, err))
		return true
	}
	if err != nil {
		if _, isNAK := errors.AsType[*nclient4.ErrNak](err); isNAK {
			c.emit(leaseState(LeaseExpired, held.current.AcquiredAt, err))
			return true
		}
		logger.WarnContext(ctx, "dhcp: renewal attempt failed", "state", held.state, "err", err)
		active.Err = err
		c.emit(active)
		held.noResponse = errors.Is(err, nclient4.ErrNoResponse) || errors.Is(err, context.DeadlineExceeded)
		return false
	}
	held.noResponse = false
	if err := validateLease(newLease.lease); err != nil {
		logger.WarnContext(ctx, "dhcp: invalid renewal ACK", "err", err)
		active.Err = err
		c.emit(active)
		return false
	}
	next := leaseToInfo(LeaseBound, newLease.lease, c.clock.Now(), newLease.requestedAt)
	if !c.clock.Now().Before(next.ExpiresAt) {
		err := errors.New("renewed DHCP lease expired before the ACK arrived")
		logger.WarnContext(ctx, "dhcp: renewed lease expired", "err", err)
		c.emit(leaseState(LeaseExpired, held.current.AcquiredAt, err))
		return true
	}
	held.lease = newLease
	c.emit(next)
	held.current = c.LastLease()
	held.state = LeaseBound
	held.verifyDiscoverAt = time.Time{}
	return false
}

type renewalWake int

const (
	renewalWakeCancelled renewalWake = iota
	renewalWakeDeadline
	renewalWakeInterfaceChanged
	renewalWakeReconnected
)

func (c *DHCPClient) waitForRenewal(ctx context.Context, wait time.Duration) renewalWake {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	linkWasDown := false
	for {
		select {
		case <-ctx.Done():
			return renewalWakeCancelled
		case <-c.linkChanged:
			if !c.originMatches() {
				return renewalWakeInterfaceChanged
			}
			linkWasDown = c.takeLinkDown() || linkWasDown
			if linkWasDown && c.linkReady() {
				return renewalWakeReconnected
			}
		case <-timer.C:
			if linkWasDown {
				c.LinkDown()
			}
			return renewalWakeDeadline
		}
	}
}

// verifyAfterReconnect verifies a lease after disconnection as RFC 2131
// section 3.7 recommends. It broadcasts INIT-REBOOT until discoverAt, then
// sends one DISCOVER. Each call stops at limit; the caller runs scheduled
// renewal or rebinding and calls again. A NAK or interface change ends the
// lease; silence keeps an unexpired lease.
func (c *DHCPClient) verifyAfterReconnect(ctx context.Context, current LeaseInfo, discoverAt, limit time.Time) (verified *dhcpLease, ended, discoverTried bool) {
	requestUntil := earlier(discoverAt, limit)
	backoff := c.cfg.InitialBackoff
	for c.clock.Now().Before(requestUntil) && ctx.Err() == nil {
		attemptCtx, cancelAttempt := context.WithDeadline(ctx, requestUntil)
		lease, err := c.restartAttempt(attemptCtx, current)
		cancelAttempt()
		if err == nil {
			return lease, false, false
		}
		if errors.Is(err, errDHCPInterfaceChanged) {
			c.refreshExpiryOrigin(ctx)
			c.emit(leaseState(LeaseExpired, current.AcquiredAt, err))
			return nil, true, false
		}
		if _, isNAK := errors.AsType[*nclient4.ErrNak](err); isNAK {
			c.emit(leaseState(LeaseExpired, current.AcquiredAt, err))
			return nil, true, false
		}
		sleepOrCancel(ctx, min(backoff, max(requestUntil.Sub(c.clock.Now()), 0)))
		backoff = nextBackoff(backoff, c.cfg.MaxBackoff)
	}
	if ctx.Err() != nil || !c.clock.Now().Before(limit) {
		return nil, false, false
	}
	acquireCtx, cancelAcquire := context.WithDeadline(ctx, limit)
	defer cancelAcquire()
	acquired, finished := c.recoveryAcquire(acquireCtx, current)
	if acquired != nil {
		return acquired, false, true
	}
	return nil, finished, true
}

func (c *DHCPClient) renewalWaitDeadline(now time.Time, current LeaseInfo, state LeaseState, noResponse bool) time.Time {
	deadline := current.RenewAt
	if state != LeaseBound {
		wait := c.cfg.RenewTimeout
		if noResponse {
			boundary := current.RebindAt
			if state == LeaseRebinding {
				boundary = current.ExpiresAt
			}
			wait = max(boundary.Sub(now)/2, dhcpMinimumRetransmitWait)
		}
		deadline = now.Add(wait)
	}
	deadline = earlier(deadline, current.ExpiresAt)
	if state != LeaseRebinding {
		deadline = earlier(deadline, current.RebindAt)
	}
	return deadline
}
