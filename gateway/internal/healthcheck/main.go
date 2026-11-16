// Package healthcheck runs configured WAN probes without daemon state or alerts.
package healthcheck

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"goodkind.io/mwan/internal/ifmgr/modules/health"
)

// Run serializes records while independent WAN workers use their configured intervals.
func Run(ctx context.Context, options Options, logger *slog.Logger, emit func(Record) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	records := make(chan Record)
	completed := make(chan error, len(options.WANs))
	var workers sync.WaitGroup

	for _, wan := range options.WANs {
		workers.Go(func() {
			failure := runWAN(ctx, wan, options, logger, records)
			if failure != nil {
				logger.WarnContext(
					ctx,
					"health worker stopped",
					"wan",
					wan.Key(),
					"interface",
					wan.Iface,
					"err",
					failure,
				)
				cancel()
			}

			completed <- failure
		})
	}

	var outputError error
	var workerError error

	for remaining := len(options.WANs); remaining > 0; {
		select {
		case failure := <-completed:
			remaining--
			workerError = errors.Join(workerError, failure)
		case record := <-records:
			if outputError == nil {
				outputError = emit(record)
				if outputError != nil {
					logger.WarnContext(ctx, "health output failed", "err", outputError)
					cancel()
				}
			}
		}
	}

	workers.Wait()

	return errors.Join(outputError, workerError)
}

func runWAN(
	ctx context.Context,
	wan health.WAN,
	options Options,
	logger *slog.Logger,
	records chan<- Record,
) error {
	for ctx.Err() == nil {
		if err := probeWAN(ctx, wan, options, logger, records); err != nil {
			return err
		}

		if !options.Watch {
			return nil
		}

		timer := time.NewTimer(wan.CheckInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}

	return nil
}
