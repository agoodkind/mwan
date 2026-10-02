package rollback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Coordinator serializes all VM recovery operations across processes.
type Coordinator struct {
	file *os.File
}

// Owns verifies that an open coordinator uses the configured recovery lock.
func (coordinator *Coordinator) Owns(markerPath string) bool {
	return coordinator != nil && coordinator.file != nil && coordinator.file.Name() == markerPath+".coordination"
}

// Acquire serializes recovery independently of the existing recovery marker.
// The advisory lock file must remain present because unlinking it permits a
// second process to lock a different inode during recovery.
func Acquire(ctx context.Context, markerPath string, pollInterval time.Duration) (*Coordinator, error) {
	if !filepath.IsAbs(markerPath) || pollInterval <= 0 {
		return nil, fmt.Errorf("rollback coordination requires an absolute marker path and positive polling interval")
	}
	file, err := os.OpenFile(markerPath+".coordination", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		slog.WarnContext(ctx, "rollback coordinator open failed")
		return nil, fmt.Errorf("open rollback coordinator: %w", err)
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Coordinator{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			closeErr := file.Close()
			slog.WarnContext(ctx, "rollback coordinator lock failed")
			return nil, errors.Join(fmt.Errorf("lock rollback coordinator: %w", err), closeErr)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			closeErr := file.Close()
			return nil, errors.Join(ctx.Err(), closeErr)
		case <-timer.C:
		}
	}
}

// Close releases the advisory lock without unlinking its persistent inode.
func (coordinator *Coordinator) Close() error {
	err := unix.Flock(int(coordinator.file.Fd()), unix.LOCK_UN)
	closeErr := coordinator.file.Close()
	coordinator.file = nil
	if err != nil || closeErr != nil {
		slog.Warn("rollback coordinator release failed")
		return errors.Join(err, closeErr)
	}
	return nil
}
