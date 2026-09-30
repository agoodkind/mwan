package netif

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
	"github.com/insomniacslk/dhcp/iana"
	internalclock "goodkind.io/mwan/internal/clock"
)

// DHCPv6PDConfig identifies the DHCPv6 associations on one link.
type DHCPv6PDConfig struct {
	Iface          string
	DUID           []byte
	IAID           uint32
	IANAIAID       uint32
	RequestAddress bool
	RequestPrefix  bool
	Hint           netip.Prefix
	Clock          internalclock.Clock
	WaitForRA      bool
	CachedLease    *DHCPv6PDLease
}

// DelegatedPrefix records the server's association and absolute lifetimes.
type DelegatedPrefix struct {
	Prefix         netip.Prefix
	PreferredUntil time.Time
	ValidUntil     time.Time
}

// DelegatedAddress records an assigned address and its absolute lifetimes.
type DelegatedAddress struct {
	Address        netip.Addr
	PreferredUntil time.Time
	ValidUntil     time.Time
}

// DHCPv6PDLease retains protocol metadata for renewal and future recovery.
type DHCPv6PDLease struct {
	LinkName         string
	LinkIndex        int
	LinkHardwareAddr net.HardwareAddr
	DUID             []byte
	IAID             uint32
	IANAIAID         uint32
	ServerID         []byte
	AcquiredAt       time.Time
	RenewAt          time.Time
	RebindAt         time.Time
	IANARenewAt      time.Time
	IANARebindAt     time.Time
	Prefixes         []DelegatedPrefix
	Addresses        []DelegatedAddress
	RequestAddress   bool
	RequestPrefix    bool
	Hint             netip.Prefix
	WaitForRA        bool
}

// DHCPv6PDClient negotiates the requested associations in one session.
type DHCPv6PDClient struct {
	mu           sync.RWMutex
	lease        DHCPv6PDLease
	declines     chan netip.Addr
	pending      map[netip.Addr]bool
	done         <-chan struct{}
	recoveryDone chan struct{}
	recoveryOnce sync.Once
	Events       chan struct{}
}

// DHCPv6PDStore publishes current MWAN-owned DHCPv6 assignments to consumers.
type DHCPv6PDStore struct {
	mu     sync.RWMutex
	leases map[string]DHCPv6PDLease
}

// NewDHCPv6PDStore creates the shared lease store.
func NewDHCPv6PDStore() *DHCPv6PDStore {
	return &DHCPv6PDStore{mu: sync.RWMutex{}, leases: make(map[string]DHCPv6PDLease)}
}

// Set replaces one interface's negotiated lease.
func (store *DHCPv6PDStore) Set(iface string, lease DHCPv6PDLease) {
	store.mu.Lock()
	store.leases[iface] = cloneDHCPv6Lease(lease)
	store.mu.Unlock()
}

// Delete removes one interface's negotiated lease.
func (store *DHCPv6PDStore) Delete(iface string) {
	store.mu.Lock()
	delete(store.leases, iface)
	store.mu.Unlock()
}

// Get reads one interface's negotiated lease.
func (store *DHCPv6PDStore) Get(iface string) (DHCPv6PDLease, bool) {
	store.mu.RLock()
	lease, ok := store.leases[iface]
	store.mu.RUnlock()
	return cloneDHCPv6Lease(lease), ok
}

// LastLease returns the most recent association snapshot.
func (client *DHCPv6PDClient) LastLease() DHCPv6PDLease {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return cloneDHCPv6Lease(client.lease)
}

// DeclineAddress queues a duplicate IA_NA address for protocol withdrawal.
func (client *DHCPv6PDClient) DeclineAddress(address netip.Addr) bool {
	client.mu.Lock()
	found := false
	for _, assigned := range client.lease.Addresses {
		if assigned.Address == address {
			found = true
			break
		}
	}
	if !found || client.pending[address] {
		client.mu.Unlock()
		return false
	}
	client.pending[address] = true
	client.mu.Unlock()
	select {
	case <-client.done:
		client.mu.Lock()
		delete(client.pending, address)
		client.mu.Unlock()
		return false
	case client.declines <- address:
		return true
	}
}

func cloneDHCPv6Lease(lease DHCPv6PDLease) DHCPv6PDLease {
	lease.DUID = bytes.Clone(lease.DUID)
	lease.ServerID = bytes.Clone(lease.ServerID)
	lease.LinkHardwareAddr = bytes.Clone(lease.LinkHardwareAddr)
	lease.Prefixes = append([]DelegatedPrefix(nil), lease.Prefixes...)
	lease.Addresses = append([]DelegatedAddress(nil), lease.Addresses...)
	return lease
}

