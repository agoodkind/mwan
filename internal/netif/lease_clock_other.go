//go:build !linux

package netif

import (
	"errors"
	"time"
)

func leaseBootTime() (time.Duration, error) {
	return 0, errors.New("lease recovery boot clock requires Linux")
}
