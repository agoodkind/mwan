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

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/notify"
)

// Options supplies reporting settings and the shared delivery services.
type Options struct {
	Config        config.ReportSection
	Network       *networkjson.Config
	Notifications *notify.Manager
	Log           *slog.Logger
}

type pendingDelivery struct {
	Kind     Kind         `json:"Kind"`
	Incident IncidentView `json:"Incident"`
	Start    time.Time    `json:"Start"`
	End      time.Time    `json:"End"`
}
type reportingState struct {
	PeriodStart time.Time         `json:"PeriodStart"`
	PeriodEnd   time.Time         `json:"PeriodEnd"`
	LastMinute  time.Time         `json:"LastMinute"`
	Incident    incidentState     `json:"Incident"`
	Pending     []pendingDelivery `json:"Pending"`
}

// Service records observations and manages report delivery and incidents.
type Service struct {
	settings      config.ReportSection
	store         *Store
	renderer      *Renderer
	notifications *notify.Manager
	log           *slog.Logger
	mu            sync.Mutex
	state         reportingState
	lock          *flock.Flock
	clock         clock.Clock
}

type deliveryError struct{ err error }

func (failure *deliveryError) Error() string {
	return "report delivery remains pending: " + failure.err.Error()
}
func (failure *deliveryError) Unwrap() error { return failure.err }

// NewService opens reporting history and restores pending delivery state.
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
	service := new(Service)
	service.settings = options.Config
	service.store = store
	service.renderer = renderer
	service.notifications = options.Notifications
	service.log = options.Log
	service.clock = clock.Real{}
	data, err := os.ReadFile(filepath.Join(options.Config.StateDirectory, "state.json"))
	if err == nil {
		if err := json.Unmarshal(data, &service.state); err != nil {
			service.log.Warn("reporting delivery state is invalid", "error", err)
			return nil, fmt.Errorf("read reporting delivery state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		service.log.Warn("reporting delivery state read failed", "error", err)
		return nil, fmt.Errorf("read reporting delivery state: %w", err)
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
		service.log.Warn("reporting writer lock failed", "error", err)
		return fmt.Errorf("acquire reporting writer lock: %w", err)
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
		service.log.Warn("reporting delivery state encoding failed", "error", err)
		return fmt.Errorf("encode reporting delivery state: %w", err)
	}
	if err := renameio.WriteFile(filepath.Join(service.settings.StateDirectory, "state.json"), data, 0o600); err != nil {
		service.log.Warn("reporting delivery state save failed", "error", err)
		return fmt.Errorf("save reporting delivery state: %w", err)
	}
	return nil
}

func (service *Service) enqueue(kind Kind, incident IncidentView, end time.Time) {
	incident.Affected = slices.Clone(incident.Affected)
	incident.Signals = slices.Clone(incident.Signals)
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

// RecordMinute stores one interval and evaluates scheduled reports and alerts.
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
		var incident IncidentView
		history, err := service.store.history(service.state.PeriodStart, service.state.PeriodEnd)
		if err != nil {
			return err
		}
		if len(history) > 0 {
			service.enqueue(Daily, incident, service.state.PeriodEnd)
		} else {
			service.log.WarnContext(ctx, "daily reporting period did not contain observations", "start", service.state.PeriodStart, "end", service.state.PeriodEnd)
		}
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

// Watch collects intervals while a separate writer renders and sends reports.
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
	defer func() {
		cancel()
		close(queue)
		writer.Wait()
	}()
	step, _ := time.ParseDuration(service.settings.SampleInterval)
	start := service.clock.Now()
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
			return fmt.Errorf("reporting collection stopped: %w", runContext.Err())
		default:
			return errors.New("reporting minute queue is full; collection cannot discard measurements")
		}
		start = end
	}
}

// SendDaily sends the current summary without closing its scheduled period.
func (service *Service) SendDaily(ctx context.Context) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	end := service.state.LastMinute
	if end.IsZero() {
		return errors.New("reporting history is empty; run mwan report watch first")
	}
	var incident IncidentView
	message, err := service.renderer.Message(ctx, Daily, incident, end)
	if err != nil {
		return err
	}
	if err := service.notifications.SendReport(ctx, message); err != nil {
		service.log.WarnContext(ctx, "daily report delivery failed", "error", err)
		return fmt.Errorf("send daily report: %w", err)
	}
	return nil
}

// Close releases the writer lock and closes reporting history.
func (service *Service) Close() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	var lockError error
	if service.lock != nil {
		lockError = service.lock.Close()
		service.lock = nil
	}
	err := errors.Join(service.store.Close(), lockError)
	if err != nil {
		service.log.Warn("reporting close failed", "error", err)
	}
	return err
}