func (client *DHCPv6PDClient) publish(lease DHCPv6PDLease) {
	client.mu.Lock()
	client.lease = lease
	for address := range client.pending {
		present := false
		for _, assigned := range lease.Addresses {
			if assigned.Address == address {
				present = true
				break
			}
		}
		if !present {
			delete(client.pending, address)
		}
	}
	client.mu.Unlock()
	// The watcher reads the latest lease, so one pending event covers later replacements.
	select {
	case client.Events <- struct{}{}:
	default:
	}
}

// StartDHCPv6PDClient validates cached assignments or starts fresh acquisition.
func StartDHCPv6PDClient(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig) (*DHCPv6PDClient, error) {
	if config.Iface == "" || len(config.DUID) == 0 {
		return nil, errors.New("DHCPv6 interface and DUID are required")
	}
	if _, err := dhcpv6.DUIDFromBytes(config.DUID); err != nil {
		log.WarnContext(ctx, "dhcpv6: invalid DUID", "err", err)
		return nil, fmt.Errorf("DHCPv6 DUID: %w", err)
	}
	if config.Clock == nil {
		config.Clock = realClock{}
	}
	if !config.RequestAddress && !config.RequestPrefix {
		config.RequestPrefix = true
	}
	if config.CachedLease != nil {
		cached := cloneDHCPv6Lease(*config.CachedLease)
		config.CachedLease = &cached
	}
	var emptyLease DHCPv6PDLease
	client := &DHCPv6PDClient{mu: sync.RWMutex{}, lease: emptyLease, declines: make(chan netip.Addr, 16), pending: make(map[netip.Addr]bool), done: ctx.Done(), recoveryDone: make(chan struct{}), recoveryOnce: sync.Once{}, Events: make(chan struct{}, 1)}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "dhcpv6: client panicked", "err", recovered)
			}
		}()
		client.run(ctx, log, config)
	}()
	return client, nil
}

func (client *DHCPv6PDClient) run(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig) {
	defer close(client.Events)
	defer client.completeRecovery()
	for ctx.Err() == nil {
		link, err := net.InterfaceByName(config.Iface)
		if err != nil {
			log.WarnContext(ctx, "dhcpv6: link unavailable", "iface", config.Iface, "err", err)
			if !waitDHCPv6(ctx) {
				return
			}
			continue
		}
		if config.WaitForRA && !waitDHCPv6RA(ctx, log, config.Iface, link.Index, config.RequestAddress) {
			if !waitDHCPv6(ctx) {
				return
			}
			continue
		}
		recovered, solicitMaxRT, err := client.recoverOnLink(ctx, log, config, link)
		if err != nil {
			log.WarnContext(ctx, "dhcpv6: recovery socket unavailable", "iface", config.Iface, "err", err)
			if !waitDHCPv6(ctx) {
				return
			}
			continue
		}
		client.completeRecovery()
		transport, err := nclient6.New(config.Iface, nclient6.WithRetry(1), nclient6.WithTimeout(time.Second))
		if err != nil {
			log.WarnContext(ctx, "dhcpv6: socket unavailable", "iface", config.Iface, "err", err)
			if !waitDHCPv6(ctx) {
				return
			}
			continue
		}
		client.negotiate(ctx, log, transport, config, link, recovered, solicitMaxRT)
		if err := transport.Close(); err != nil {
			log.WarnContext(ctx, "dhcpv6: close socket failed", "err", err)
		}
	}
}

func waitDHCPv6RA(ctx context.Context, log *slog.Logger, iface string, linkIndex int, requestAddress bool) bool {
	raClient, err := NewRAClient(iface, log)
	if err != nil {
		log.WarnContext(ctx, "dhcpv6: RA listener unavailable", "iface", iface, "err", err)
		return false
	}
	defer raClient.Close()
	for ctx.Err() == nil {
		link, err := net.InterfaceByName(iface)
		if err != nil || link.Index != linkIndex {
			return false
		}
		advertisement, err := raClient.SolicitRA(ctx, time.Second)
		if err == nil && (advertisement.ManagedConfiguration || (!requestAddress && advertisement.OtherConfiguration)) {
			link, err = net.InterfaceByName(iface)
			return err == nil && link.Index == linkIndex && ctx.Err() == nil
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			log.WarnContext(ctx, "dhcpv6: RA read failed", "iface", iface, "err", err)
			return false
		}
		if !waitDHCPv6(ctx) {
			return false
		}
	}
	return false
}

