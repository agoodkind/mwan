package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/renameio/v2"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/notify"
)

type Options struct {
	Config        config.ReportSection
	Network       *networkjson.Config
	Notifications *notify.Manager
	Log           *slog.Logger
}

type pendingDelivery struct {
	Kind       Kind
	Incident   IncidentView
	Start, End time.Time
}
type reportingState struct {
	PeriodStart, PeriodEnd, LastMinute time.Time
	Incident                           incidentState
	Pending                            []pendingDelivery
}

type Service struct {
	settings      config.ReportSection
	store         *Store
	renderer      *Renderer
	notifications *notify.Manager
	log           *slog.Logger
	mu            sync.Mutex
	state         reportingState
	lock          *flock.Flock
}

type deliveryError struct{ err error }

func (failure *deliveryError) Error() string {
	return "report delivery remains pending: " + failure.err.Error()
}
func (failure *deliveryError) Unwrap() error { return failure.err }

func NewService(options Options) (*Service, error) {
	if options.Notifications == nil || options.Log == nil {
		return nil, errors.New("reporting service requires notifications and a logger")
	}
	store, err := OpenStore(options.Config, options.Network)
	if err != nil {
		return nil, err
	}
	renderer, err := NewRenderer(options.Config, options.Network, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	service := &Service{settings: options.Config, store: store, renderer: renderer, notifications: options.Notifications, log: options.Log}
	data, err := os.ReadFile(filepath.Join(options.Config.StateDirectory, "state.json"))
	if err == nil {
		if err := json.Unmarshal(data, &service.state); err != nil {
			return nil, fmt.Errorf("read reporting delivery state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return service, nil
}

func (service *Service) acquire() error {
	if service.lock != nil {
		return nil
	}
	lock := flock.New(filepath.Join(service.settings.StateDirectory, "writer.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("another reporting monitor owns this state directory")
	}
	service.lock = lock
	return nil
}

func (service *Service) save() error {
	data, err := json.Marshal(service.state)
	if err != nil {
		return err
	}
	return renameio.WriteFile(filepath.Join(service.settings.StateDirectory, "state.json"), data, 0o600)
}

func (service *Service) enqueue(kind Kind, incident IncidentView, end time.Time) {
	incident.Affected = slices.Clone(incident.Affected)
	start := service.state.PeriodStart
	if kind != Daily {
		start = end.Add(-24 * time.Hour)
		if incident.Start.After(start) {
			window, _ := time.ParseDuration(service.settings.BreachWindow)
			start = incident.Start.Add(-window)
		}
	}
	service.state.Pending = append(service.state.Pending, pendingDelivery{Kind: kind, Incident: incident, Start: start, End: end})
}

func (service *Service) RecordMinute(ctx context.Context, minute Minute) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.acquire(); err != nil {
		return err
	}
	if !service.state.LastMinute.IsZero() && !minute.End.After(service.state.LastMinute) {
		return errors.New("reporting minute must follow the last recorded interval")
	}
	if err := service.store.Record(ctx, minute); err != nil {
		return err
	}
	if service.state.PeriodStart.IsZero() {
		service.state.PeriodStart = minute.Start
		service.state.PeriodEnd = service.store.periodEnd(minute.Start)
	}
	if err := service.evaluateIncident(minute.End); err != nil {
		return err
	}
	for !minute.End.Before(service.state.PeriodEnd) {
		service.enqueue(Daily, IncidentView{}, service.state.PeriodEnd)
		service.state.PeriodStart = service.state.PeriodEnd
		service.state.PeriodEnd = service.store.periodEnd(service.state.PeriodStart)
	}
	service.state.LastMinute = minute.End
	if err := service.save(); err != nil {
		return err
	}
	if minute.Partial {
		return nil
	}
	return service.deliverPending(ctx)
}

func (service *Service) deliverPending(ctx context.Context) error {
	for len(service.state.Pending) > 0 {
		pending := service.state.Pending[0]
		timeout, _ := time.ParseDuration(service.settings.MailTimeout)
		sendContext, cancel := context.WithTimeout(ctx, timeout)
		message, err := service.renderer.messagePeriod(sendContext, pending.Kind, pending.Incident, pending.Start, pending.End)
		if err == nil {
			err = service.notifications.SendReport(sendContext, message)
		}
		cancel()
		if err != nil {
			service.log.WarnContext(ctx, "report delivery remains pending", "kind", pending.Kind, "error", err)
			return &deliveryError{err: err}
		}
		service.state.Pending = service.state.Pending[1:]
		if err := service.save(); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) Watch(ctx context.Context, collector *Collector) error {
	if collector == nil {
		return errors.New("reporting watch requires a collector")
	}
	service.mu.Lock()
	err := service.acquire()
	service.mu.Unlock()
	if err != nil {
		return err
	}
	queue := make(chan Minute, 8)
	writerErrors := make(chan error, 1)
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var writer sync.WaitGroup
	writer.Go(func() {
		for minute := range queue {
			if err := service.RecordMinute(runContext, minute); err != nil {
				var pending *deliveryError
				if errors.As(err, &pending) {
					service.log.WarnContext(runContext, "report retry scheduled after the next minute", "error", err)
					continue
				}
				writerErrors <- err
				cancel()
				return
			}
		}
	})
	defer writer.Wait()
	defer close(queue)
	step, _ := time.ParseDuration(service.settings.SampleInterval)
	start := time.Now()
	for {
		end := start.Truncate(step).Add(step)
		minute, err := collector.Sample(runContext, start, end)
		if err != nil {
			select {
			case writerError := <-writerErrors:
				return writerError
			default:
			}
			return err
		}
		minute.Partial = end.Sub(start) != step
		select {
		case queue <- minute:
		case err := <-writerErrors:
			return err
		case <-runContext.Done():
			return runContext.Err()
		default:
			return errors.New("reporting minute queue is full; collection cannot discard measurements")
		}
		start = end
	}
}

func (service *Service) SendDaily(ctx context.Context) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	end := service.state.LastMinute
	if end.IsZero() {
		return errors.New("reporting history is empty; run mwan report watch first")
	}
	message, err := service.renderer.Message(ctx, Daily, IncidentView{}, end)
	if err != nil {
		return err
	}
	return service.notifications.SendReport(ctx, message)
}

func (service *Service) Close() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	var lockError error
	if service.lock != nil {
		lockError = service.lock.Close()
		service.lock = nil
	}
	return errors.Join(service.store.Close(), lockError)
}
