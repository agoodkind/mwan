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
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LeaseRecoveryStore saves assignments for validation after a restart.
type LeaseRecoveryStore struct {
	directory string
}

const maxRecoveryDirectoryEntries = 4096

type dhcpv4RecoveryPayload struct {
	IP         net.IP                `json:"ip"`
	PrefixLen  int                   `json:"prefix_len"`
	Gateway    net.IP                `json:"gateway"`
	Routes     []dhcpv4RecoveryRoute `json:"routes"`
	Server     net.IP                `json:"server"`
	LeaseTime  time.Duration         `json:"lease_time"`
	AcquiredAt time.Time             `json:"acquired_at"`
	RenewAt    time.Time             `json:"renew_at"`
	RebindAt   time.Time             `json:"rebind_at"`
	ExpiresAt  time.Time             `json:"expires_at"`
}

type dhcpv4RecoveryRoute struct {
	Destination *net.IPNet `json:"destination"`
	Gateway     net.IP     `json:"gateway"`
}

type dhcpv6RecoveryPrefix struct {
	Prefix         netip.Prefix `json:"prefix"`
	PreferredUntil time.Time    `json:"preferred_until"`
	ValidUntil     time.Time    `json:"valid_until"`
}

type dhcpv6RecoveryAddress struct {
	Address        netip.Addr `json:"address"`
	PreferredUntil time.Time  `json:"preferred_until"`
	ValidUntil     time.Time  `json:"valid_until"`
}

type dhcpv6RecoveryPayload struct {
	LinkName         string                  `json:"link_name"`
	LinkIndex        int                     `json:"link_index"`
	LinkHardwareAddr net.HardwareAddr        `json:"link_hardware_addr"`
	DUID             []byte                  `json:"duid"`
	IAID             uint32                  `json:"iaid"`
	IANAIAID         uint32                  `json:"iana_iaid"`
	ServerID         []byte                  `json:"server_id"`
	AcquiredAt       time.Time               `json:"acquired_at"`
	RenewAt          time.Time               `json:"renew_at"`
	RebindAt         time.Time               `json:"rebind_at"`
	IANARenewAt      time.Time               `json:"iana_renew_at"`
	IANARebindAt     time.Time               `json:"iana_rebind_at"`
	Prefixes         []dhcpv6RecoveryPrefix  `json:"prefixes"`
	Addresses        []dhcpv6RecoveryAddress `json:"addresses"`
	RequestAddress   bool                    `json:"request_address"`
	RequestPrefix    bool                    `json:"request_prefix"`
	Hint             netip.Prefix            `json:"hint"`
	WaitForRA        bool                    `json:"wait_for_ra"`
}

// NewLeaseRecoveryStore selects a private directory for saved assignments.
func NewLeaseRecoveryStore(directory string) (*LeaseRecoveryStore, error) {
	if directory == "" || !filepath.IsAbs(directory) || directory != filepath.Clean(directory) || directory == string(filepath.Separator) {
		return nil, errors.New("absolute clean lease recovery directory is required")
	}
	return &LeaseRecoveryStore{directory: directory}, nil
}

func (store *LeaseRecoveryStore) leaseStore(connectionID string, protocol LeaseProtocol) (*LeaseStore, error) {
	if store == nil || store.directory == "" {
		return nil, errors.New("lease recovery store is required")
	}
	if connectionID == "" {
		return nil, errors.New("connection ID is required")
	}
	if protocol != LeaseProtocolDHCPv4 && protocol != LeaseProtocolDHCPv6 {
		return nil, errors.New("unsupported lease protocol")
	}
	sum := sha256.Sum256([]byte(connectionID + "\x00" + string(protocol)))
	return NewLeaseStore(filepath.Join(store.directory, hex.EncodeToString(sum[:])+".json")), nil
}