func (client *DHCPv6PDClient) negotiate(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, lease DHCPv6PDLease, solicitMaxRT time.Duration) {
	backoff := time.Second
	var notifiedThrough time.Time
	reacquireAddress := false
	if len(lease.Prefixes) != 0 || len(lease.Addresses) != 0 {
		notifiedThrough = config.Clock.Now()
		client.completeRecoveredDHCPv6(ctx, log, transport, config, link, &lease, &solicitMaxRT)
	}
	for ctx.Err() == nil {
		if reacquireAddress && len(lease.Prefixes) != 0 &&
			client.servicePrefixWhileAcquiringAddress(ctx, log, transport, config, link, &lease, &reacquireAddress, &solicitMaxRT, &notifiedThrough) {
			continue
		}
		if len(lease.Prefixes) == 0 && len(lease.Addresses) == 0 || reacquireAddress {
			if !client.acquireInNegotiation(ctx, log, transport, config, link, &lease, &reacquireAddress, &backoff, &solicitMaxRT, &notifiedThrough) {
				return
			}
			continue
		}
		now := config.Clock.Now()
		validUntil := latestDHCPv6Validity(lease)
		deadline := nextDHCPv6Deadline(lease, notifiedThrough)
		if !deadline.IsZero() && !now.Before(deadline) {
			lease = expireDHCPv6Assignments(lease, now)
			client.publish(lease)
			notifiedThrough = deadline
			continue
		}
		if !now.Before(validUntil) {
			var emptyLease DHCPv6PDLease
			lease = emptyLease
			backoff = time.Second
			client.publish(lease)
			continue
		}
		renewAt, rebindAt := dhcpv6AssociationTimers(lease)
		if now.Before(renewAt) {
			address, ok := client.waitDHCPv6OrDecline(ctx, dhcpv6WaitDuration(min(renewAt.Sub(now), validUntil.Sub(now)), now, deadline))
			if !ok {
				return
			}
			client.processDHCPv6Decline(ctx, log, transport, config, &lease, &reacquireAddress, &solicitMaxRT, address)
			continue
		}
		rebinding := !now.Before(rebindAt)
		replacement, err := renewDHCPv6(ctx, transport, config, link, lease, rebinding, &solicitMaxRT)
		if err == nil {
			lease = replacement
			client.publish(lease)
			notifiedThrough = lease.AcquiredAt
			backoff = time.Second
			continue
		}
		log.WarnContext(ctx, "dhcpv6: renewal failed", "iface", config.Iface, "err", err)
		retryDeadline := rebindAt
		if rebinding {
			retryDeadline = validUntil
		}
		now = config.Clock.Now()
		address, ok := client.waitDHCPv6OrDecline(ctx, dhcpv6WaitDuration(min(jitterDHCPv6(backoff), retryDeadline.Sub(now)), now, deadline))
		if !ok {
			return
		}
		client.processDHCPv6Decline(ctx, log, transport, config, &lease, &reacquireAddress, &solicitMaxRT, address)
		backoff = min(backoff*2, 120*time.Second)
	}
}

func (client *DHCPv6PDClient) completeRecoveredDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, lease *DHCPv6PDLease, solicitMaxRT *time.Duration) {
	for ctx.Err() == nil && (config.RequestPrefix && len(lease.Prefixes) == 0 || config.RequestAddress && len(lease.Addresses) == 0) {
		if !client.acquireMissingDHCPv6(ctx, log, transport, config, link, lease, solicitMaxRT) {
			return
		}
	}
}

func (client *DHCPv6PDClient) acquireMissingDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, lease *DHCPv6PDLease, solicitMaxRT *time.Duration) bool {
	expired := expireDHCPv6Assignments(*lease, config.Clock.Now())
	if len(expired.Prefixes) != len(lease.Prefixes) || len(expired.Addresses) != len(lease.Addresses) {
		*lease = expired
		client.publish(*lease)
	}
	if len(lease.Prefixes) == 0 && len(lease.Addresses) == 0 {
		return false
	}
	missing := config
	missing.RequestPrefix = config.RequestPrefix && len(lease.Prefixes) == 0
	missing.RequestAddress = config.RequestAddress && len(lease.Addresses) == 0
	if !missing.RequestPrefix && !missing.RequestAddress {
		return false
	}
	acquireCtx, cancel := context.WithDeadline(ctx, latestDHCPv6Validity(*lease))
	defer cancel()
	replacement, err := acquireDHCPv6(acquireCtx, transport, missing, link, solicitMaxRT, nil)
	*lease = expireDHCPv6Assignments(*lease, config.Clock.Now())
	if err != nil {
		if len(lease.Prefixes) == 0 && len(lease.Addresses) == 0 {
			client.publish(*lease)
			return false
		}
		log.WarnContext(ctx, "dhcpv6: missing association acquisition failed", "iface", config.Iface, "err", err)
	} else {
		if missing.RequestPrefix {
			lease.Prefixes, lease.RenewAt, lease.RebindAt = replacement.Prefixes, replacement.RenewAt, replacement.RebindAt
		}
		if missing.RequestAddress {
			lease.Addresses, lease.IANARenewAt, lease.IANARebindAt = replacement.Addresses, replacement.IANARenewAt, replacement.IANARebindAt
		}
		lease.ServerID = bytes.Clone(replacement.ServerID)
		client.publish(*lease)
	}
	if !waitDHCPv6(ctx) {
		return false
	}
	return config.RequestPrefix && len(lease.Prefixes) == 0 || config.RequestAddress && len(lease.Addresses) == 0
}

