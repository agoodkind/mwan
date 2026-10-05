// Package alert holds the two pieces of watchdog state that outlive a single
// check cycle: how recently an alert of each severity was sent, and whether a
// rollback is in progress. Both are shared between the watchdog loop and the
// signal handler goroutine, so every method takes a lock.
package alert

import (
	"sync"
	"time"
)

// Coord holds a SIGTERM back until an in-progress rollback finishes. A
// rollback stops the gateway VM, restores a snapshot, and starts it again;
// exiting partway through leaves the VM stopped and the site offline. The
// signal handler asks IsRollingBack before it cancels the main context, and
// the rollback asks TakeShutdownAfterRollback once the VM is back up.
//
// The zero value is ready to use.
type Coord struct {
	mu                    sync.Mutex
	rollingBack           bool
	shutdownAfterRollback bool
}

// SetRollingBack marks the rollback as started or finished. The watchdog sets
// it true before it stops the VM and clears it in a deferred call, so an early
// return from the rollback still releases the signal handler.
func (c *Coord) SetRollingBack(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollingBack = v
}

// IsRollingBack reports whether a rollback is in progress. The signal handler
// reads it to decide between exiting now and deferring.
func (c *Coord) IsRollingBack() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rollingBack
}

// OnSignalDuringRollback records that a shutdown signal arrived and was not
// acted on. The signal handler calls it instead of cancelling the main
// context.
func (c *Coord) OnSignalDuringRollback() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdownAfterRollback = true
}

// TakeShutdownAfterRollback reports whether a shutdown was deferred and clears
// the record, so a second rollback in the same process does not exit on a
// signal the first one already consumed.
func (c *Coord) TakeShutdownAfterRollback() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.shutdownAfterRollback
	c.shutdownAfterRollback = false
	return v
}

// Limiter keeps a sustained outage from sending one alert per check cycle. It
// tracks partial loss, where one address family still works, separately from
// total loss, so a total-loss alert is never suppressed by a partial one sent
// moments earlier.
//
// Time arrives as an argument rather than from the wall clock, so the watchdog
// tests drive the cooldown deterministically.
type Limiter struct {
	mu                sync.Mutex
	nextPartialSendAt time.Time
	nextTotalSendAt   time.Time
	cooldown          time.Duration
}

// NewLimiter returns a limiter that allows one alert of each severity per
// cooldown. A cooldown of zero allows every alert, which is what the red-team
// scenarios run with.
func NewLimiter(cooldownSec int) *Limiter {
	// Both send times start as the zero time, which is before any real now,
	// so the first alert of each severity goes out immediately.
	return &Limiter{
		mu:                sync.Mutex{},
		nextPartialSendAt: time.Time{},
		nextTotalSendAt:   time.Time{},
		cooldown:          time.Duration(cooldownSec) * time.Second,
	}
}

// TrySendPartial reports whether a partial-loss alert may be sent now, and
// starts a new cooldown when it may. A false result means the caller must
// suppress the alert rather than retry.
func (a *Limiter) TrySendPartial(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextPartialSendAt) {
		return false
	}
	a.nextPartialSendAt = now.Add(a.cooldown)
	return true
}

// TrySendTotal reports whether a total-loss alert may be sent now, and starts
// a new cooldown when it may. It keeps its own cooldown, independent of the
// partial one.
func (a *Limiter) TrySendTotal(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextTotalSendAt) {
		return false
	}
	a.nextTotalSendAt = now.Add(a.cooldown)
	return true
}

// ResetCooldowns clears both cooldowns, so the next outage alerts immediately
// instead of waiting out a window that started before the recovery.
func (a *Limiter) ResetCooldowns() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextPartialSendAt = time.Time{}
	a.nextTotalSendAt = time.Time{}
}

// PartialCooldownRemaining returns how long the partial cooldown still has to
// run, or zero when an alert may be sent. The watchdog logs it on the line
// that reports the suppression, so the log says why nothing was sent.
func (a *Limiter) PartialCooldownRemaining(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextPartialSendAt) {
		return a.nextPartialSendAt.Sub(now)
	}
	return 0
}

// TotalCooldownRemaining returns how long the total cooldown still has to run,
// or zero when an alert may be sent.
func (a *Limiter) TotalCooldownRemaining(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.nextTotalSendAt) {
		return a.nextTotalSendAt.Sub(now)
	}
	return 0
}
