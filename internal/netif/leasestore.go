package netif

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	leaseRecordVersion = 1
	maxLeaseRecordSize = 1048576
)

// LeaseProtocol identifies the protocol that produced a saved assignment.
type LeaseProtocol string

const (
	// LeaseProtocolDHCPv4 identifies a DHCPv4 assignment.
	LeaseProtocolDHCPv4 LeaseProtocol = "dhcpv4"
	// LeaseProtocolDHCPv6 identifies a DHCPv6 assignment.
	LeaseProtocolDHCPv6 LeaseProtocol = "dhcpv6"
)

// LeaseClockSnapshot records the wall and boot clocks at the time of saving.
type LeaseClockSnapshot struct {
	WallTime time.Time     `json:"wall_time"`
	BootID   string        `json:"boot_id"`
	Uptime   time.Duration `json:"uptime"`
}

// LeaseRecord stores identity and protocol data for later restart validation.
type LeaseRecord struct {
	ConnectionID     string             `json:"connection_id"`
	Protocol         LeaseProtocol      `json:"protocol"`
	InterfaceName    string             `json:"interface_name"`
	LinkHardwareAddr net.HardwareAddr   `json:"link_hardware_addr"`
	ProtocolIdentity []byte             `json:"protocol_identity"`
	Clock            LeaseClockSnapshot `json:"clock"`
	Payload          json.RawMessage    `json:"payload"`
}

type leaseRecordEnvelope struct {
	Version      int             `json:"version"`
	ConnectionID string          `json:"connection_id"`
	Protocol     LeaseProtocol   `json:"protocol"`
	Checksum     string          `json:"checksum"`
	Record       json.RawMessage `json:"record"`
}

// LeaseStore persists one record at a caller-supplied path.
type LeaseStore struct {
	mu   sync.Mutex
	path string
}

// NewLeaseStore selects the exact file used for a lease record.
func NewLeaseStore(path string) *LeaseStore {
	return &LeaseStore{mu: sync.Mutex{}, path: path}
}

// Save atomically replaces the record and syncs its file and parent directory.
func (store *LeaseStore) Save(record LeaseRecord) error {
	if store == nil {
		return errors.New("lease store is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.validatePath(); err != nil {
		return err
	}
	if err := validateLeaseRecord(record); err != nil {
		return err
	}
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return leaseStoreError("encode lease record", err)
	}
	sum := sha256.Sum256(encodedRecord)
	envelope := leaseRecordEnvelope{
		Version: leaseRecordVersion, ConnectionID: record.ConnectionID, Protocol: record.Protocol,
		Checksum: hex.EncodeToString(sum[:]), Record: encodedRecord,
	}
	encodedEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return leaseStoreError("encode lease envelope", err)
	}
	if len(encodedEnvelope) > maxLeaseRecordSize {
		return fmt.Errorf("lease record exceeds %d bytes", maxLeaseRecordSize)
	}
	if err := store.prepareDirectory(); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".lease-*")
	if err != nil {
		return leaseStoreError("create temporary lease record", err)
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return leaseStoreError("set lease record permissions", err)
	}
	if _, err := temporary.Write(encodedEnvelope); err != nil {
		temporary.Close()
		return leaseStoreError("write lease record", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return leaseStoreError("sync lease record", err)
	}
	if err := temporary.Close(); err != nil {
		return leaseStoreError("close lease record", err)
	}
	if err := os.Rename(temporary.Name(), store.path); err != nil {
		return leaseStoreError("replace lease record", err)
	}
	if err := store.syncDirectory(); err != nil {
		return err
	}
	slog.Debug("lease record saved", "path", store.path)
	return nil
}