var errDHCPv6RestartRejected = errors.New("DHCPv6 restart assignment rejected")

func compatibleDHCPv6Lease(config DHCPv6PDConfig, link *net.Interface, cached DHCPv6PDLease) bool {
	if !bytes.Equal(cached.DUID, config.DUID) || cached.IAID != config.IAID || cached.IANAIAID != config.IANAIAID ||
		cached.RequestAddress != config.RequestAddress || cached.RequestPrefix != config.RequestPrefix || cached.Hint != config.Hint || cached.WaitForRA != config.WaitForRA ||
		cached.LinkName != link.Name || !bytes.Equal(cached.LinkHardwareAddr, link.HardwareAddr) {
		return false
	}
	if len(cached.ServerID) == 0 || cached.AcquiredAt.IsZero() || cached.AcquiredAt.After(config.Clock.Now()) {
		return false
	}
	if _, err := dhcpv6.DUIDFromBytes(cached.ServerID); err != nil {
		return false
	}
	if !config.RequestPrefix && len(cached.Prefixes) != 0 || !config.RequestAddress && len(cached.Addresses) != 0 {
		return false
	}
	return validDHCPv6CachedDeadlines(cached) && config.Clock.Now().Before(latestDHCPv6Validity(cached))
}

func validDHCPv6CachedDeadlines(cached DHCPv6PDLease) bool {
	for _, prefix := range cached.Prefixes {
		if !prefix.Prefix.IsValid() || prefix.ValidUntil.IsZero() || prefix.PreferredUntil.After(prefix.ValidUntil) {
			return false
		}
	}
	if len(cached.Prefixes) != 0 && (cached.RenewAt.IsZero() || !cached.RenewAt.Before(cached.RebindAt)) {
		return false
	}
	for _, address := range cached.Addresses {
		if !address.Address.IsValid() || address.ValidUntil.IsZero() || address.PreferredUntil.After(address.ValidUntil) {
			return false
		}
	}
	if len(cached.Addresses) != 0 && (cached.IANARenewAt.IsZero() || !cached.IANARenewAt.Before(cached.IANARebindAt)) {
		return false
	}
	return true
}

func validateDHCPv6Restart(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, cached DHCPv6PDLease, solicitMaxRT *time.Duration) (DHCPv6PDLease, error) {
	cached = expireDHCPv6Assignments(cached, config.Clock.Now())
	if len(cached.Prefixes) == 0 && len(cached.Addresses) == 0 {
		return DHCPv6PDLease{}, errDHCPv6RestartRejected
	}
	if len(cached.Prefixes) == 0 {
		confirm := config
		confirm.RequestPrefix = false
		reply, err := exchangeDHCPv6Restart(ctx, transport, confirm, dhcpv6.MessageTypeConfirm, cached, solicitMaxRT)
		if err != nil {
			return DHCPv6PDLease{}, err
		}
		status := reply.Options.Status()
		if status == nil || status.StatusCode != iana.StatusSuccess {
			return DHCPv6PDLease{}, errDHCPv6RestartRejected
		}
		cached = expireDHCPv6Assignments(cached, config.Clock.Now())
		if len(cached.Addresses) == 0 {
			return DHCPv6PDLease{}, errDHCPv6RestartRejected
		}
		cached.ServerID = reply.Options.ServerID().ToBytes()
		cached.LinkIndex = link.Index
		return cached, nil
	}
	reply, err := exchangeDHCPv6Restart(ctx, transport, config, dhcpv6.MessageTypeRebind, cached, solicitMaxRT)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	validated, err := parseDHCPv6(reply, config, nil, link, nil)
	if err != nil {
		return DHCPv6PDLease{}, errDHCPv6RestartRejected
	}
	return validated, nil
}

func (client *DHCPv6PDClient) servicePrefixWhileAcquiringAddress(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, lease *DHCPv6PDLease, reacquireAddress *bool, solicitMaxRT *time.Duration, notifiedThrough *time.Time) bool {
	now := config.Clock.Now()
	*lease = expireDHCPv6Assignments(*lease, now)
	if len(lease.Prefixes) == 0 {
		client.publish(*lease)
		return false
	}
	if now.Before(lease.RenewAt) {
		return false
	}
	rebinding := !now.Before(lease.RebindAt)
	replacement, err := renewDHCPv6(ctx, transport, config, link, *lease, rebinding, solicitMaxRT)
	if err != nil {
		log.WarnContext(ctx, "dhcpv6: prefix renewal during address acquisition failed", "iface", config.Iface, "err", err)
		return false
	}
	*lease = replacement
	client.publish(*lease)
	*notifiedThrough = lease.AcquiredAt
	*reacquireAddress = config.RequestAddress && len(lease.Addresses) == 0
	return true
}

