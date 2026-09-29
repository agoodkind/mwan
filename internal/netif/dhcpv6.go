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

// DHCPv6PDConfig identifies one delegated-prefix association on one link.
type DHCPv6PDConfig struct {
	Iface     string
	DUID      []byte
	IAID      uint32
	Hint      netip.Prefix
	Clock     internalclock.Clock
	WaitForRA bool
}

// DelegatedPrefix records the server's association and absolute lifetimes.
type DelegatedPrefix struct {
	Prefix         netip.Prefix
	PreferredUntil time.Time
	ValidUntil     time.Time
}

// DHCPv6PDLease retains protocol metadata for renewal and future recovery.
type DHCPv6PDLease struct {
	LinkIndex        int
	LinkHardwareAddr net.HardwareAddr
	DUID             []byte
	IAID             uint32
	ServerID         []byte
	AcquiredAt       time.Time
	RenewAt          time.Time
	RebindAt         time.Time
	Prefixes         []DelegatedPrefix
}

// DHCPv6PDClient negotiates one IA_PD and publishes each lease replacement.
type DHCPv6PDClient struct {
	mu     sync.RWMutex
	lease  DHCPv6PDLease
	Events chan struct{}
}

// DHCPv6PDStore publishes current MWAN-owned delegations to consumers.
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
	store.leases[iface] = lease
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
	return lease, ok
}

// LastLease returns the most recent association snapshot.
func (client *DHCPv6PDClient) LastLease() DHCPv6PDLease {
	client.mu.RLock()
	defer client.mu.RUnlock()
	lease := client.lease
	lease.DUID = bytes.Clone(lease.DUID)
	lease.ServerID = bytes.Clone(lease.ServerID)
	lease.LinkHardwareAddr = bytes.Clone(lease.LinkHardwareAddr)
	lease.Prefixes = append([]DelegatedPrefix(nil), lease.Prefixes...)
	return lease
}

func (client *DHCPv6PDClient) publish(lease DHCPv6PDLease) {
	client.mu.Lock()
	client.lease = lease
	client.mu.Unlock()
	// The watcher reads the latest lease, so one pending event covers later replacements.
	select {
	case client.Events <- struct{}{}:
	default:
	}
}

// StartDHCPv6PDClient starts a fresh negotiation with the configured identity.
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
	var emptyLease DHCPv6PDLease
	client := &DHCPv6PDClient{mu: sync.RWMutex{}, lease: emptyLease, Events: make(chan struct{}, 1)}
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
	for ctx.Err() == nil {
		link, err := net.InterfaceByName(config.Iface)
		if err != nil {
			log.WarnContext(ctx, "dhcpv6: link unavailable", "iface", config.Iface, "err", err)
			if !waitDHCPv6(ctx, time.Second) {
				return
			}
			continue
		}
		if config.WaitForRA && !waitDHCPv6RA(ctx, log, config.Iface, link.Index) {
			if !waitDHCPv6(ctx, time.Second) {
				return
			}
			continue
		}
		transport, err := nclient6.New(config.Iface, nclient6.WithRetry(1), nclient6.WithTimeout(time.Second))
		if err != nil {
			log.WarnContext(ctx, "dhcpv6: socket unavailable", "iface", config.Iface, "err", err)
			if !waitDHCPv6(ctx, time.Second) {
				return
			}
			continue
		}
		client.negotiate(ctx, log, transport, config, link)
		if err := transport.Close(); err != nil {
			log.WarnContext(ctx, "dhcpv6: close socket failed", "err", err)
		}
	}
}

func waitDHCPv6RA(ctx context.Context, log *slog.Logger, iface string, linkIndex int) bool {
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
		if err == nil && (advertisement.ManagedConfiguration || advertisement.OtherConfiguration) {
			link, err = net.InterfaceByName(iface)
			return err == nil && link.Index == linkIndex && ctx.Err() == nil
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			log.WarnContext(ctx, "dhcpv6: RA read failed", "iface", iface, "err", err)
			return false
		}
		if !waitDHCPv6(ctx, time.Second) {
			return false
		}
	}
	return false
}