func leaseClockNow(loading bool) (LeaseClockSnapshot, error) {
	var wallTime time.Time
	if loading {
		wallTime = (realClock{}).Now()
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		slog.Warn("read boot ID for lease recovery failed", "err", err)
		return LeaseClockSnapshot{}, fmt.Errorf("read boot ID: %w", err)
	}
	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		slog.Warn("read uptime for lease recovery failed", "err", err)
		return LeaseClockSnapshot{}, fmt.Errorf("read uptime: %w", err)
	}
	fields := strings.Fields(string(uptime))
	if len(fields) < 1 || strings.TrimSpace(string(bootID)) == "" {
		return LeaseClockSnapshot{}, errors.New("invalid boot clock snapshot")
	}
	parts := strings.SplitN(fields[0], ".", 2)
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
		return LeaseClockSnapshot{}, errors.New("invalid system uptime")
	}
	var fraction int64
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 9 {
			return LeaseClockSnapshot{}, errors.New("invalid uptime precision")
		}
		fraction, err = strconv.ParseInt(parts[1]+strings.Repeat("0", 9-len(parts[1])), 10, 64)
		if err != nil {
			return LeaseClockSnapshot{}, errors.New("invalid uptime fraction")
		}
	}
	if !loading {
		wallTime = (realClock{}).Now()
	}
	return LeaseClockSnapshot{WallTime: wallTime, BootID: strings.TrimSpace(string(bootID)), Uptime: time.Duration(seconds)*time.Second + time.Duration(fraction)}, nil
}

func leaseElapsed(saved, current LeaseClockSnapshot) (time.Duration, error) {
	if saved.WallTime.IsZero() || saved.BootID == "" || saved.Uptime < 0 || current.Uptime < 0 {
		return 0, errors.New("invalid lease clock snapshot")
	}
	if saved.BootID == current.BootID {
		if current.Uptime < saved.Uptime {
			return 0, errors.New("lease uptime moved backward")
		}
		return current.Uptime - saved.Uptime, nil
	}
	if current.WallTime.Before(saved.WallTime) {
		return 0, errors.New("lease wall clock moved backward")
	}
	return current.WallTime.Sub(saved.WallTime), nil
}

func rebaseLeaseDeadline(deadline time.Time, saved, current LeaseClockSnapshot, elapsed time.Duration) time.Time {
	if deadline.IsZero() {
		return time.Time{}
	}
	return current.WallTime.Add(deadline.Sub(saved.WallTime) - elapsed)
}

func dhcpv4RecoveryIdentity(clientID []byte, hardware net.HardwareAddr) []byte {
	if len(clientID) != 0 {
		return append([]byte{1}, clientID...)
	}
	return append([]byte{2}, hardware...)
}

func dhcpv6RecoveryValue(lease DHCPv6PDLease) dhcpv6RecoveryPayload {
	prefixes := make([]dhcpv6RecoveryPrefix, len(lease.Prefixes))
	for i, prefix := range lease.Prefixes {
		prefixes[i] = dhcpv6RecoveryPrefix(prefix)
	}
	addresses := make([]dhcpv6RecoveryAddress, len(lease.Addresses))
	for i, address := range lease.Addresses {
		addresses[i] = dhcpv6RecoveryAddress(address)
	}
	return dhcpv6RecoveryPayload{
		LinkName: lease.LinkName, LinkIndex: lease.LinkIndex, LinkHardwareAddr: lease.LinkHardwareAddr,
		DUID: lease.DUID, IAID: lease.IAID, IANAIAID: lease.IANAIAID, ServerID: lease.ServerID,
		AcquiredAt: lease.AcquiredAt, RenewAt: lease.RenewAt, RebindAt: lease.RebindAt,
		IANARenewAt: lease.IANARenewAt, IANARebindAt: lease.IANARebindAt,
		Prefixes: prefixes, Addresses: addresses, RequestAddress: lease.RequestAddress,
		RequestPrefix: lease.RequestPrefix, Hint: lease.Hint, WaitForRA: lease.WaitForRA,
	}
}