func (client *DHCPv6PDClient) acquireInNegotiation(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, lease *DHCPv6PDLease, reacquireAddress *bool, backoff, solicitMaxRT *time.Duration, notifiedThrough *time.Time) bool {
	var previous *DHCPv6PDLease
	if *reacquireAddress {
		previous = lease
	}
	replacement, err := acquireDHCPv6(ctx, transport, config, link, solicitMaxRT, previous)
	if err != nil {
		log.WarnContext(ctx, "dhcpv6: acquisition failed", "iface", config.Iface, "err", err)
	} else {
		*lease = replacement
		*reacquireAddress = config.RequestAddress && len(lease.Addresses) == 0
		client.publish(*lease)
		*notifiedThrough = lease.AcquiredAt
		if !*reacquireAddress {
			*backoff = time.Second
			return true
		}
	}
	wait := min(jitterDHCPv6(*backoff), *solicitMaxRT)
	if len(lease.Prefixes) != 0 {
		now := config.Clock.Now()
		wait = min(wait, latestDHCPv6Validity(*lease).Sub(now))
		if lease.RenewAt.After(now) {
			wait = min(wait, lease.RenewAt.Sub(now))
		}
	}
	address, ok := client.waitDHCPv6OrDecline(ctx, wait)
	if !ok {
		return false
	}
	client.processDHCPv6Decline(ctx, log, transport, config, lease, reacquireAddress, solicitMaxRT, address)
	*backoff = min(*backoff*2, *solicitMaxRT)
	return true
}

func dhcpv6WaitDuration(wait time.Duration, now, deadline time.Time) time.Duration {
	if !deadline.IsZero() {
		wait = min(wait, deadline.Sub(now))
	}
	return wait
}

func (client *DHCPv6PDClient) waitDHCPv6OrDecline(ctx context.Context, duration time.Duration) (netip.Addr, bool) {
	if duration < 0 {
		duration = 0
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return netip.Addr{}, false
	case <-timer.C:
		return netip.Addr{}, true
	case address := <-client.declines:
		return address, true
	}
}

func (client *DHCPv6PDClient) processDHCPv6Decline(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, lease *DHCPv6PDLease, reacquireAddress *bool, solicitMaxRT *time.Duration, address netip.Addr) {
	if !address.IsValid() {
		return
	}
	addresses := make([]DelegatedAddress, 0, len(lease.Addresses))
	found := false
	for _, assigned := range lease.Addresses {
		if assigned.Address == address {
			found = true
			continue
		}
		addresses = append(addresses, assigned)
	}
	if !found {
		return
	}
	lease.Addresses = addresses
	client.publish(*lease)
	*reacquireAddress = true
	declined := *lease
	declined.Addresses = []DelegatedAddress{{Address: address, PreferredUntil: time.Time{}, ValidUntil: time.Time{}}}
	if _, err := exchangeDHCPv6(ctx, transport, config, dhcpv6.MessageTypeDecline, lease.ServerID, &declined, solicitMaxRT); err != nil {
		log.WarnContext(ctx, "dhcpv6: decline failed", "iface", config.Iface, "address", address, "err", err)
	}
}

func nextDHCPv6Deadline(lease DHCPv6PDLease, after time.Time) time.Time {
	var next time.Time
	for _, prefix := range lease.Prefixes {
		for _, deadline := range []time.Time{prefix.PreferredUntil, prefix.ValidUntil} {
			if !deadline.After(after) {
				continue
			}
			if next.IsZero() || deadline.Before(next) {
				next = deadline
			}
		}
	}
	for _, address := range lease.Addresses {
		for _, deadline := range []time.Time{address.PreferredUntil, address.ValidUntil} {
			if deadline.After(after) && (next.IsZero() || deadline.Before(next)) {
				next = deadline
			}
		}
	}
	return next
}

func expireDHCPv6Assignments(lease DHCPv6PDLease, now time.Time) DHCPv6PDLease {
	prefixes := make([]DelegatedPrefix, 0, len(lease.Prefixes))
	for _, prefix := range lease.Prefixes {
		if now.Before(prefix.ValidUntil) {
			prefixes = append(prefixes, prefix)
		}
	}
	addresses := make([]DelegatedAddress, 0, len(lease.Addresses))
	for _, address := range lease.Addresses {
		if now.Before(address.ValidUntil) {
			addresses = append(addresses, address)
		}
	}
	lease.Prefixes = prefixes
	lease.Addresses = addresses
	return lease
}

func dhcpv6AssociationTimers(lease DHCPv6PDLease) (time.Time, time.Time) {
	var renewAt, rebindAt time.Time
	if len(lease.Prefixes) != 0 {
		renewAt, rebindAt = lease.RenewAt, lease.RebindAt
	}
	if len(lease.Addresses) != 0 {
		if renewAt.IsZero() || lease.IANARenewAt.Before(renewAt) {
			renewAt = lease.IANARenewAt
		}
		if rebindAt.IsZero() || lease.IANARebindAt.Before(rebindAt) {
			rebindAt = lease.IANARebindAt
		}
	}
	return renewAt, rebindAt
}

