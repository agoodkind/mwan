package deployoperation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/renameio/v2"
	"github.com/google/uuid"
	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/observation"
	"goodkind.io/mwan/internal/rollback"
)

// Status restricts grants and recovery to the current operation lifecycle.
type Status string

const (
	// Armed permits mutations only with a verified watch and current observations.
	Armed Status = "armed"
	// Recovering denies new grants while prior writes drain.
	Recovering Status = "recovering"
	// Recovered requires an actual restored identity and application replies.
	Recovered Status = "recovered"
	// RecoveryFailed permits only exact-operation recovery retries.
	RecoveryFailed Status = "recovery_failed"
	// Committed ends the current generation after target verification.
	Committed Status = "committed"
	// Disarmed ends a generation that granted no mutation lease, without restoration.
	Disarmed Status = "disarmed"
)

// Identity binds the recovery pair to one machine and records its actual boot.
type Identity struct {
	MachineID        string `json:"machine_id"`
	BootID           string `json:"boot_id"`
	ExecutableSHA256 string `json:"executable_sha256"`
	NetworkSHA256    string `json:"network_sha256"`
	RuntimeSHA256    string `json:"runtime_sha256"`
}

// Lease fences one mutation until explicit release or its runtime deadline.
type Lease struct {
	ID        string    `json:"id"`
	Phase     string    `json:"phase"`
	StartedAt time.Time `json:"started_at,omitzero"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Record persists the current generation separately from archived terminal records.
type Record struct {
	Manifest
	Status           Status               `json:"status"`
	Lease            *Lease               `json:"lease,omitempty"`
	Watch            WatchIdentity        `json:"watch"`
	Reason           string               `json:"reason,omitempty"`
	UpdatedAt        time.Time            `json:"updated_at"`
	RestoredIdentity *Identity            `json:"restored_identity,omitempty"`
	Results          []observation.Result `json:"results,omitempty"`
	ObservedAt       time.Time            `json:"observed_at"`
}

// Store serializes record transactions independently of the VM recovery coordinator.
type Store struct {
	Path         string
	PollInterval time.Duration
	Clock        clock.Clock
}

// Read rejects malformed records rather than treating them as absent operations.
func (store Store) Read(ctx context.Context) (Record, error) {
	var record Record
	err := store.locked(ctx, func() error {
		loaded, loadErr := store.read()
		record = loaded
		return loadErr
	})
	return record, err
}

// Create archives terminal records and rejects unresolved or reused identities.
func (store Store) Create(ctx context.Context, record Record) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "deployment operation creation failed")
		}
	}()
	return store.locked(ctx, func() error {
		_, err := os.Stat(store.Path)
		if err == nil {
			previous, readErr := store.read()
			if readErr != nil {
				return readErr
			}
			if previous.Status == Armed || previous.Status == Recovering || previous.Status == RecoveryFailed {
				return fmt.Errorf("an unresolved deploy operation already exists")
			}
			if previous.OperationID == record.OperationID || previous.Generation == record.Generation {
				return fmt.Errorf("deploy operation identity cannot be reused")
			}
			if archiveErr := store.archive(previous); archiveErr != nil {
				return archiveErr
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect deploy operation: %w", err)
		}
		if record.Status != Armed || record.Lease != nil || record.Generation == "" {
			return fmt.Errorf("new deploy operation must be armed without a lease")
		}
		record.UpdatedAt = store.Clock.Now().UTC()
		if !record.Deadline.After(record.UpdatedAt) {
			return fmt.Errorf("new deploy operation deadline has expired")
		}
		return store.write(record)
	})
}

// Grant requires a verified watch and fresh application observations before mutation.
func (store Store) Grant(ctx context.Context, operationID, generation, phase string, deadline time.Time) (Lease, error) {
	var lease Lease
	err := store.change(ctx, operationID, generation, func(record *Record) error {
		now := store.Clock.Now()
		if record.Status != Armed || !record.Deadline.After(now) {
			return fmt.Errorf("deploy operation does not permit mutations")
		}
		if err := record.Watch.Verify(ctx); err != nil {
			slog.WarnContext(ctx, "deployment mutation lease watch verification failed")
			return fmt.Errorf("mutation lease requires the active deploy watch: %w", err)
		}
		if !record.MutationReady(now) {
			return fmt.Errorf("mutation lease requires fresh passing application observations")
		}
		if record.Lease != nil {
			return fmt.Errorf("deploy operation already has an outstanding lease")
		}
		if strings.TrimSpace(phase) == "" || !deadline.After(now) || deadline.After(record.Deadline) {
			return fmt.Errorf("mutation lease requires a phase and deadline within the operation deadline")
		}
		for _, interruption := range record.ExpectedInterruptions {
			if interruption.Phase == phase && deadline.After(now.Add(time.Duration(interruption.MaxSeconds)*time.Second)) {
				return fmt.Errorf("mutation lease exceeds the expected interruption bound for phase %s", phase)
			}
		}
		lease = Lease{ID: rand.Text(), Phase: phase, StartedAt: now.UTC(), ExpiresAt: deadline}
		record.Lease = &lease
		return nil
	})
	return lease, err
}

// RegisterWatch binds the service invocation to the actual registering process.
func (store Store) RegisterWatch(ctx context.Context, operationID, generation, unit string) error {
	identity, err := ReadWatch(ctx, unit)
	if err != nil {
		return err
	}
	if int64(identity.PID) != int64(os.Getpid()) {
		return fmt.Errorf("deploy watch PID does not match the registering process")
	}
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status != Armed || record.Watch.PID != 0 {
			return fmt.Errorf("deploy watch cannot register for this operation")
		}
		record.Watch = identity
		return nil
	})
}

// Release requires the exact generation and outstanding lease identity.
func (store Store) Release(ctx context.Context, operationID, generation, leaseID string) error {
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status != Armed && record.Status != Recovering {
			return fmt.Errorf("terminal deploy operation rejects lease release")
		}
		if record.Lease == nil || record.Lease.ID != leaseID {
			return fmt.Errorf("mutation lease identity does not match")
		}
		record.Lease = nil
		return nil
	})
}

// BeginRecovery denies further grants and permits exact-operation failure retries.
func (store Store) BeginRecovery(ctx context.Context, operationID, generation, reason string) error {
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status == Recovering {
			return nil
		}
		if record.Status != Armed && record.Status != RecoveryFailed {
			return fmt.Errorf("terminal deploy operation rejects recovery")
		}
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("deploy recovery requires a reason")
		}
		record.Status = Recovering
		record.Reason = reason
		return nil
	})
}

// Disarm ends an armed operation that has no mutation lease. The gateway keeps its current disk.
func (store Store) Disarm(ctx context.Context, operationID, generation, reason string) error {
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status != Armed {
			return fmt.Errorf("deploy operation with status %s cannot disarm", record.Status)
		}
		if record.Lease != nil {
			return fmt.Errorf("deploy operation with a mutation lease cannot disarm")
		}
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("deploy disarm requires a reason")
		}
		record.Status = Disarmed
		record.Reason = reason
		return nil
	})
}

func (store Store) finishRecovery(ctx context.Context, operationID, generation string, restored *Identity, reason string) error {
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status != Recovering {
			return fmt.Errorf("deploy operation is not recovering")
		}
		record.Status = RecoveryFailed
		if restored != nil {
			if record.Lease != nil && record.Lease.ExpiresAt.After(store.Clock.Now()) {
				return fmt.Errorf("deploy recovery cannot finish before mutation lease release or expiry")
			}
			if err := restored.verifyRestored(record.Baseline); err != nil {
				return err
			}
			record.Status = Recovered
			record.Lease = nil
		}
		record.RestoredIdentity = restored
		record.Reason = reason
		return nil
	})
}

// Commit excludes mutation grants throughout target and application verification.
func (store Store) Commit(ctx context.Context, operationID, generation string, verify func(Record) ([]observation.Result, error)) error {
	var commitErr error
	transactionErr := store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status != Armed || record.Lease != nil || !record.Deadline.After(store.Clock.Now()) {
			return fmt.Errorf("deploy operation cannot commit with an outstanding lease or expired deadline")
		}
		results, err := verify(*record)
		if err != nil {
			return err
		}
		record.Results = results
		record.ObservedAt = store.Clock.Now().UTC()
		if !record.Deadline.After(store.Clock.Now()) {
			commitErr = fmt.Errorf("deploy operation deadline expired during commit verification")
			return nil
		}
		if err := record.Watch.Verify(ctx); err != nil {
			slog.WarnContext(ctx, "deployment commit watch verification failed")
			commitErr = fmt.Errorf("deploy commit requires the active deploy watch: %w", err)
			return nil
		}
		if !record.MutationReady(store.Clock.Now()) {
			commitErr = fmt.Errorf("deploy commit requires fresh passing application observations")
			return nil
		}
		record.Status = Committed
		return nil
	})
	return errors.Join(transactionErr, commitErr)
}

// MutationReady requires complete fresh application replies for every configured check.
func (record Record) MutationReady(now time.Time) bool {
	if record.Status != Armed || record.Watch.PID == 0 || !record.Deadline.After(now) {
		return false
	}
	return checksPassed(record.RequiredChecks, record.Results, now)
}

func checksPassed(checks []observation.CheckSpec, results []observation.Result, now time.Time) bool {
	if len(checks) == 0 || len(checks) != len(results) {
		return false
	}
	for index, check := range checks {
		if !observation.RequiredPassed(check, results[index], now) {
			return false
		}
	}
	return true
}

func (store Store) observe(ctx context.Context, operationID, generation string, results []observation.Result) (Status, error) {
	var status Status
	err := store.locked(ctx, func() error {
		record, err := store.read()
		if err != nil {
			return err
		}
		if record.OperationID != operationID || record.Generation != generation {
			return fmt.Errorf("deploy operation identity does not match")
		}
		status = record.Status
		if status == Committed || status == Recovered || status == Disarmed {
			return nil
		}
		if record.Status != Armed && record.Status != Recovering {
			return fmt.Errorf("terminal deploy operation rejects observations")
		}
		record.Results = results
		record.ObservedAt = store.Clock.Now().UTC()
		record.UpdatedAt = store.Clock.Now().UTC()
		return store.write(record)
	})
	return status, err
}

func (store Store) change(ctx context.Context, operationID, generation string, update func(*Record) error) error {
	return store.locked(ctx, func() error {
		record, err := store.read()
		if err != nil {
			return err
		}
		if record.OperationID != operationID || record.Generation != generation {
			return fmt.Errorf("deploy operation identity does not match")
		}
		if err := update(&record); err != nil {
			return err
		}
		record.UpdatedAt = store.Clock.Now().UTC()
		return store.write(record)
	})
}

func (store Store) locked(ctx context.Context, operation func() error) (resultErr error) {
	if !filepath.IsAbs(store.Path) {
		return fmt.Errorf("deploy operation path must be absolute")
	}
	coordinator, err := rollback.Acquire(ctx, store.Path, store.PollInterval)
	if err != nil {
		slog.WarnContext(ctx, "deployment record coordination failed")
		return fmt.Errorf("coordinate deployment record: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, coordinator.Close()) }()
	return operation()
}

func (store Store) read() (Record, error) {
	file, err := os.Open(store.Path)
	if err != nil {
		return Record{}, fmt.Errorf("open deploy operation: %w", err)
	}
	defer func() { _ = file.Close() }()
	var record Record
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		slog.Warn("deployment operation decoding failed")
		return Record{}, fmt.Errorf("decode deploy operation: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Record{}, fmt.Errorf("deploy operation contains trailing JSON")
	}
	if err := record.validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (store Store) write(record Record) error {
	if err := record.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode deploy operation: %w", err)
	}
	if err := renameio.WriteFile(store.Path, data, 0o600); err != nil {
		slog.Warn("deploy operation persistence failed")
		return fmt.Errorf("write deploy operation: %w", err)
	}
	return nil
}

func (store Store) archive(record Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode terminal deploy operation: %w", err)
	}
	path := store.Path + "." + record.OperationID + "." + record.Generation + ".json"
	if err := renameio.WriteFile(path, data, 0o600); err != nil {
		slog.Warn("terminal deploy operation persistence failed")
		return fmt.Errorf("archive terminal deploy operation: %w", err)
	}
	return nil
}

func (record Record) validate() error {
	if err := record.Manifest.validate(); err != nil {
		return err
	}
	if !validName(record.OperationID) || !validName(record.Generation) ||
		!validName(record.Snapshot) || record.Deadline.IsZero() || record.UpdatedAt.IsZero() {
		return fmt.Errorf("deploy operation identity, snapshot and timestamps are required")
	}
	vmid, err := strconv.Atoi(record.VMID)
	if err != nil || vmid <= 0 {
		return fmt.Errorf("deploy operation VMID must be positive")
	}
	if err := record.Baseline.validate(); err != nil {
		return err
	}
	if record.Status == Recovered {
		if record.RestoredIdentity == nil {
			return fmt.Errorf("recovered deploy operation requires actual restored identity")
		}
		if err := record.RestoredIdentity.verifyRestored(record.Baseline); err != nil {
			return err
		}
	}
	switch record.Status {
	case Armed, Recovering, Recovered, RecoveryFailed, Committed, Disarmed:
	default:
		return fmt.Errorf("deploy operation status is invalid")
	}
	if record.Lease != nil && (record.Lease.ID == "" || record.Lease.Phase == "" ||
		record.Lease.ExpiresAt.IsZero() || record.Lease.ExpiresAt.After(record.Deadline)) {
		return fmt.Errorf("deploy operation lease is invalid")
	}
	return nil
}

func (identity Identity) validate() error {
	for _, digest := range []string{identity.ExecutableSHA256, identity.NetworkSHA256, identity.RuntimeSHA256} {
		decoded, decodeErr := hex.DecodeString(digest)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("deploy identity requires SHA256 digests")
		}
	}
	machine, err := hex.DecodeString(identity.MachineID)
	if err != nil || len(machine) != len(uuid.UUID{}) {
		return fmt.Errorf("deploy machine identity is invalid")
	}
	boot, err := uuid.Parse(identity.BootID)
	if err != nil {
		slog.Warn("deployment boot identity validation failed")
		return fmt.Errorf("deploy identity boot UUID is invalid: %w", err)
	}
	if boot.String() != identity.BootID {
		return fmt.Errorf("deploy boot identity requires the canonical UUID format")
	}
	return nil
}

func (identity Identity) verifyRestored(baseline Identity) error {
	if err := identity.validate(); err != nil {
		return err
	}
	if identity.MachineID != baseline.MachineID || identity.ExecutableSHA256 != baseline.ExecutableSHA256 ||
		identity.NetworkSHA256 != baseline.NetworkSHA256 || identity.RuntimeSHA256 != baseline.RuntimeSHA256 {
		return fmt.Errorf("restored deploy identity does not match the baseline recovery pair")
	}
	if identity.BootID == baseline.BootID {
		return fmt.Errorf("restored deploy VM has not booted after snapshot recovery")
	}
	return nil
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
