package vpp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gofrs/flock"
)

func (controller *Controller) lockJournal(ctx context.Context) (result *flock.Flock, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "lock VPP ownership journal failed", "error", failure)
		}
	}()
	lock := flock.New(controller.settings.StateFile + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock VPP ownership journal: %w", errors.Join(err, lock.Close()))
	}
	if !locked {
		return nil, errors.Join(fmt.Errorf("another VPP controller owns %s", controller.settings.StateFile), lock.Close())
	}
	return lock, nil
}

func (controller *Controller) refreshGeneration(ctx context.Context, state *journal) error {
	generation, err := socketGeneration(ctx, controller.settings.Endpoints.APISocket)
	if err != nil {
		return err
	}
	if state.Generation == generation {
		return nil
	}
	state.Generation = generation
	state.Objects = nil
	state.Pending = nil
	state.TAPs = nil
	state.NAT64Enabled = false
	state.NAT64Owned = false
	state.NAT64EnablePending = false
	return nil
}