func acquireDHCPv6(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, solicitMaxRT *time.Duration, previous *DHCPv6PDLease) (DHCPv6PDLease, error) {
	advertise, err := exchangeDHCPv6(ctx, transport, config, dhcpv6.MessageTypeSolicit, nil, previous, solicitMaxRT)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	if _, err := parseDHCPv6(advertise, config, nil, link, previous); err != nil {
		return DHCPv6PDLease{}, err
	}
	return requestDHCPv6(ctx, transport, config, advertise, link, solicitMaxRT, previous)
}

func renewDHCPv6(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, lease DHCPv6PDLease, rebinding bool, solicitMaxRT *time.Duration) (DHCPv6PDLease, error) {
	messageType := dhcpv6.MessageTypeRenew
	serverID := lease.ServerID
	if rebinding {
		messageType = dhcpv6.MessageTypeRebind
		serverID = nil
	}
	reply, err := exchangeDHCPv6(ctx, transport, config, messageType, serverID, &lease, solicitMaxRT)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	return parseDHCPv6(reply, config, serverID, link, &lease)
}

func requestDHCPv6(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, advertise *dhcpv6.Message, link *net.Interface, solicitMaxRT *time.Duration, previous *DHCPv6PDLease) (DHCPv6PDLease, error) {
	server := advertise.Options.ServerID()
	if server == nil {
		return DHCPv6PDLease{}, errors.New("advertise has no server ID")
	}
	reply, err := exchangeDHCPv6(ctx, transport, config, dhcpv6.MessageTypeRequest, server.ToBytes(), previous, solicitMaxRT)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	return parseDHCPv6(reply, config, server.ToBytes(), link, previous)
}

func exchangeDHCPv6(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, messageType dhcpv6.MessageType, serverID []byte, previous *DHCPv6PDLease, solicitMaxRT *time.Duration) (*dhcpv6.Message, error) {
	duid, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: invalid configured DUID", "err", err)
		return nil, fmt.Errorf("decode DHCPv6 DUID: %w", err)
	}
	message, err := dhcpv6.NewMessage(dhcpv6.WithClientID(duid))
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: message creation failed", "err", err)
		return nil, fmt.Errorf("create DHCPv6 message: %w", err)
	}
	if len(serverID) != 0 {
		server, err := dhcpv6.DUIDFromBytes(serverID)
		if err != nil {
			slog.WarnContext(ctx, "dhcpv6: invalid server DUID", "err", err)
			return nil, fmt.Errorf("decode DHCPv6 server DUID: %w", err)
		}
		dhcpv6.WithServerID(server)(message)
	}
	configureDHCPv6Message(message, config, messageType, previous)
	expected := dhcpv6.MessageTypeReply
	if messageType == dhcpv6.MessageTypeSolicit {
		expected = dhcpv6.MessageTypeAdvertise
	}
	response, err := transport.SendAndRead(ctx, transport.RemoteAddr(), message, func(response *dhcpv6.Message) bool {
		if response.MessageType != expected {
			return false
		}
		client := response.Options.ClientID()
		server := response.Options.ServerID()
		if client == nil || server == nil || !bytes.Equal(client.ToBytes(), config.DUID) {
			return false
		}
		return len(serverID) == 0 || bytes.Equal(server.ToBytes(), serverID)
	})
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: exchange failed", "err", err)
		return nil, fmt.Errorf("exchange DHCPv6 message: %w", err)
	}
	updateDHCPv6SolMaxRT(response, solicitMaxRT)
	return response, nil
}

func configureDHCPv6Message(message *dhcpv6.Message, config DHCPv6PDConfig, messageType dhcpv6.MessageType, previous *DHCPv6PDLease) {
	message.MessageType = messageType
	message.AddOption(dhcpv6.OptElapsedTime(0))
	message.AddOption(dhcpv6.OptRequestedOption(dhcpv6.OptionSolMaxRT))
	if config.RequestPrefix && messageType != dhcpv6.MessageTypeDecline {
		var iaid [4]byte
		binary.BigEndian.PutUint32(iaid[:], config.IAID)
		var prefixes []*dhcpv6.OptIAPrefix
		if previous != nil {
			for _, prefix := range previous.Prefixes {
				if config.Clock.Now().Before(prefix.ValidUntil) {
					prefixes = append(prefixes, &dhcpv6.OptIAPrefix{Prefix: prefixToIPNet(prefix.Prefix)})
				}
			}
		} else if config.Hint.IsValid() {
			prefixes = append(prefixes, &dhcpv6.OptIAPrefix{Prefix: prefixToIPNet(config.Hint)})
		}
		dhcpv6.WithIAPD(iaid, prefixes...)(message)
	}
	if config.RequestAddress {
		var iaid [4]byte
		binary.BigEndian.PutUint32(iaid[:], config.IANAIAID)
		association := &dhcpv6.OptIANA{IaId: iaid}
		if previous != nil {
			for _, address := range previous.Addresses {
				if messageType == dhcpv6.MessageTypeDecline || config.Clock.Now().Before(address.ValidUntil) {
					association.Options.Add(&dhcpv6.OptIAAddress{IPv6Addr: net.IP(address.Address.AsSlice())})
				}
			}
		}
		message.AddOption(association)
	}
}

