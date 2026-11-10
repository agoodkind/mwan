package ifmgr

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

// ForwardingGuard retires acceleration before a writer changes forwarding.
// The writer publishes its result before calling the returned completion.
type ForwardingGuard func(context.Context) (func(), error)

// ForwardingChanges keeps the registered owner stable during each mutation.
// Its zero value permits writers without an acceleration owner.
type ForwardingChanges struct {
	mu               sync.RWMutex
	registration     *forwardingRegistration
	requestReconcile func(string)
}

type forwardingRegistration struct {
	guard ForwardingGuard
}

type (
	forwardingMutationKey  struct{}
	forwardingReconcileKey struct{}
)

type forwardingMutation struct {
	changes *ForwardingChanges
	active  atomic.Bool
}

type forwardingReconcilePass struct {
	active atomic.Bool
}

// BeginForwardingChange also permits roles without an acceleration registry.
func (env *Env) BeginForwardingChange(ctx context.Context) (context.Context, func(), error) {
	var changes *ForwardingChanges
	if env != nil {
		changes = env.ForwardingChanges
	}
	return changes.Begin(ctx)
}

// Register permits one owner; unregistration waits for active writers.
func (changes *ForwardingChanges) Register(guard ForwardingGuard) (func(), error) {
	if changes == nil {
		return nil, fmt.Errorf("forwarding mutation registry is required")
	}
	if guard == nil {
		return nil, fmt.Errorf("forwarding mutation guard is required")
	}
	changes.mu.Lock()
	defer changes.mu.Unlock()
	if changes.registration != nil {
		return nil, fmt.Errorf("forwarding mutation guard already has an owner")
	}
	registration := &forwardingRegistration{guard: guard}
	changes.registration = registration
	var once sync.Once
	return func() {
		once.Do(func() {
			changes.mu.Lock()
			defer changes.mu.Unlock()
			if changes.registration == registration {
				changes.registration = nil
			}
		})
	}, nil
}

// Begin returns a context for nested synchronous writers. Workers must use
// WithoutForwardingChange before starting independent asynchronous work.
func (changes *ForwardingChanges) Begin(ctx context.Context) (mutationContext context.Context, complete func(), resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "forwarding mutation rejected", "err", resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return ctx, nil, fmt.Errorf("begin forwarding mutation: %w", err)
	}
	if changes == nil {
		return ctx, func() {}, nil
	}
	if mutation, ok := ctx.Value(forwardingMutationKey{}).(*forwardingMutation); ok &&
		mutation != nil && mutation.changes == changes && mutation.active.Load() {
		return ctx, func() {}, nil
	}
	changes.mu.RLock()
	locked := true
	defer func() {
		if locked {
			changes.mu.RUnlock()
		}
	}()
	registration := changes.registration
	var release func()
	if registration != nil {
		var err error
		release, err = registration.guard(ctx)
		if err != nil {
			if release != nil {
				release()
			}
			return ctx, nil, fmt.Errorf("prepare forwarding mutation: %w", err)
		}
		if release == nil {
			return ctx, nil, fmt.Errorf("forwarding mutation guard did not supply its completion function")
		}
	}
	if err := ctx.Err(); err != nil {
		if release != nil {
			release()
		}
		return ctx, nil, fmt.Errorf("prepare forwarding mutation: %w", err)
	}
	mutation := &forwardingMutation{changes: changes, active: atomic.Bool{}}
	mutation.active.Store(true)
	ctx = context.WithValue(ctx, forwardingMutationKey{}, mutation)
	var once sync.Once
	finish := func() {
		once.Do(func() {
			defer changes.mu.RUnlock()
			mutation.active.Store(false)
			if release != nil {
				release()
			}
			pass, _ := ctx.Value(forwardingReconcileKey{}).(*forwardingReconcilePass)
			if registration != nil && changes.requestReconcile != nil && (pass == nil || !pass.active.Load()) {
				changes.requestReconcile("owned forwarding mutation completed")
			}
		})
	}
	locked = false
	return ctx, finish, nil
}

// WithoutForwardingChange preserves cancellation while giving an asynchronous
// worker an independent mutation and reconcile lifetime.
func WithoutForwardingChange(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, forwardingMutationKey{}, (*forwardingMutation)(nil))
	return context.WithValue(ctx, forwardingReconcileKey{}, (*forwardingReconcilePass)(nil))
}

func forwardingReconcileContext(ctx context.Context) (context.Context, func()) {
	pass := new(forwardingReconcilePass)
	pass.active.Store(true)
	return context.WithValue(ctx, forwardingReconcileKey{}, pass), func() { pass.active.Store(false) }
}