func (payload dhcpv6RecoveryPayload) lease() DHCPv6PDLease {
	prefixes := make([]DelegatedPrefix, len(payload.Prefixes))
	for i, prefix := range payload.Prefixes {
		prefixes[i] = DelegatedPrefix(prefix)
	}
	addresses := make([]DelegatedAddress, len(payload.Addresses))
	for i, address := range payload.Addresses {
		addresses[i] = DelegatedAddress(address)
	}
	return DHCPv6PDLease{
		LinkName: payload.LinkName, LinkIndex: payload.LinkIndex, LinkHardwareAddr: payload.LinkHardwareAddr,
		DUID: payload.DUID, IAID: payload.IAID, IANAIAID: payload.IANAIAID, ServerID: payload.ServerID,
		AcquiredAt: payload.AcquiredAt, RenewAt: payload.RenewAt, RebindAt: payload.RebindAt,
		IANARenewAt: payload.IANARenewAt, IANARebindAt: payload.IANARebindAt,
		Prefixes: prefixes, Addresses: addresses, RequestAddress: payload.RequestAddress,
		RequestPrefix: payload.RequestPrefix, Hint: payload.Hint, WaitForRA: payload.WaitForRA,
	}
}

// SaveDHCPv4 saves a currently valid DHCPv4 assignment.
func (store *LeaseRecoveryStore) SaveDHCPv4(connectionID, iface string, clientID []byte, lease LeaseInfo) error {
	path, err := store.leaseStore(connectionID, LeaseProtocolDHCPv4)
	if err != nil {
		return err
	}
	link, err := net.InterfaceByName(iface)
	if err != nil {
		slog.Warn("find DHCPv4 recovery interface failed", "iface", iface, "err", err)
		return fmt.Errorf("find DHCPv4 interface: %w", err)
	}
	clock, err := leaseClockNow(false)
	if err != nil {
		return err
	}
	if lease.State != LeaseBound && lease.State != LeaseRenewing && lease.State != LeaseRebinding {
		return errors.New("DHCPv4 lease is not assigned")
	}
	if lease.IP.To4() == nil || lease.PrefixLen < 0 || lease.PrefixLen > 32 || lease.LeaseTime <= 0 || lease.AcquiredAt.IsZero() || !lease.ExpiresAt.After(clock.WallTime) || lease.ExpiresAt.Before(lease.AcquiredAt) || lease.Err != nil || !bytes.Equal(lease.LinkHardwareAddr, link.HardwareAddr) {
		return errors.New("invalid DHCPv4 assignment")
	}
	routes := make([]dhcpv4RecoveryRoute, len(lease.Routes))
	for i, route := range lease.Routes {
		routes[i] = dhcpv4RecoveryRoute(route)
	}
	payload, err := json.Marshal(dhcpv4RecoveryPayload{
		IP: lease.IP, PrefixLen: lease.PrefixLen, Gateway: lease.Gateway, Routes: routes,
		Server: lease.Server, LeaseTime: lease.LeaseTime, AcquiredAt: lease.AcquiredAt,
		RenewAt: lease.RenewAt, RebindAt: lease.RebindAt, ExpiresAt: lease.ExpiresAt,
	})
	if err != nil {
		slog.Warn("encode DHCPv4 recovery assignment failed", "err", err)
		return fmt.Errorf("encode DHCPv4 assignment: %w", err)
	}
	return path.Save(LeaseRecord{
		ConnectionID: connectionID, Protocol: LeaseProtocolDHCPv4,
		InterfaceName: iface, LinkHardwareAddr: bytes.Clone(link.HardwareAddr),
		ProtocolIdentity: dhcpv4RecoveryIdentity(clientID, link.HardwareAddr), Clock: clock, Payload: payload,
	})
}