func updateDHCPv6SolMaxRT(message *dhcpv6.Message, solicitMaxRT *time.Duration) {
	option := message.GetOneOption(dhcpv6.OptionSolMaxRT)
	if option == nil {
		return
	}
	data := option.ToBytes()
	if len(data) != 4 {
		return
	}
	seconds := binary.BigEndian.Uint32(data)
	if seconds < 60 || seconds > 86400 {
		return
	}
	*solicitMaxRT = time.Duration(seconds) * time.Second
}

func parseDHCPv6(message *dhcpv6.Message, config DHCPv6PDConfig, expectedServer []byte, link *net.Interface, previous *DHCPv6PDLease) (DHCPv6PDLease, error) {
	server := message.Options.ServerID()
	client := message.Options.ClientID()
	if server == nil || client == nil || !bytes.Equal(client.ToBytes(), config.DUID) {
		return DHCPv6PDLease{}, errors.New("DHCPv6 response identity mismatch")
	}
	if len(expectedServer) != 0 && !bytes.Equal(server.ToBytes(), expectedServer) {
		return DHCPv6PDLease{}, errors.New("DHCPv6 server changed during request")
	}
	if status := message.Options.Status(); status != nil && status.StatusCode != iana.StatusSuccess {
		return DHCPv6PDLease{}, fmt.Errorf("DHCPv6 status %s", status.StatusCode)
	}
	now := config.Clock.Now()
	lease := DHCPv6PDLease{
		LinkName: link.Name, LinkIndex: link.Index, LinkHardwareAddr: bytes.Clone(link.HardwareAddr),
		DUID: bytes.Clone(config.DUID), IAID: config.IAID, IANAIAID: config.IANAIAID,
		ServerID: server.ToBytes(), AcquiredAt: now, RenewAt: time.Time{}, RebindAt: time.Time{},
		IANARenewAt: time.Time{}, IANARebindAt: time.Time{}, Prefixes: nil, Addresses: nil,
		RequestAddress: config.RequestAddress, RequestPrefix: config.RequestPrefix, Hint: config.Hint, WaitForRA: config.WaitForRA,
	}
	responded := false
	if config.RequestPrefix {
		var err error
		lease.Prefixes, lease.RenewAt, lease.RebindAt, responded, err = parseDHCPv6PrefixAssociation(message, config.IAID, previous, now)
		if err != nil {
			return DHCPv6PDLease{}, err
		}
	}
	if config.RequestAddress {
		var addressResponded bool
		var err error
		lease.Addresses, lease.IANARenewAt, lease.IANARebindAt, addressResponded, err = parseDHCPv6AddressAssociation(message, config.IANAIAID, previous, now)
		if err != nil {
			return DHCPv6PDLease{}, err
		}
		responded = responded || addressResponded
	}
	lease = expireDHCPv6Assignments(lease, now)
	if len(lease.Prefixes) == 0 && len(lease.Addresses) == 0 {
		if previous != nil && responded {
			return lease, nil
		}
		return DHCPv6PDLease{}, errors.New("DHCPv6 response has no valid association")
	}
	return lease, nil
}

func parseDHCPv6PrefixAssociation(message *dhcpv6.Message, iaid uint32, previous *DHCPv6PDLease, now time.Time) ([]DelegatedPrefix, time.Time, time.Time, bool, error) {
	var association *dhcpv6.OptIAPD
	for _, candidate := range message.Options.IAPD() {
		if binary.BigEndian.Uint32(candidate.IaId[:]) != iaid {
			continue
		}
		if association != nil {
			return nil, time.Time{}, time.Time{}, false, errors.New("duplicate IA_PD")
		}
		association = candidate
	}
	if association == nil {
		if previous == nil {
			return nil, time.Time{}, time.Time{}, false, nil
		}
		renewAt := previous.RenewAt
		if renewAt.Before(now.Add(30 * time.Second)) {
			renewAt = now.Add(30 * time.Second)
		}
		return append([]DelegatedPrefix(nil), previous.Prefixes...), renewAt, previous.RebindAt, false, nil
	}
	if !successfulDHCPv6Status(association.Options.Status()) {
		return nil, time.Time{}, time.Time{}, true, nil
	}
	prefixes, shortest := acceptedDHCPv6Prefixes(association, now)
	if shortest == 0 {
		return nil, time.Time{}, time.Time{}, true, nil
	}
	renewAt, rebindAt := dhcpv6Timers(now, association.T1, association.T2, shortest)
	return prefixes, renewAt, rebindAt, true, nil
}