func (client *DHCPv6PDClient) negotiate(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface) {
	var lease DHCPv6PDLease
	backoff := time.Second
	solicitMaxRT := time.Hour
	var notifiedThrough time.Time
	for ctx.Err() == nil {
		if len(lease.Prefixes) == 0 {
			var err error
			lease, err = acquireDHCPv6(ctx, transport, config, link, &solicitMaxRT)
			if err != nil {
				log.WarnContext(ctx, "dhcpv6: acquisition failed", "iface", config.Iface, "err", err)
				if !waitDHCPv6(ctx, min(jitterDHCPv6(backoff), solicitMaxRT)) {
					return
				}
				backoff = min(backoff*2, solicitMaxRT)
				continue
			}
			backoff = time.Second
			client.publish(lease)
			notifiedThrough = lease.AcquiredAt
		}
		now := config.Clock.Now()
		validUntil := latestDHCPv6Validity(lease.Prefixes)
		deadline := nextDHCPv6PrefixDeadline(lease.Prefixes, notifiedThrough)
		if !deadline.IsZero() && !now.Before(deadline) {
			client.publish(lease)
			notifiedThrough = deadline
			continue
		}
		if !now.Before(validUntil) {
			lease = DHCPv6PDLease{LinkIndex: 0, LinkHardwareAddr: nil, DUID: nil, IAID: 0, ServerID: nil, AcquiredAt: time.Time{}, RenewAt: time.Time{}, RebindAt: time.Time{}, Prefixes: nil}
			backoff = time.Second
			client.publish(lease)
			continue
		}
		if now.Before(lease.RenewAt) {
			if !waitBeforeDHCPv6Deadline(ctx, min(lease.RenewAt.Sub(now), validUntil.Sub(now)), now, deadline) {
				return
			}
			continue
		}
		rebinding := !now.Before(lease.RebindAt)
		replacement, err := renewDHCPv6(ctx, transport, config, link, lease, rebinding, &solicitMaxRT)
		if err == nil {
			lease = replacement
			client.publish(lease)
			notifiedThrough = lease.AcquiredAt
			backoff = time.Second
			continue
		}
		log.WarnContext(ctx, "dhcpv6: renewal failed", "iface", config.Iface, "err", err)
		retryDeadline := lease.RebindAt
		if rebinding {
			retryDeadline = validUntil
		}
		now = config.Clock.Now()
		if !waitBeforeDHCPv6Deadline(ctx, min(jitterDHCPv6(backoff), retryDeadline.Sub(now)), now, deadline) {
			return
		}
		backoff = min(backoff*2, 120*time.Second)
	}
}

func waitBeforeDHCPv6Deadline(ctx context.Context, wait time.Duration, now, deadline time.Time) bool {
	if !deadline.IsZero() {
		wait = min(wait, deadline.Sub(now))
	}
	return waitDHCPv6(ctx, wait)
}

func nextDHCPv6PrefixDeadline(prefixes []DelegatedPrefix, after time.Time) time.Time {
	var next time.Time
	for _, prefix := range prefixes {
		for _, deadline := range []time.Time{prefix.PreferredUntil, prefix.ValidUntil} {
			if !deadline.After(after) {
				continue
			}
			if next.IsZero() || deadline.Before(next) {
				next = deadline
			}
		}
	}
	return next
}

func acquireDHCPv6(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, solicitMaxRT *time.Duration) (DHCPv6PDLease, error) {
	advertise, err := exchangeDHCPv6(ctx, transport, config, dhcpv6.MessageTypeSolicit, nil, nil, solicitMaxRT)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	if _, err := parseDHCPv6PD(advertise, config, nil, link); err != nil {
		return DHCPv6PDLease{}, err
	}
	return requestDHCPv6(ctx, transport, config, advertise, link, solicitMaxRT)
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
	return parseDHCPv6PD(reply, config, serverID, link)
}