// Load reads a record only when its saved connection and protocol match.
func (store *LeaseStore) Load(connectionID string, protocol LeaseProtocol) (LeaseRecord, error) {
	if store == nil {
		return LeaseRecord{}, errors.New("lease store is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.validatePath(); err != nil {
		return LeaseRecord{}, err
	}
	file, err := os.Open(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LeaseRecord{}, os.ErrNotExist
		}
		return LeaseRecord{}, leaseStoreError("open lease record", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLeaseRecordSize+1))
	if err != nil {
		return LeaseRecord{}, leaseStoreError("read lease record", err)
	}
	if len(data) > maxLeaseRecordSize {
		return LeaseRecord{}, fmt.Errorf("lease record exceeds %d bytes", maxLeaseRecordSize)
	}
	var envelope leaseRecordEnvelope
	if err := decodeLeaseEnvelope(data, &envelope); err != nil {
		return LeaseRecord{}, leaseStoreError("decode lease envelope", err)
	}
	if envelope.Version != leaseRecordVersion {
		return LeaseRecord{}, fmt.Errorf("unsupported lease record version %d", envelope.Version)
	}
	if envelope.ConnectionID != connectionID || envelope.Protocol != protocol {
		return LeaseRecord{}, errors.New("lease record identity mismatch")
	}
	sum := sha256.Sum256(envelope.Record)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return LeaseRecord{}, errors.New("lease record checksum mismatch")
	}
	var record LeaseRecord
	if err := decodeLeaseRecord(envelope.Record, &record); err != nil {
		return LeaseRecord{}, leaseStoreError("decode lease record", err)
	}
	if err := validateLeaseRecord(record); err != nil {
		return LeaseRecord{}, err
	}
	if record.ConnectionID != envelope.ConnectionID || record.Protocol != envelope.Protocol {
		return LeaseRecord{}, errors.New("lease record envelope identity mismatch")
	}
	slog.Debug("lease record loaded", "path", store.path)
	return record, nil
}

// Delete removes the record and syncs its parent directory.
func (store *LeaseStore) Delete() error {
	if store == nil {
		return errors.New("lease store is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.validatePath(); err != nil {
		return err
	}
	if err := os.Remove(store.path); err != nil {
		return leaseStoreError("remove lease record", err)
	}
	if err := store.syncDirectory(); err != nil {
		return err
	}
	slog.Debug("lease record deleted", "path", store.path)
	return nil
}

func (store *LeaseStore) validatePath() error {
	if store.path == "" || !filepath.IsAbs(store.path) || store.path != filepath.Clean(store.path) || filepath.Base(store.path) == string(filepath.Separator) {
		return errors.New("absolute lease record path is required")
	}
	leafDirectory := filepath.Dir(store.path)
	for directory := leafDirectory; ; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) || directory != leafDirectory {
				return leaseStoreError("inspect lease directory path", err)
			}
		} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("lease directory path component %q is not a directory", directory)
		}
		if directory == filepath.Dir(directory) {
			break
		}
	}
	return nil
}

func (store *LeaseStore) prepareDirectory() error {
	directory := filepath.Dir(store.path)
	info, err := os.Lstat(directory)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return leaseStoreError("create lease directory", err)
		}
		created = true
		if err := os.Chmod(directory, 0o700); err != nil {
			return leaseStoreError("set lease directory permissions", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return leaseStoreError("inspect lease directory", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("lease directory must have mode 0700")
	}
	if created {
		return syncLeaseDirectory(filepath.Dir(directory))
	}
	return nil
}

func (store *LeaseStore) syncDirectory() error {
	return syncLeaseDirectory(filepath.Dir(store.path))
}

func syncLeaseDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return leaseStoreError("open lease directory", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return leaseStoreError("sync lease directory", err)
	}
	return nil
}

func validateLeaseRecord(record LeaseRecord) error {
	if record.ConnectionID == "" || record.InterfaceName == "" || len(record.LinkHardwareAddr) == 0 || len(record.ProtocolIdentity) == 0 {
		return errors.New("lease record identity is incomplete")
	}
	if record.Protocol != LeaseProtocolDHCPv4 && record.Protocol != LeaseProtocolDHCPv6 {
		return errors.New("unsupported lease protocol")
	}
	if record.Clock.WallTime.IsZero() || record.Clock.BootID == "" || record.Clock.Uptime < 0 || !json.Valid(record.Payload) {
		return errors.New("lease record clock or payload is invalid")
	}
	return nil
}

func decodeLeaseEnvelope(data []byte, destination *leaseRecordEnvelope) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return leaseStoreError("decode envelope", err)
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return leaseStoreError("decode trailing data", err)
	}
	return nil
}

func decodeLeaseRecord(data []byte, destination *LeaseRecord) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return leaseStoreError("decode record", err)
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return leaseStoreError("decode trailing data", err)
	}
	return nil
}

func leaseStoreError(operation string, err error) error {
	return &leaseStoreOperationError{operation: operation, cause: err}
}

type leaseStoreOperationError struct {
	operation string
	cause     error
}

func (failure *leaseStoreOperationError) Error() string {
	return failure.operation + ": " + failure.cause.Error()
}

func (failure *leaseStoreOperationError) Unwrap() error {
	return failure.cause
}
