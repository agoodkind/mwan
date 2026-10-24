package reporting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
)

type persistentEcho struct {
	session        *netif.EchoSession
	source, target netip.Addr
	opened         time.Time
}

type Collector struct {
	settings   config.ReportSection
	network    *networkjson.Config
	log        *slog.Logger
	host       hostSampler
	talkers    *talkerAccounting
	identifier uint16
	mu         sync.Mutex
	persistent map[ProbeKey]persistentEcho
	udp        map[string]uint64
}

func NewCollector(settings config.ReportSection, network *networkjson.Config, log *slog.Logger) (*Collector, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if network == nil || log == nil {
		return nil, errors.New("reporting collector requires a network configuration and logger")
	}
	collector := &Collector{settings: settings, network: network, log: log, host: hostSampler{directory: settings.ProcDirectory}, persistent: make(map[ProbeKey]persistentEcho)}
	if _, err := os.Stat(filepath.Join(settings.ProcDirectory, "stat")); err != nil {
		return nil, fmt.Errorf("open procfs CPU measurements: %w", err)
	}
	local := make(map[netip.Addr]bool)
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil {
			local[prefix.Addr().Unmap()] = true
		}
	}
	collector.talkers = newTalkerAccounting(network, settings.ProcDirectory, local)
	return collector, nil
}

