package netif

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// DNSProbeSpec requires an explicit resolver destination and source binding.
type DNSProbeSpec struct {
	Interface string
	Family    string
	Source    netip.Addr
	Server    string
	Name      string
	Timeout   time.Duration
}

// DNSProbeResult records resolver replies and the actual query socket endpoints.
type DNSProbeResult struct {
	Addresses []netip.Addr
	Source    netip.Addr
	Server    netip.Addr
}

// DNSProbe queries the configured server through a bound standard resolver.
func DNSProbe(ctx context.Context, spec DNSProbeSpec) (observed DNSProbeResult, probeError error) {
	defer func() {
		if probeError != nil {
			slog.WarnContext(ctx, "DNS probe failed", "err", probeError)
		}
	}()
	var result DNSProbeResult
	var resultMutex sync.Mutex
	lookupNetwork := "ip4"
	networkSuffix := "4"
	if spec.Family == "inet6" {
		lookupNetwork = "ip6"
		networkSuffix = "6"
	} else if spec.Family != "inet" {
		return result, fmt.Errorf("unsupported DNS family %q", spec.Family)
	}
	server, err := netip.ParseAddrPort(spec.Server)
	if err != nil {
		return result, fmt.Errorf("DNS server: %w", err)
	}
	if server.Addr().Is4() != (networkSuffix == "4") {
		return result, fmt.Errorf("DNS server does not match family %q", spec.Family)
	}
	resolver := &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(dialContext context.Context, network, _ string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: spec.Timeout}
		if spec.Interface != "" {
			dialer.Control = bindToDevice(spec.Interface)
		}
		if spec.Source.IsValid() {
			if network == "tcp" {
				dialer.LocalAddr = &net.TCPAddr{IP: spec.Source.AsSlice(), Zone: spec.Source.Zone(), Port: 0}
			} else {
				dialer.LocalAddr = &net.UDPAddr{IP: spec.Source.AsSlice(), Zone: spec.Source.Zone(), Port: 0}
			}
		}
		connection, dialErr := dialer.DialContext(dialContext, network+networkSuffix, server.String())
		if dialErr != nil {
			return nil, fmt.Errorf("dial configured DNS server: %w", dialErr)
		}
		// A single requested address family queries one server. TCP fallback
		// uses the same source address and destination as the UDP request.
		resultMutex.Lock()
		switch address := connection.LocalAddr().(type) {
		case *net.UDPAddr:
			result.Source = address.AddrPort().Addr().Unmap()
		case *net.TCPAddr:
			result.Source = address.AddrPort().Addr().Unmap()
		}
		result.Server = server.Addr()
		resultMutex.Unlock()
		return connection, nil
	}}
	queryContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	addresses, err := resolver.LookupNetIP(queryContext, lookupNetwork, spec.Name)
	resultMutex.Lock()
	observed = result
	resultMutex.Unlock()
	observed.Addresses = addresses
	if err != nil {
		return observed, fmt.Errorf("query configured DNS server: %w", err)
	}
	return observed, nil
}
