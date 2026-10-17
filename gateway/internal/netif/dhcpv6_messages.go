package netif

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
)

func acquireDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, solicitMaxRT *time.Duration, previous *DHCPv6PDLease, end time.Time, solicitCount int) (DHCPv6PDLease, error) {
	solicit := dhcpv6Retransmission{
		maxDelay: dhcpv6SolicitMaxDelay, initial: dhcpv6SolicitTimeout, maximum: *solicitMaxRT,
		count: solicitCount, duration: 0, end: end,
	}
	advertise, err := exchangeDHCPv6(ctx, log, transport, config, dhcpv6.MessageTypeSolicit, solicit, nil, previous, solicitMaxRT, nil)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	if _, err := parseDHCPv6(advertise, config, nil, link, previous); err != nil {
		return DHCPv6PDLease{}, err
	}
	return requestDHCPv6(ctx, log, transport, config, advertise, link, solicitMaxRT, previous, end)
}

func renewDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, lease *DHCPv6PDLease, rebinding bool, rebindAt time.Time, solicitMaxRT *time.Duration, service *dhcpv6ExchangeService) (DHCPv6PDLease, error) {
	messageType := dhcpv6.MessageTypeRenew
	serverID := bytes.Clone(lease.ServerID)
	validUntil := latestDHCPv6Validity(*lease)
	timing := dhcpv6Retransmission{
		maxDelay: 0, initial: dhcpv6RenewTimeout, maximum: dhcpv6RenewMaxRT,
		count: 0, duration: 0, end: earliestDHCPv6Time(validUntil, rebindAt),
	}
	if rebinding {
		messageType = dhcpv6.MessageTypeRebind
		serverID = nil
		timing.initial, timing.maximum, timing.end = dhcpv6RebindTimeout, dhcpv6RebindMaxRT, validUntil
	}
	reply, err := exchangeDHCPv6(ctx, log, transport, config, messageType, timing, serverID, lease, solicitMaxRT, service)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	return parseDHCPv6(reply, config, serverID, link, lease)
}

func requestDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, advertise *dhcpv6.Message, link *net.Interface, solicitMaxRT *time.Duration, previous *DHCPv6PDLease, end time.Time) (DHCPv6PDLease, error) {
	server := advertise.Options.ServerID()
	if server == nil {
		return DHCPv6PDLease{}, errors.New("advertise has no server ID")
	}
	timing := dhcpv6Retransmission{
		maxDelay: 0, initial: dhcpv6RequestTimeout, maximum: dhcpv6RequestMaxRT,
		count: dhcpv6RequestMaxCount, duration: 0, end: end,
	}
	reply, err := exchangeDHCPv6(ctx, log, transport, config, dhcpv6.MessageTypeRequest, timing, server.ToBytes(), previous, solicitMaxRT, nil)
	if err != nil {
		return DHCPv6PDLease{}, err
	}
	return parseDHCPv6(reply, config, server.ToBytes(), link, previous)
}

// exchangeDHCPv6 rebuilds each transmission from previous. A retransmission omits
// any assignment that expired or that the client declined during the exchange.
func exchangeDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, messageType dhcpv6.MessageType, timing dhcpv6Retransmission, serverID []byte, previous *DHCPv6PDLease, solicitMaxRT *time.Duration, service *dhcpv6ExchangeService) (*dhcpv6.Message, error) {
	var server dhcpv6.DUID
	if len(serverID) != 0 {
		var err error
		server, err = dhcpv6.DUIDFromBytes(serverID)
		if err != nil {
			log.WarnContext(ctx, "dhcpv6: invalid server DUID", "iface", config.Iface, "err", err)
			return nil, fmt.Errorf("decode DHCPv6 server DUID: %w", err)
		}
	}
	prepare := func(message *dhcpv6.Message) error {
		if server != nil {
			dhcpv6.WithServerID(server)(message)
		}
		configureDHCPv6Message(message, config, messageType, previous)
		return nil
	}
	expected := dhcpv6.MessageTypeReply
	if messageType == dhcpv6.MessageTypeSolicit {
		expected = dhcpv6.MessageTypeAdvertise
	}
	match := matchDHCPv6Response(config, expected, serverID)
	response, err := exchangeDHCPv6Retransmitting(ctx, log, transport, config, messageType, timing, prepare, match, service)
	if err != nil {
		log.WarnContext(ctx, "dhcpv6: exchange failed", "iface", config.Iface, "message", messageType, "err", err)
		return nil, fmt.Errorf("exchange DHCPv6 %s: %w", messageType, err)
	}
	updateDHCPv6SolMaxRT(response, solicitMaxRT)
	return response, nil
}

// matchDHCPv6Response accepts a reply from any server when serverID is empty.
func matchDHCPv6Response(config DHCPv6PDConfig, expected dhcpv6.MessageType, serverID []byte) nclient6.Matcher {
	return func(response *dhcpv6.Message) bool {
		if response.MessageType != expected {
			return false
		}
		client := response.Options.ClientID()
		server := response.Options.ServerID()
		if client == nil || server == nil || !bytes.Equal(client.ToBytes(), config.DUID) {
			return false
		}
		return len(serverID) == 0 || bytes.Equal(server.ToBytes(), serverID)
	}
}

// renewalService publishes lifetime deadlines and withdraws declined addresses while
// a Renew or Rebind exchange waits for its reply.
func (client *DHCPv6PDClient) renewalService(log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, lease *DHCPv6PDLease, reacquireAddress *bool, solicitMaxRT *time.Duration, notifiedThrough *time.Time) *dhcpv6ExchangeService {
	return &dhcpv6ExchangeService{
		next: func() time.Time {
			return nextDHCPv6Deadline(*lease, *notifiedThrough)
		},
		expire: func(context.Context) bool {
			now := config.Clock.Now()
			deadline := nextDHCPv6Deadline(*lease, *notifiedThrough)
			if deadline.IsZero() || now.Before(deadline) {
				return true
			}
			*lease = expireDHCPv6Assignments(*lease, now)
			client.publish(*lease)
			*notifiedThrough = deadline
			return len(lease.Prefixes) != 0 || len(lease.Addresses) != 0
		},
		declines: client.declines,
		decline: func(ctx context.Context, address netip.Addr) bool {
			assigned := len(lease.Addresses)
			client.processDHCPv6Decline(ctx, log, transport, config, lease, reacquireAddress, solicitMaxRT, address)
			// A reply to the current exchange can still assign the declined address.
			// The next exchange starts from the lease without it.
			return len(lease.Addresses) == assigned
		},
	}
}