func waitUntil(ctx context.Context, end time.Time) error {
	timer := time.NewTimer(time.Until(end))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (collector *Collector) Sample(ctx context.Context, start, end time.Time) (Minute, error) {
	if !end.After(start) {
		return Minute{}, errors.New("reporting interval end must follow its start")
	}
	minute := Minute{Start: start, End: end}
	before := collector.linkCounters()
	interval, _ := time.ParseDuration(collector.settings.ProbeInterval)
	results := make(chan ProbeMinute, len(collector.network.WAN)*8)
	var workers sync.WaitGroup
	for key, wan := range collector.network.WAN {
		id := connectionid.Resolve(collector.network.ConnectionIDs[key].String(), key, wan.Iface)
		for _, family := range []Family{IPv4, IPv6} {
			for _, target := range []Target{Gateway, Cloudflare} {
				for _, mode := range []ProbeMode{Fresh, Persistent} {
					probeKey := ProbeKey{WAN: id, Family: family, Target: target, Mode: mode}
					workers.Go(func() { results <- collector.sampleProbe(ctx, wan, probeKey, start, end, interval) })
				}
			}
		}
	}
	workers.Wait()
	close(results)
	for result := range results {
		minute.Probes = append(minute.Probes, result)
	}
	if err := waitUntil(ctx, end); err != nil {
		return minute, err
	}
	for key, wan := range collector.network.WAN {
		id := connectionid.Resolve(collector.network.ConnectionIDs[key].String(), key, wan.Iface)
		traffic := WANTraffic{WAN: id}
		previous, exists := before[key]
		next, err := netif.ReadLinkStats(collector.log, wan.Iface)
		if exists && err == nil && next.RxBytes >= previous.RxBytes && next.TxBytes >= previous.TxBytes && next.RxPackets >= previous.RxPackets && next.TxPackets >= previous.TxPackets && next.RxDropped >= previous.RxDropped && next.TxDropped >= previous.TxDropped {
			traffic.Downloaded = next.RxBytes - previous.RxBytes
			traffic.Uploaded = next.TxBytes - previous.TxBytes
			traffic.Packets = next.RxPackets - previous.RxPackets + next.TxPackets - previous.TxPackets
			traffic.Drops = next.RxDropped - previous.RxDropped + next.TxDropped - previous.TxDropped
			traffic.Available = true
		}
		minute.Traffic = append(minute.Traffic, traffic)
	}
	var err error
	minute.Host, err = collector.host.sample()
	if err != nil {
		return minute, err
	}
	minute.Talkers, minute.Attribution = collector.talkers.sample()
	minute.UDPSendFailures, minute.DNSReceiveDrops, err = collector.udpDeltas()
	return minute, err
}

func (collector *Collector) linkCounters() map[string]netif.LinkStats {
	result := make(map[string]netif.LinkStats)
	for key, wan := range collector.network.WAN {
		stats, err := netif.ReadLinkStats(collector.log, wan.Iface)
		if err != nil {
			collector.log.Warn("WAN counter sample unavailable", "wan", key, "error", err)
			continue
		}
		result[key] = stats
	}
	return result
}

func (collector *Collector) source(wan config.IfMgrWANEntry, family Family) (netip.Addr, netip.Addr, error) {
	link, err := netlink.LinkByName(wan.Iface)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	addressFamily := netlink.FAMILY_V4
	if family == IPv6 {
		addressFamily = netlink.FAMILY_V6
	}
	addresses, err := netlink.AddrList(link, addressFamily)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	var source netip.Addr
	for _, address := range addresses {
		parsed, ok := netip.AddrFromSlice(address.IP)
		if ok && parsed.IsGlobalUnicast() {
			source = parsed.Unmap()
			break
		}
	}
	routes, err := netlink.RouteListFiltered(addressFamily, &netlink.Route{Table: wan.TableID, LinkIndex: link.Attrs().Index}, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_OIF)
	if err != nil {
		return source, netip.Addr{}, err
	}
	var gateway netip.Addr
	for _, route := range routes {
		if route.Dst != nil {
			ones, _ := route.Dst.Mask.Size()
			if ones != 0 {
				continue
			}
		}
		gateway, _ = netip.AddrFromSlice(route.Gw)
		if gateway.IsValid() {
			gateway = gateway.Unmap()
			if gateway.IsLinkLocalUnicast() {
				gateway = gateway.WithZone(wan.Iface)
			}
			break
		}
	}
	return source, gateway, nil
}

func (collector *Collector) sampleProbe(ctx context.Context, wan config.IfMgrWANEntry, key ProbeKey, start, end time.Time, cadence time.Duration) ProbeMinute {
	result := ProbeMinute{Key: key}
	if (key.Family == IPv4 && wan.TranslationV4 == nil) || (key.Family == IPv6 && wan.TranslationV6 == nil) {
		return result
	}
	result.Available = true
	timeout, _ := time.ParseDuration(collector.settings.ProbeTimeout)
	for scheduled := start; scheduled.Before(end); scheduled = scheduled.Add(cadence) {
		if err := waitUntil(ctx, scheduled); err != nil {
			return result
		}
		if !time.Now().Before(end) {
			break
		}
		result.Sent++
		source, target, err := collector.source(wan, key.Family)
		if !source.IsValid() || err != nil {
			result.Lost++
			continue
		}
		probeContext, cancel := context.WithDeadline(ctx, end)
		if key.Target == Cloudflare {
			family, server := "inet", collector.settings.DNSv4
			if key.Family == IPv6 {
				family, server = "inet6", collector.settings.DNSv6
			}
			resolved, resolveError := netif.DNSProbe(probeContext, netif.DNSProbeSpec{Interface: wan.Iface, Family: family, Source: source, Server: server, Name: collector.settings.CloudflareHost, Timeout: timeout})
			if resolveError != nil || len(resolved.Addresses) == 0 {
				result.DNSFailures++
				result.Lost++
				cancel()
				continue
			}
			target = resolved.Addresses[0]
		}
		if !target.IsValid() {
			result.Lost++
			cancel()
			continue
		}
		session, sessionError := collector.echo(probeContext, key, wan.Iface, source, target)
		if sessionError != nil {
			result.Lost++
			cancel()
			continue
		}
		reply, probeError := session.Probe(probeContext, timeout)
		if key.Mode == Fresh {
			_ = session.Close()
		}
		cancel()
		if probeError != nil {
			result.Lost++
			continue
		}
		result.Replies++
		result.LatencySum += reply.RoundTripTime
		if result.LatencyMin == 0 || reply.RoundTripTime < result.LatencyMin {
			result.LatencyMin = reply.RoundTripTime
		}
		if reply.RoundTripTime > result.LatencyMax {
			result.LatencyMax = reply.RoundTripTime
		}
	}
	return result
}

func (collector *Collector) echo(ctx context.Context, key ProbeKey, iface string, source, target netip.Addr) (*netif.EchoSession, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	renewal, _ := time.ParseDuration(collector.settings.PersistentRenewal)
	previous, exists := collector.persistent[key]
	if key.Mode == Persistent && exists {
		if previous.source == source && previous.target == target && time.Since(previous.opened) < renewal {
			return previous.session, nil
		}
		_ = previous.session.Close()
		delete(collector.persistent, key)
	}
	collector.identifier++
	session, err := netif.OpenEchoSession(ctx, iface, source, target, collector.identifier)
	if err != nil {
		return nil, err
	}
	if key.Mode == Persistent {
		collector.persistent[key] = persistentEcho{session: session, source: source, target: target, opened: time.Now()}
	}
	return session, nil
}

func (collector *Collector) udpDeltas() (uint64, uint64, error) {
	data, err := os.ReadFile(filepath.Join(collector.settings.ProcDirectory, "net/snmp"))
	if err != nil {
		return 0, 0, fmt.Errorf("read UDP counters: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	next := make(map[string]uint64)
	for index, line := range lines {
		if !strings.HasPrefix(line, "Udp:") || index+1 >= len(lines) {
			continue
		}
		headers, values := strings.Fields(line), strings.Fields(lines[index+1])
		if len(headers) != len(values) {
			continue
		}
		for column, name := range headers {
			if name != "SndbufErrors" && name != "RcvbufErrors" {
				continue
			}
			value, err := strconv.ParseUint(values[column], 10, 64)
			if err != nil {
				return 0, 0, err
			}
			next[name] = value
		}
		break
	}
	delta := func(key string) uint64 {
		old, exists := collector.udp[key]
		if !exists || next[key] < old {
			return 0
		}
		return next[key] - old
	}
	sent, received := delta("SndbufErrors"), delta("RcvbufErrors")
	collector.udp = next
	return sent, received, nil
}

func (collector *Collector) Close() error {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	var failures []error
	for key, session := range collector.persistent {
		failures = append(failures, session.session.Close())
		delete(collector.persistent, key)
	}
	failures = append(failures, collector.talkers.close())
	return errors.Join(failures...)
}