func requestDHCPv6(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, advertise *dhcpv6.Message, link *net.Interface, solicitMaxRT *time.Duration) (DHCPv6PDLease, error) {
	server := advertise.Options.ServerID()
	if server == nil {
		return DHCPv6PDLease{}, errors.New("advertise has no server ID")
	}
	reply, err := exchangeDHCPv6(ctx, transport, config, dhcpv6.MessageTypeRequest, server.ToBytes(), nil, solicitMaxRT)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	return parseDHCPv6PD(reply, config, server.ToBytes(), link)
}

func exchangeDHCPv6(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, messageType dhcpv6.MessageType, serverID []byte, previous *DHCPv6PDLease, solicitMaxRT *time.Duration) (*dhcpv6.Message, error) {
	duid, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: configured DUID invalid", "err", err)
		return nil, fmt.Errorf("decode DHCPv6 DUID: %w", err)
	}
	message, err := dhcpv6.NewMessage(dhcpv6.WithClientID(duid))
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: message creation failed", "err", err)
		return nil, fmt.Errorf("create DHCPv6 message: %w", err)
	}
	message.MessageType = messageType
	message.AddOption(dhcpv6.OptElapsedTime(0))
	message.AddOption(dhcpv6.OptRequestedOption(dhcpv6.OptionSolMaxRT))
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
	if len(serverID) != 0 {
		server, err := dhcpv6.DUIDFromBytes(serverID)
		if err != nil {
			slog.WarnContext(ctx, "dhcpv6: server DUID invalid", "err", err)
			return nil, fmt.Errorf("decode DHCPv6 server DUID: %w", err)
		}
		dhcpv6.WithServerID(server)(message)
	}
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

func parseDHCPv6PD(message *dhcpv6.Message, config DHCPv6PDConfig, expectedServer []byte, link *net.Interface) (DHCPv6PDLease, error) {
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
	var association *dhcpv6.OptIAPD
	for _, candidate := range message.Options.IAPD() {
		if binary.BigEndian.Uint32(candidate.IaId[:]) == config.IAID {
			if association != nil {
				return DHCPv6PDLease{}, errors.New("duplicate IA_PD")
			}
			association = candidate
		}
	}
	if association == nil {
		return DHCPv6PDLease{}, errors.New("response has no matching IA_PD")
	}
	if status := association.Options.Status(); status != nil && status.StatusCode != iana.StatusSuccess {
		return DHCPv6PDLease{}, fmt.Errorf("IA_PD status %s", status.StatusCode)
	}
	now := config.Clock.Now()
	lease := DHCPv6PDLease{LinkIndex: link.Index, LinkHardwareAddr: bytes.Clone(link.HardwareAddr), DUID: bytes.Clone(config.DUID), IAID: config.IAID, ServerID: server.ToBytes(), AcquiredAt: now, RenewAt: time.Time{}, RebindAt: time.Time{}, Prefixes: nil}
	prefixes, shortest := acceptedDHCPv6Prefixes(association, now)
	lease.Prefixes = prefixes
	if len(lease.Prefixes) == 0 {
		return DHCPv6PDLease{}, errors.New("IA_PD has no valid prefix")
	}
	t1, t2 := association.T1, association.T2
	if t1 <= 0 || t2 <= 0 || t1 >= t2 || t2 >= shortest {
		t1, t2 = shortest/2, shortest*4/5
	}
	lease.RenewAt, lease.RebindAt = now.Add(t1), now.Add(t2)
	return lease, nil
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

func latestDHCPv6Validity(prefixes []DelegatedPrefix) time.Time {
	var latest time.Time
	for _, prefix := range prefixes {
		if prefix.ValidUntil.After(latest) {
			latest = prefix.ValidUntil
		}
	}
	return latest
}

func waitDHCPv6(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
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
