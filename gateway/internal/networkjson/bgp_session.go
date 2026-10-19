package networkjson

import (
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/bgpsession"
	"goodkind.io/mwan/internal/config"
)

const defaultBGPPeerPort = 179

type bgpSessionWire struct {
	Name                string          `json:"name"`
	PeerAddress         string          `json:"peer-address"`
	PeerPort            *uint16         `json:"peer-port"`
	LocalAddress        string          `json:"local-address"`
	LocalAS             *uint32         `json:"local-as"`
	RemoteAS            *uint32         `json:"remote-as"`
	Families            []string        `json:"family"`
	MultihopTTL         *uint8          `json:"multihop-ttl"`
	Keepalive           *uint16         `json:"keepalive"`
	Hold                *uint16         `json:"hold"`
	ConnectRetry        *uint16         `json:"connect-retry"`
	GracefulRestartTime *uint16         `json:"graceful-restart-time"`
	RouteMetric         *uint32         `json:"route-metric"`
	Import              []bgpImportWire `json:"import"`
	Export              []bgpExportWire `json:"export"`
}

func buildBGPSessions(label string, entry ifaceEntry) ([]config.BGPSession, error) {
	wires := entry.WAN.BGPSessions
	if len(wires) == 0 {
		return nil, nil
	}
	if entry.IPv6 == nil {
		return nil, fmt.Errorf("%s: a bgp-session requires an ipv6 family", label)
	}
	sessions := make([]config.BGPSession, 0, len(wires))
	names := make(map[string]bool, len(wires))
	for _, wire := range wires {
		if wire.Name == "" {
			return nil, fmt.Errorf("%s: bgp-session name is required", label)
		}
		if names[wire.Name] {
			return nil, fmt.Errorf("%s: bgp-session name %q is duplicated", label, wire.Name)
		}
		names[wire.Name] = true
		session, err := buildBGPSession(label+" bgp-session "+wire.Name, wire)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func buildBGPSession(label string, wire bgpSessionWire) (config.BGPSession, error) {
	var none config.BGPSession
	session := bgpsession.Config{
		Name: wire.Name, RouterID: netip.Addr{}, Families: nil, LocalASN: 0, RemoteASN: 0,
		PeerAddress: netip.Addr{}, PeerPort: defaultBGPPeerPort, LocalAddress: netip.Addr{},
		Interface: "", MultihopTTL: 0, KeepaliveSeconds: 0, HoldSeconds: 0, ConnectRetrySeconds: 0,
		GracefulRestartSeconds: 0, Import: nil, Export: nil, Tables: nil, RouteMetric: 0,
	}
	if err := setBGPSessionPeers(label, wire, &session); err != nil {
		return none, err
	}
	families, err := buildBGPFamilies(label, wire.Families)
	if err != nil {
		return none, err
	}
	session.Families = families
	if err := setBGPSessionTimers(label, wire, &session); err != nil {
		return none, err
	}
	if wire.RouteMetric == nil {
		return none, fmt.Errorf("%s: route-metric is required", label)
	}
	if *wire.RouteMetric == 0 || *wire.RouteMetric == bgpsession.KernelDefaultIPv6Metric {
		return none, fmt.Errorf("%s: route-metric %d collides with the kernel default metric %d",
			label, *wire.RouteMetric, bgpsession.KernelDefaultIPv6Metric)
	}
	session.RouteMetric = *wire.RouteMetric
	importRules, err := buildBGPImports(label, wire.Import)
	if err != nil {
		return none, err
	}
	session.Import = importRules
	exportRules, backupFor, err := buildBGPExports(label, wire.Export)
	if err != nil {
		return none, err
	}
	session.Export = exportRules
	validated, err := bgpsession.Validated(session)
	if err != nil {
		slog.Error("networkjson: bgp-session invalid", "session", label, "err", err)
		return none, fmt.Errorf("%s: %w", label, err)
	}
	return config.BGPSession{Session: validated, BackupFor: backupFor}, nil
}

func setBGPSessionPeers(label string, wire bgpSessionWire, session *bgpsession.Config) error {
	if wire.PeerAddress == "" {
		return fmt.Errorf("%s: peer-address is required", label)
	}
	peer, err := parseAddress(label, "peer-address", wire.PeerAddress)
	if err != nil {
		return err
	}
	session.PeerAddress = peer
	if wire.PeerPort != nil {
		session.PeerPort = *wire.PeerPort
	}
	if wire.LocalAddress != "" {
		local, err := parseAddress(label, "local-address", wire.LocalAddress)
		if err != nil {
			return err
		}
		if local.Is4() != peer.Is4() {
			return fmt.Errorf("%s: local-address %s and peer-address %s use different address families",
				label, local, peer)
		}
		session.LocalAddress = local
	}
	numbers := []struct {
		leaf   string
		value  *uint32
		target *uint32
	}{
		{leaf: "local-as", value: wire.LocalAS, target: &session.LocalASN},
		{leaf: "remote-as", value: wire.RemoteAS, target: &session.RemoteASN},
	}
	for _, number := range numbers {
		if number.value == nil {
			return fmt.Errorf("%s: %s is required", label, number.leaf)
		}
		if *number.value == 0 {
			return fmt.Errorf("%s: %s must not be 0", label, number.leaf)
		}
		*number.target = *number.value
	}
	if wire.MultihopTTL != nil {
		if *wire.MultihopTTL == 0 {
			return fmt.Errorf("%s: multihop-ttl must be between 1 and 255", label)
		}
		session.MultihopTTL = *wire.MultihopTTL
	}
	return nil
}

func buildBGPFamilies(label string, raws []string) ([]bgpsession.Family, error) {
	// networkjson.Decode reads the raw JSON document, which does not include
	// defaults that libyang adds to its parsed tree. An absent or empty family
	// leaf-list needs the schema default of ipv6-unicast here.
	if len(raws) == 0 {
		return []bgpsession.Family{bgpsession.FamilyIPv6Unicast}, nil
	}
	families := make([]bgpsession.Family, 0, len(raws))
	for _, raw := range raws {
		family, known := bgpsession.ParseFamily(raw)
		if !known {
			return nil, fmt.Errorf("%s: family %q must be %s", label, raw, bgpsession.FamilyIPv6Unicast)
		}
		families = append(families, family)
	}
	return families, nil
}

func setBGPSessionTimers(label string, wire bgpSessionWire, session *bgpsession.Config) error {
	if wire.Hold == nil {
		return fmt.Errorf("%s: hold is required", label)
	}
	if wire.Keepalive == nil {
		return fmt.Errorf("%s: keepalive is required", label)
	}
	hold := *wire.Hold
	if hold != 0 && hold < bgpsession.MinHoldSeconds {
		return fmt.Errorf("%s: hold %d must be 0 or between %d and 65535", label, hold, bgpsession.MinHoldSeconds)
	}
	if hold == 0 {
		return fmt.Errorf("%s: hold 0 disables the hold timer, and the embedded speaker cannot disable it", label)
	}
	if *wire.Keepalive >= hold {
		return fmt.Errorf("%s: keepalive %d must be below hold %d", label, *wire.Keepalive, hold)
	}
	session.HoldSeconds = uint32(hold)
	session.KeepaliveSeconds = uint32(*wire.Keepalive)
	if wire.ConnectRetry != nil {
		if *wire.ConnectRetry < bgpsession.MinConnectRetrySeconds {
			return fmt.Errorf("%s: connect-retry %d must be at least %d", label, *wire.ConnectRetry, bgpsession.MinConnectRetrySeconds)
		}
		session.ConnectRetrySeconds = uint32(*wire.ConnectRetry)
	}
	if wire.GracefulRestartTime != nil {
		restart := *wire.GracefulRestartTime
		if restart == 0 || restart > bgpsession.MaxGracefulRestartSeconds {
			return fmt.Errorf("%s: graceful-restart-time %d must be between 1 and %d", label, restart, bgpsession.MaxGracefulRestartSeconds)
		}
		session.GracefulRestartSeconds = uint32(restart)
	}
	return nil
}
