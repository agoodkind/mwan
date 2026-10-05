// Package clock provides the canonical wall-clock seam for production code.
package clock

import "time"

// Clock is the canonical wall-clock seam injected into time-sensitive code.
type Clock interface {
	Now() time.Time
}

// Real reads the wall clock in production callers.
type Real struct{}

// Now returns the current wall-clock time.
func (Real) Now() time.Time {
	return time.Now()
}
