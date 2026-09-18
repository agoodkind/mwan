package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordedStaleSweeper struct {
	mu           sync.Mutex
	calls        int
	called       chan struct{}
	err          error
	panicOnFirst bool
}

func (s *recordedStaleSweeper) SweepStale(context.Context) error {
	s.mu.Lock()
	s.calls++
	panicOnFirstCall := s.panicOnFirst && s.calls == 1
	s.mu.Unlock()
	s.called <- struct{}{}
	if panicOnFirstCall {
		panic("sweep failed")
	}
	return s.err
}

func (s *recordedStaleSweeper) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type recordedStaleSweepTicker struct {
	channel chan time.Time
	stopped bool
}

func (t *recordedStaleSweepTicker) Channel() <-chan time.Time {
	return t.channel
}

func (t *recordedStaleSweepTicker) Stop() {
	t.stopped = true
}

type recordedStaleSweepClock struct {
	mu       sync.Mutex
	interval time.Duration
	ticker   *recordedStaleSweepTicker
}

func (c *recordedStaleSweepClock) NewTicker(interval time.Duration) staleSweepTicker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interval = interval
	return c.ticker
}

func TestStaleSweepReconcilerCallsSweepStaleOnEachTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sweeper := &recordedStaleSweeper{called: make(chan struct{}, 2)}
	ticker := &recordedStaleSweepTicker{channel: make(chan time.Time, 2)}
	clock := &recordedStaleSweepClock{ticker: ticker}
	reconciler := staleSweepReconciler{
		sweeper: sweeper,
		clock:   clock,
		log:     testLogger(t),
	}
	done := make(chan struct{})
	go func() {
		reconciler.run(ctx)
		close(done)
	}()

	ticker.channel <- time.Time{}
	ticker.channel <- time.Time{}
	for i := 0; i < 2; i++ {
		select {
		case <-sweeper.called:
		case <-time.After(time.Second):
			t.Fatal("periodic stale sweep did not call SweepStale")
		}
	}
	if got, want := sweeper.callCount(), 2; got != want {
		t.Fatalf("SweepStale call count = %d, want %d", got, want)
	}
	if got, want := clock.interval, staleSweepReconcileInterval; got != want {
		t.Fatalf("stale sweep reconcile interval = %s, want %s", got, want)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic stale sweep did not stop after context cancellation")
	}
	if !ticker.stopped {
		t.Fatal("periodic stale sweep did not stop its ticker")
	}
}

func TestStaleSweepReconcilerContinuesAfterSweepError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sweeper := &recordedStaleSweeper{
		called: make(chan struct{}, 2),
		err:    errors.New("list routes failed"),
	}
	ticker := &recordedStaleSweepTicker{channel: make(chan time.Time, 2)}
	clock := &recordedStaleSweepClock{ticker: ticker}
	reconciler := staleSweepReconciler{
		sweeper: sweeper,
		clock:   clock,
		log:     testLogger(t),
	}
	done := make(chan struct{})
	go func() {
		reconciler.run(ctx)
		close(done)
	}()

	ticker.channel <- time.Time{}
	ticker.channel <- time.Time{}
	for i := 0; i < 2; i++ {
		select {
		case <-sweeper.called:
		case <-time.After(time.Second):
			t.Fatal("periodic stale sweep stopped after an error")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic stale sweep did not stop after context cancellation")
	}
}

func TestStaleSweepReconcilerContinuesAfterSweepPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sweeper := &recordedStaleSweeper{
		called:       make(chan struct{}, 2),
		panicOnFirst: true,
	}
	ticker := &recordedStaleSweepTicker{channel: make(chan time.Time, 2)}
	clock := &recordedStaleSweepClock{ticker: ticker}
	reconciler := staleSweepReconciler{
		sweeper: sweeper,
		clock:   clock,
		log:     testLogger(t),
	}
	done := make(chan struct{})
	go func() {
		reconciler.run(ctx)
		close(done)
	}()

	ticker.channel <- time.Time{}
	select {
	case <-sweeper.called:
	case <-time.After(time.Second):
		t.Fatal("periodic stale sweep did not call SweepStale")
	}
	ticker.channel <- time.Time{}
	select {
	case <-sweeper.called:
	case <-time.After(time.Second):
		t.Fatal("periodic stale sweep stopped after a panic")
	}
	if got, want := sweeper.callCount(), 2; got != want {
		t.Fatalf("SweepStale call count = %d, want %d", got, want)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic stale sweep did not stop after context cancellation")
	}
}