// LoadDHCPv4 returns a saved assignment for DHCPv4 restart validation.
func (store *LeaseRecoveryStore) LoadDHCPv4(connectionID string, link *net.Interface, clientID []byte) (*LeaseInfo, error) {
	path, err := store.leaseStore(connectionID, LeaseProtocolDHCPv4)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, errors.New("DHCPv4 interface is required")
	}
	record, err := path.Load(connectionID, LeaseProtocolDHCPv4)
	if err != nil {
		return nil, err
	}
	if record.InterfaceName != link.Name || !bytes.Equal(record.LinkHardwareAddr, link.HardwareAddr) || !bytes.Equal(record.ProtocolIdentity, dhcpv4RecoveryIdentity(clientID, link.HardwareAddr)) {
		return nil, errors.New("DHCPv4 lease identity mismatch")
	}
	var payload dhcpv4RecoveryPayload
	if err := decodeStrictLeaseJSON(record.Payload, func(decoder *json.Decoder) error { return decoder.Decode(&payload) }); err != nil {
		slog.Warn("decode DHCPv4 recovery assignment failed", "err", err)
		return nil, fmt.Errorf("decode DHCPv4 assignment: %w", err)
	}
	current, err := leaseClockNow(true)
	if err != nil {
		return nil, err
	}
	elapsed, err := leaseElapsed(record.Clock, current)
	if err != nil {
		return nil, err
	}
	routes := make([]LeaseRoute, len(payload.Routes))
	for i, route := range payload.Routes {
		routes[i] = LeaseRoute(route)
	}
	lease := &LeaseInfo{
		State: LeaseBound, LinkIndex: link.Index, LinkHardwareAddr: bytes.Clone(link.HardwareAddr), InvalidationEpoch: 0, Err: nil,
		IP: payload.IP, PrefixLen: payload.PrefixLen, Gateway: payload.Gateway, Routes: routes,
		Server: payload.Server, LeaseTime: payload.LeaseTime,
		AcquiredAt: rebaseLeaseDeadline(payload.AcquiredAt, record.Clock, current, elapsed),
		RenewAt:    rebaseLeaseDeadline(payload.RenewAt, record.Clock, current, elapsed),
		RebindAt:   rebaseLeaseDeadline(payload.RebindAt, record.Clock, current, elapsed),
		ExpiresAt:  rebaseLeaseDeadline(payload.ExpiresAt, record.Clock, current, elapsed),
	}
	if lease.IP.To4() == nil || lease.PrefixLen < 0 || lease.PrefixLen > 32 || lease.LeaseTime <= 0 || lease.AcquiredAt.IsZero() || lease.AcquiredAt.After(current.WallTime) || !lease.ExpiresAt.After(current.WallTime) || lease.ExpiresAt.Before(lease.AcquiredAt) {
		return nil, errors.New("saved DHCPv4 assignment is invalid or expired")
	}
	return lease, nil
}

// SaveDHCPv6 saves a currently valid DHCPv6 assignment.
func (store *LeaseRecoveryStore) SaveDHCPv6(connectionID string, lease DHCPv6PDLease) error {
	path, err := store.leaseStore(connectionID, LeaseProtocolDHCPv6)
	if err != nil {
		return err
	}
	link, err := net.InterfaceByName(lease.LinkName)
	if err != nil {
		slog.Warn("find DHCPv6 recovery interface failed", "iface", lease.LinkName, "err", err)
		return fmt.Errorf("find DHCPv6 interface: %w", err)
	}
	clock, err := leaseClockNow(false)
	if err != nil {
		return err
	}
	cfg := DHCPv6PDConfig{
		Iface: lease.LinkName, DUID: lease.DUID, IAID: lease.IAID, IANAIAID: lease.IANAIAID,
		RequestAddress: lease.RequestAddress, RequestPrefix: lease.RequestPrefix,
		Hint: lease.Hint, Clock: realClock{}, WaitForRA: lease.WaitForRA, CachedLease: nil,
	}
	if len(lease.DUID) == 0 || !compatibleDHCPv6Lease(cfg, link, lease) || !latestDHCPv6Validity(lease).After(clock.WallTime) {
		return errors.New("invalid DHCPv6 assignment")
	}
	lease.LinkIndex = 0
	payload, err := json.Marshal(dhcpv6RecoveryValue(lease))
	if err != nil {
		slog.Warn("encode DHCPv6 recovery assignment failed", "err", err)
		return fmt.Errorf("encode DHCPv6 assignment: %w", err)
	}
	return path.Save(LeaseRecord{
		ConnectionID: connectionID, Protocol: LeaseProtocolDHCPv6,
		InterfaceName: link.Name, LinkHardwareAddr: bytes.Clone(link.HardwareAddr),
		ProtocolIdentity: bytes.Clone(lease.DUID), Clock: clock, Payload: payload,
	})
}

