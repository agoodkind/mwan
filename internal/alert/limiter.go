package alert

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Coord: coordinates signal delivery during an in-progress rollback
// ---------------------------------------------------------------------------

type Coord struct {
	mu                    sync.Mutex
	rollingBack           bool
	shutdownAfterRollback bool
}

func (c *Coord) SetRollingBack(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollingBack = v
}

func (c *Coord) IsRollingBack() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rollingBack
}

func (c *Coord) OnSignalDuringRollback() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdownAfterRollback = true
}

func (c *Coord) TakeShutdownAfterRollback() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.shutdownAfterRollback
	c.shutdownAfterRollback = false
	return v
}

// ---------------------------------------------------------------------------
// Limiter: prevents alert floods during sustained outages
// ---------------------------------------------------------------------------

type Limiter struct {
	mu                sync.Mutex
	nextPartialSendAt time.Time
	nextTotalSendAt   time.Time
	cooldown          time.Duration
}

func NewLimiter(cooldownSec int) *Limiter {
	return &Limiter{cooldown: time.Duration(cooldownSec) * time.Second}
}

func (a *Limiter) TrySendPartial(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextPartialSendAt) {
		return false
	}
	a.nextPartialSendAt = now.Add(a.cooldown)
	return true
}

func (a *Limiter) TrySendTotal(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextTotalSendAt) {
		return false
	}
	a.nextTotalSendAt = now.Add(a.cooldown)
	return true
}

func (a *Limiter) ResetCooldowns() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextPartialSendAt = time.Time{}
	a.nextTotalSendAt = time.Time{}
}

func (a *Limiter) PartialCooldownRemaining(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextPartialSendAt) {
		return a.nextPartialSendAt.Sub(now)
	}
	return 0
}

func (a *Limiter) TotalCooldownRemaining(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextTotalSendAt) {
		return a.nextTotalSendAt.Sub(now)
	}
	return 0
}