func parseDHCPv6AddressAssociation(message *dhcpv6.Message, iaid uint32, previous *DHCPv6PDLease, now time.Time) ([]DelegatedAddress, time.Time, time.Time, bool, error) {
	var association *dhcpv6.OptIANA
	for _, candidate := range message.Options.IANA() {
		if binary.BigEndian.Uint32(candidate.IaId[:]) != iaid {
			continue
		}
		if association != nil {
			return nil, time.Time{}, time.Time{}, false, errors.New("duplicate IA_NA")
		}
		association = candidate
	}
	if association == nil {
		if previous == nil {
			return nil, time.Time{}, time.Time{}, false, nil
		}
		renewAt := previous.IANARenewAt
		if renewAt.Before(now.Add(30 * time.Second)) {
			renewAt = now.Add(30 * time.Second)
		}
		return append([]DelegatedAddress(nil), previous.Addresses...), renewAt, previous.IANARebindAt, false, nil
	}
	if !successfulDHCPv6Status(association.Options.Status()) {
		return nil, time.Time{}, time.Time{}, true, nil
	}
	addresses, shortest := acceptedDHCPv6Addresses(association, now)
	if shortest == 0 {
		return nil, time.Time{}, time.Time{}, true, nil
	}
	renewAt, rebindAt := dhcpv6Timers(now, association.T1, association.T2, shortest)
	return addresses, renewAt, rebindAt, true, nil
}

func successfulDHCPv6Status(status *dhcpv6.OptStatusCode) bool {
	return status == nil || status.StatusCode == iana.StatusSuccess
}

func dhcpv6Timers(now time.Time, t1, t2, shortest time.Duration) (time.Time, time.Time) {
	if t1 <= 0 || t2 <= 0 || t1 >= t2 || t2 >= shortest {
		t1, t2 = shortest/2, shortest*4/5
	}
	return now.Add(t1), now.Add(t2)
}

func acceptedDHCPv6Addresses(association *dhcpv6.OptIANA, now time.Time) ([]DelegatedAddress, time.Duration) {
	var addresses []DelegatedAddress
	shortest := time.Duration(0)
	for _, value := range association.Options.Addresses() {
		if value.PreferredLifetime < 0 || value.ValidLifetime <= 0 || value.PreferredLifetime > value.ValidLifetime {
			continue
		}
		if !successfulDHCPv6Status(value.Options.Status()) {
			continue
		}
		address, ok := netip.AddrFromSlice(value.IPv6Addr)
		if !ok || !address.Is6() || address.Is4In6() || address.IsMulticast() || address.IsUnspecified() || address.IsLinkLocalUnicast() {
			continue
		}
		addresses = append(addresses, DelegatedAddress{Address: address, PreferredUntil: now.Add(value.PreferredLifetime), ValidUntil: now.Add(value.ValidLifetime)})
		if shortest == 0 || value.ValidLifetime < shortest {
			shortest = value.ValidLifetime
		}
	}
	return addresses, shortest
}

func acceptedDHCPv6Prefixes(association *dhcpv6.OptIAPD, now time.Time) ([]DelegatedPrefix, time.Duration) {
	var prefixes []DelegatedPrefix
	shortest := time.Duration(0)
	for _, value := range association.Options.Prefixes() {
		if value.Prefix == nil || value.PreferredLifetime < 0 || value.ValidLifetime <= 0 || value.PreferredLifetime > value.ValidLifetime {
			continue
		}
		if status := value.Options.Status(); status != nil && status.StatusCode != iana.StatusSuccess {
			continue
		}
		prefix, err := netip.ParsePrefix(value.Prefix.String())
		if err != nil || !prefix.Addr().Is6() || prefix.Bits() > 64 {
			continue
		}
		prefixes = append(prefixes, DelegatedPrefix{Prefix: prefix.Masked(), PreferredUntil: now.Add(value.PreferredLifetime), ValidUntil: now.Add(value.ValidLifetime)})
		if shortest == 0 || value.ValidLifetime < shortest {
			shortest = value.ValidLifetime
		}
	}
	return prefixes, shortest
}

func prefixToIPNet(prefix netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: net.IP(prefix.Addr().AsSlice()), Mask: net.CIDRMask(prefix.Bits(), 128)}
}

func latestDHCPv6Validity(lease DHCPv6PDLease) time.Time {
	var latest time.Time
	for _, prefix := range lease.Prefixes {
		if prefix.ValidUntil.After(latest) {
			latest = prefix.ValidUntil
		}
	}
	for _, address := range lease.Addresses {
		if address.ValidUntil.After(latest) {
			latest = address.ValidUntil
		}
	}
	return latest
}

func waitDHCPv6(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func jitterDHCPv6(duration time.Duration) time.Duration {
	value, err := rand.Int(rand.Reader, big.NewInt(int64(duration/5)))
	if err != nil {
		return duration
	}
	return duration + time.Duration(value.Int64()) - duration/10
}