// LoadDHCPv6 returns a saved assignment for DHCPv6 restart validation.
func (store *LeaseRecoveryStore) LoadDHCPv6(connectionID string, link *net.Interface, cfg DHCPv6PDConfig) (*DHCPv6PDLease, error) {
	path, err := store.leaseStore(connectionID, LeaseProtocolDHCPv6)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, errors.New("DHCPv6 interface is required")
	}
	record, err := path.Load(connectionID, LeaseProtocolDHCPv6)
	if err != nil {
		return nil, err
	}
	if record.InterfaceName != link.Name || !bytes.Equal(record.LinkHardwareAddr, link.HardwareAddr) || !bytes.Equal(record.ProtocolIdentity, cfg.DUID) || cfg.Iface != link.Name {
		return nil, errors.New("DHCPv6 lease identity mismatch")
	}
	var payload dhcpv6RecoveryPayload
	if err := decodeStrictLeaseJSON(record.Payload, func(decoder *json.Decoder) error { return decoder.Decode(&payload) }); err != nil {
		slog.Warn("decode DHCPv6 recovery assignment failed", "err", err)
		return nil, fmt.Errorf("decode DHCPv6 assignment: %w", err)
	}
	lease := payload.lease()
	if lease.LinkIndex != 0 || !bytes.Equal(lease.DUID, record.ProtocolIdentity) || lease.IAID != cfg.IAID || lease.IANAIAID != cfg.IANAIAID || lease.RequestAddress != cfg.RequestAddress || lease.RequestPrefix != cfg.RequestPrefix || lease.Hint != cfg.Hint || lease.WaitForRA != cfg.WaitForRA {
		return nil, errors.New("DHCPv6 lease configuration mismatch")
	}
	current, err := leaseClockNow(true)
	if err != nil {
		return nil, err
	}
	elapsed, err := leaseElapsed(record.Clock, current)
	if err != nil {
		return nil, err
	}
	lease.AcquiredAt = rebaseLeaseDeadline(lease.AcquiredAt, record.Clock, current, elapsed)
	lease.RenewAt = rebaseLeaseDeadline(lease.RenewAt, record.Clock, current, elapsed)
	lease.RebindAt = rebaseLeaseDeadline(lease.RebindAt, record.Clock, current, elapsed)
	lease.IANARenewAt = rebaseLeaseDeadline(lease.IANARenewAt, record.Clock, current, elapsed)
	lease.IANARebindAt = rebaseLeaseDeadline(lease.IANARebindAt, record.Clock, current, elapsed)
	for i := range lease.Prefixes {
		lease.Prefixes[i].PreferredUntil = rebaseLeaseDeadline(lease.Prefixes[i].PreferredUntil, record.Clock, current, elapsed)
		lease.Prefixes[i].ValidUntil = rebaseLeaseDeadline(lease.Prefixes[i].ValidUntil, record.Clock, current, elapsed)
	}
	for i := range lease.Addresses {
		lease.Addresses[i].PreferredUntil = rebaseLeaseDeadline(lease.Addresses[i].PreferredUntil, record.Clock, current, elapsed)
		lease.Addresses[i].ValidUntil = rebaseLeaseDeadline(lease.Addresses[i].ValidUntil, record.Clock, current, elapsed)
	}
	lease.LinkIndex = link.Index
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if !latestDHCPv6Validity(lease).After(current.WallTime) || !compatibleDHCPv6Lease(cfg, link, lease) {
		return nil, errors.New("saved DHCPv6 assignment is invalid or expired")
	}
	return &lease, nil
}

