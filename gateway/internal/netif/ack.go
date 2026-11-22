package netif

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
)

var (
	errMissingDHCPACK          = errors.New("missing DHCP ACK")
	errInvalidAssignedAddress  = errors.New("invalid assigned address")
	errMissingDHCPSubnetMask   = errors.New("missing subnet mask")
	errInvalidDHCPSubnetMask   = errors.New("invalid subnet mask")
	errInvalidDHCPServer       = errors.New("invalid server identifier")
	errMissingDHCPLeaseTime    = errors.New("missing or zero lease time")
	errEmptyDHCPClasslessRoute = errors.New("empty classless route option")
)

const (
	ipv4PrefixBits              = 32
	defaultRenewalNumerator     = 1
	defaultRenewalDenominator   = 2
	defaultRebindingNumerator   = 7
	defaultRebindingDenominator = 8
)

// leaseToInfo converts a DHCP ACK into values used by the gateway.
func leaseToInfo(
	state LeaseState,
	lease *nclient4.Lease,
	acquired time.Time,
	requestedAt time.Time,
) LeaseInfo {
	info := leaseState(state, acquired, nil)
	if lease == nil || lease.ACK == nil {
		return info
	}

	ack := lease.ACK
	info.IP = ack.YourIPAddr
	mask := ack.SubnetMask()
	if mask != nil {
		info.PrefixLen, _ = mask.Size()
	}

	if ack.Options.Has(dhcpv4.OptionClasslessStaticRoute) {
		info.Routes, _ = classlessRoutes(ack)
		for _, route := range info.Routes {
			if ones, _ := route.Destination.Mask.Size(); ones == 0 {
				info.Gateway = route.Gateway

				break
			}
		}
	} else if routers := ack.Router(); len(routers) > 0 {
		info.Gateway = routers[0]
		info.Routes = []LeaseRoute{
			{
				Destination: &net.IPNet{
					IP:   net.IPv4zero,
					Mask: net.CIDRMask(0, ipv4PrefixBits),
				},
				Gateway: routers[0],
			},
		}
	}

	info.Server = ack.ServerIdentifier()
	for _, server := range ack.DNS() {
		address, ok := netip.AddrFromSlice(server.To4())
		if ok && !address.IsUnspecified() && !address.IsMulticast() &&
			address != netip.MustParseAddr("255.255.255.255") {
			info.DNS = append(info.DNS, address.Unmap())
		}
	}

	info.LeaseTime = ack.IPAddressLeaseTime(0)
	renewalTime := ack.IPAddressRenewalTime(0)
	rebindingTime := ack.IPAddressRebindingTime(0)
	if renewalTime <= 0 || renewalTime >= info.LeaseTime {
		renewalTime = info.LeaseTime * defaultRenewalNumerator / defaultRenewalDenominator
	}

	if rebindingTime <= renewalTime || rebindingTime >= info.LeaseTime {
		rebindingTime = info.LeaseTime * defaultRebindingNumerator / defaultRebindingDenominator
	}

	if renewalTime >= rebindingTime {
		renewalTime = info.LeaseTime * defaultRenewalNumerator / defaultRenewalDenominator
		rebindingTime = info.LeaseTime * defaultRebindingNumerator / defaultRebindingDenominator
	}

	info.RenewAt = requestedAt.Add(renewalTime)
	info.RebindAt = requestedAt.Add(rebindingTime)
	info.ExpiresAt = requestedAt.Add(info.LeaseTime)

	return info
}

func validateLease(lease *nclient4.Lease) error {
	if lease == nil || lease.ACK == nil {
		return errMissingDHCPACK
	}

	ack := lease.ACK
	address := ack.YourIPAddr.To4()
	if address == nil || address.IsUnspecified() || address.IsMulticast() ||
		address.Equal(net.IPv4bcast) {
		slog.Warn("dhcp: invalid ACK address", "address", ack.YourIPAddr)

		return fmt.Errorf("%w: %v", errInvalidAssignedAddress, ack.YourIPAddr)
	}

	mask := ack.SubnetMask()
	if mask == nil {
		return errMissingDHCPSubnetMask
	}

	if _, bits := mask.Size(); bits != net.IPv4len*8 {
		return errInvalidDHCPSubnetMask
	}

	server := ack.ServerIdentifier().To4()
	if server == nil || server.IsUnspecified() || server.IsMulticast() ||
		server.Equal(net.IPv4bcast) {
		slog.Warn("dhcp: invalid ACK server identifier", "server", ack.ServerIdentifier())

		return fmt.Errorf("%w: %v", errInvalidDHCPServer, ack.ServerIdentifier())
	}

	if ack.IPAddressLeaseTime(0) <= 0 {
		return errMissingDHCPLeaseTime
	}

	if ack.Options.Has(dhcpv4.OptionClasslessStaticRoute) {
		if _, err := classlessRoutes(ack); err != nil {
			return err
		}
	}

	return nil
}

func classlessRoutes(ack *dhcpv4.DHCPv4) ([]LeaseRoute, error) {
	data := ack.Options.Get(dhcpv4.OptionClasslessStaticRoute)
	if len(data) == 0 {
		return nil, errEmptyDHCPClasslessRoute
	}

	var parsed dhcpv4.Routes
	if err := parsed.FromBytes(data); err != nil {
		slog.Warn("dhcp: invalid ACK classless routes", "err", err)

		return nil, fmt.Errorf("invalid classless route option: %w", err)
	}

	routes := make([]LeaseRoute, 0, len(parsed))
	for _, route := range parsed {
		destination := &net.IPNet{
			IP:   route.Dest.IP.Mask(route.Dest.Mask),
			Mask: append(net.IPMask(nil), route.Dest.Mask...),
		}
		routes = append(
			routes,
			LeaseRoute{
				Destination: destination,
				Gateway:     append(net.IP(nil), route.Router...),
			},
		)
	}

	return routes, nil
}
