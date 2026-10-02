package deployoperation

import (
	"context"
	"crypto/rand"
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
	"goodkind.io/mwan/internal/rollback"
)

type Status string

const (
	Armed          Status = "armed"
	Recovering     Status = "recovering"
	Recovered      Status = "recovered"
	RecoveryFailed Status = "recovery_failed"
	Committed      Status = "committed"
)

type Identity struct {
	MachineID        string `json:"machine_id"`
	BootID           string `json:"boot_id"`
	ExecutableSHA256 string `json:"executable_sha256"`
	NetworkSHA256    string `json:"network_sha256"`
	RuntimeSHA256    string `json:"runtime_sha256"`
}

type Lease struct {
	ID        string    `json:"id"`
	Phase     string    `json:"phase"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Record struct {
	OperationID string        `json:"operation_id"`
	Generation  string        `json:"generation"`
	VMID        string        `json:"vmid"`
	Snapshot    string        `json:"snapshot"`
	Baseline    Identity      `json:"baseline"`
	Status      Status        `json:"status"`
	Deadline    time.Time     `json:"deadline"`
	Lease       *Lease        `json:"lease,omitempty"`
	Watch       WatchIdentity `json:"watch"`
	Reason      string        `json:"reason,omitempty"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type Store struct {
	Path         string
	PollInterval time.Duration
}

func (store Store) Read(ctx context.Context) (Record, error) {
	var record Record
	err := store.locked(ctx, func() error {
		loaded, loadErr := store.read()
		record = loaded
		return loadErr
	})
	return record, err
}

func (store Store) Create(ctx context.Context, record Record) error {
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
		record.UpdatedAt = time.Now().UTC()
		if !record.Deadline.After(record.UpdatedAt) {
			return fmt.Errorf("new deploy operation deadline has expired")
		}
		return store.write(record)
	})
}

func (store Store) Grant(ctx context.Context, operationID, generation, phase string, deadline time.Time) (Lease, error) {
	var lease Lease
	err := store.change(ctx, operationID, generation, func(record *Record) error {
		now := time.Now()
		if record.Status != Armed || !record.Deadline.After(now) {
			return fmt.Errorf("deploy operation does not permit mutations")
		}
		if err := record.Watch.Verify(ctx); err != nil {
			return fmt.Errorf("mutation lease requires the active deploy watch: %w", err)
		}
		if record.Lease != nil {
			return fmt.Errorf("deploy operation already has an outstanding lease")
		}
		if strings.TrimSpace(phase) == "" || !deadline.After(now) || deadline.After(record.Deadline) {
			return fmt.Errorf("mutation lease requires a phase and deadline within the operation deadline")
		}
		lease = Lease{ID: rand.Text(), Phase: phase, ExpiresAt: deadline}
		record.Lease = &lease
		return nil
	})
	return lease, err
}

func (store Store) RegisterWatch(ctx context.Context, operationID, generation, unit string) error {
	identity, err := ReadWatch(ctx, unit)
	if err != nil {
		return err
	}
	if identity.PID != uint32(os.Getpid()) {
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

func (store Store) BeginRecovery(ctx context.Context, operationID, generation, reason string) error {
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status == Recovering {
			return nil
		}
		if record.Status != Armed {
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

func (store Store) FinishRecovery(ctx context.Context, operationID, generation string, success bool, reason string) error {
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status != Recovering {
			return fmt.Errorf("deploy operation is not recovering")
		}
		record.Status = RecoveryFailed
		if success {
			record.Status = Recovered
		}
		record.Reason = reason
		record.Lease = nil
		return nil
	})
}

func (store Store) Commit(ctx context.Context, operationID, generation string) error {
	return store.change(ctx, operationID, generation, func(record *Record) error {
		if record.Status != Armed || record.Lease != nil || !record.Deadline.After(time.Now()) {
			return fmt.Errorf("deploy operation cannot commit with an outstanding lease or expired deadline")
		}
		if err := record.Watch.Verify(ctx); err != nil {
			return fmt.Errorf("deploy commit requires the active deploy watch: %w", err)
		}
		record.Status = Committed
		return nil
	})
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
		record.UpdatedAt = time.Now().UTC()
		return store.write(record)
	})
}

func (store Store) locked(ctx context.Context, operation func() error) (resultErr error) {
	if !filepath.IsAbs(store.Path) {
		return fmt.Errorf("deploy operation path must be absolute")
	}
	coordinator, err := rollback.Acquire(ctx, store.Path, store.PollInterval)
	if err != nil {
		return err
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
	if !validName(record.OperationID) || !validName(record.Generation) ||
		!validName(record.Snapshot) || record.Deadline.IsZero() || record.UpdatedAt.IsZero() {
		return fmt.Errorf("deploy operation identity, snapshot and timestamps are required")
	}
	vmid, err := strconv.Atoi(record.VMID)
	if err != nil || vmid <= 0 {
		return fmt.Errorf("deploy operation VMID must be positive")
	}
	for _, digest := range []string{record.Baseline.ExecutableSHA256, record.Baseline.NetworkSHA256, record.Baseline.RuntimeSHA256} {
		decoded, decodeErr := hex.DecodeString(digest)
		if decodeErr != nil || len(decoded) != 32 {
			return fmt.Errorf("deploy operation baseline requires SHA256 digests")
		}
	}
	if record.Baseline.MachineID == "" || record.Baseline.BootID == "" {
		return fmt.Errorf("deploy operation baseline machine and boot identities are required")
	}
	switch record.Status {
	case Armed, Recovering, Recovered, RecoveryFailed, Committed:
	default:
		return fmt.Errorf("deploy operation status is invalid")
	}
	if record.Lease != nil && (record.Lease.ID == "" || record.Lease.Phase == "" ||
		record.Lease.ExpiresAt.IsZero() || record.Lease.ExpiresAt.After(record.Deadline)) {
		return fmt.Errorf("deploy operation lease is invalid")
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