// Delete removes one saved protocol assignment.
func (store *LeaseRecoveryStore) Delete(connectionID string, protocol LeaseProtocol) error {
	path, err := store.leaseStore(connectionID, protocol)
	if err != nil {
		return err
	}
	return path.Delete()
}

// Has reports whether a validated record exists without requiring a link.
func (store *LeaseRecoveryStore) Has(connectionID string, protocol LeaseProtocol) (bool, error) {
	path, err := store.leaseStore(connectionID, protocol)
	if err != nil {
		return false, err
	}
	if _, err := path.Load(connectionID, protocol); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// PruneUnconfigured removes valid records absent from the active connection set.
// The store directory must contain only MWAN-owned connection records.
func (store *LeaseRecoveryStore) PruneUnconfigured(active map[string]bool) error {
	if store == nil || store.directory == "" {
		return errors.New("lease recovery store is required")
	}
	info, err := os.Lstat(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		slog.Warn("inspect lease recovery directory failed", "err", err)
		return fmt.Errorf("inspect lease recovery directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("lease recovery directory must be a private directory")
	}
	directory, err := os.Open(store.directory)
	if err != nil {
		slog.Warn("open lease recovery directory failed", "err", err)
		return fmt.Errorf("open lease recovery directory: %w", err)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maxRecoveryDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		slog.Warn("list lease recovery directory failed", "err", err)
		return fmt.Errorf("list lease recovery directory: %w", err)
	}
	if len(entries) > maxRecoveryDirectoryEntries {
		return fmt.Errorf("lease recovery directory exceeds %d entries", maxRecoveryDirectoryEntries)
	}
	var failures []error
	for _, entry := range entries {
		if err := store.pruneRecord(entry.Name(), active); err != nil {
			slog.Warn("lease recovery record retained", "file", entry.Name(), "err", err)
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name(), err))
		}
	}
	return errors.Join(failures...)
}

func (store *LeaseRecoveryStore) pruneRecord(name string, active map[string]bool) error {
	base := strings.TrimSuffix(name, ".json")
	if !strings.HasSuffix(name, ".json") || len(base) != sha256.Size*2 {
		return errors.New("unknown lease recovery filename")
	}
	encodedHash, err := hex.DecodeString(base)
	if err != nil || hex.EncodeToString(encodedHash) != base {
		return errors.New("invalid lease recovery filename")
	}
	path := filepath.Join(store.directory, name)
	info, err := os.Lstat(path)
	if err != nil {
		slog.Warn("inspect lease recovery record failed", "path", path, "err", err)
		return fmt.Errorf("inspect lease recovery record: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("lease recovery record is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		slog.Warn("open lease recovery record failed", "path", path, "err", err)
		return fmt.Errorf("open lease recovery record: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxLeaseRecordSize+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		slog.Warn("read lease recovery record failed", "path", path, "read_err", readErr, "close_err", closeErr)
		return errors.Join(readErr, closeErr)
	}
	if len(data) > maxLeaseRecordSize {
		return errors.New("lease recovery record exceeds size limit")
	}
	var envelope leaseRecordEnvelope
	if err := decodeStrictLeaseJSON(data, func(decoder *json.Decoder) error { return decoder.Decode(&envelope) }); err != nil {
		return err
	}
	if envelope.Version != leaseRecordVersion {
		return errors.New("unsupported lease recovery record version")
	}
	leaseStore, err := store.leaseStore(envelope.ConnectionID, envelope.Protocol)
	if err != nil {
		return err
	}
	if filepath.Base(leaseStore.path) != name {
		return errors.New("lease recovery filename does not match identity")
	}
	if _, err := leaseStore.Load(envelope.ConnectionID, envelope.Protocol); err != nil {
		return err
	}
	if active[envelope.ConnectionID] {
		return nil
	}
	return leaseStore.Delete()
}
