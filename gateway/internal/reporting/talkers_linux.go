package reporting

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"

	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkjson"
)

const flowLimit = 32768

type (
	flowCount struct{ uploaded, downloaded uint64 }
	talkerKey struct {
		wan                 connectionid.ID
		client, destination netip.Addr
	}
)

type talkerAccounting struct {
	mu        sync.Mutex
	dumps     *conntrack.Conn
	events    *netfilter.Conn
	workers   sync.WaitGroup
	marks     map[uint32]connectionid.ID
	previous  map[string]flowCount
	destroyed map[string]struct{}
	current   map[talkerKey]flowCount
	local     map[netip.Addr]bool
	prefixes  []netip.Prefix
	gaps      map[connectionid.ID]string
	failure   error
	closed    bool
}

func newTalkerAccounting(network *networkjson.Config, proc string, local map[netip.Addr]bool) *talkerAccounting {
	accounting := &talkerAccounting{
		mu: sync.Mutex{}, dumps: nil, events: nil, workers: sync.WaitGroup{},
		marks: make(map[uint32]connectionid.ID), previous: make(map[string]flowCount),
		destroyed: make(map[string]struct{}), current: make(map[talkerKey]flowCount),
		local: local, prefixes: nil, gaps: make(map[connectionid.ID]string), failure: nil, closed: false,
	}
	for key, wan := range network.WAN {
		if wan.FwMark < 1 || uint64(wan.FwMark) > 4294967295 {
			accounting.failure = fmt.Errorf("WAN %s requires a valid connection mark for attribution", key)
			continue
		}
		accounting.marks[uint32(wan.FwMark)] = connectionid.Resolve(network.ConnectionIDs[key].String(), key, wan.Iface)
	}
	for _, text := range []string{network.InternalPrefix, network.InternalNetV4} {
		if prefix, err := netip.ParsePrefix(text); err == nil {
			accounting.prefixes = append(accounting.prefixes, prefix.Masked())
		}
	}
	for _, wan := range network.WAN {
		if wan.TranslationV6 != nil && wan.TranslationV6.NPT != nil {
			accounting.prefixes = append(accounting.prefixes, wan.TranslationV6.NPT.InternalPrefix)
		}
	}
	if len(network.WAN) == 0 {
		return accounting
	}
	if accounting.failure == nil {
		accounting.failure = accounting.open(proc)
	}
	return accounting
}

func (accounting *talkerAccounting) open(proc string) (failure error) {
	defer func() {
		if failure != nil {
			slog.Warn("open connection accounting failed", "error", failure)
		}
	}()
	setting, err := os.ReadFile(filepath.Join(proc, "sys/net/netfilter/nf_conntrack_acct"))
	if err != nil {
		return fmt.Errorf("read conntrack accounting setting: %w", err)
	}
	if strings.TrimSpace(string(setting)) != "1" {
		return errors.New("kernel conntrack byte accounting is disabled")
	}
	events, err := netfilter.Dial(nil)
	if err != nil {
		return fmt.Errorf("open connection accounting event socket: %w", err)
	}
	if err := events.SetReadBuffer(1048576); err != nil {
		_ = events.Close()
		return fmt.Errorf("set connection accounting event buffer: %w", err)
	}
	if err := events.JoinGroups([]netfilter.NetlinkGroup{netfilter.GroupCTNew, netfilter.GroupCTDestroy}); err != nil {
		_ = events.Close()
		return fmt.Errorf("subscribe to connection accounting events: %w", err)
	}
	dumps, err := conntrack.Dial(nil)
	if err != nil {
		_ = events.Close()
		return fmt.Errorf("open connection accounting dump socket: %w", err)
	}
	flows, err := dumps.Dump(nil)
	if err != nil {
		_ = events.Close()
		_ = dumps.Close()
		return fmt.Errorf("read initial connection accounting table: %w", err)
	}
	if len(flows) > flowLimit {
		_ = events.Close()
		_ = dumps.Close()
		return errors.New("conntrack table exceeds reporting limit")
	}
	accounting.events, accounting.dumps = events, dumps
	for _, flow := range flows {
		accounting.observe(flow, true, false)
	}
	accounting.workers.Add(1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				accounting.mu.Lock()
				accounting.failure = fmt.Errorf("connection event reader panic: %v", recovered)
				accounting.mu.Unlock()
				slog.Error("connection event reader panic", "error", recovered)
			}
		}()
		accounting.readEvents()
	}()
	return nil
}

func flowKey(flow conntrack.Flow) string {
	return fmt.Sprintf("%d/%d/%d/%d/%s/%s/%d/%d/%d", flow.ID, flow.Zone, flow.TupleOrig.Zone, flow.TupleOrig.Proto.Protocol, flow.TupleOrig.IP.SourceAddress, flow.TupleOrig.IP.DestinationAddress, flow.TupleOrig.Proto.SourcePort, flow.TupleOrig.Proto.DestinationPort, flow.TupleOrig.Proto.ICMPID)
}

func (accounting *talkerAccounting) readEvents() {
	defer accounting.workers.Done()
	for {
		messages, err := accounting.events.Receive()
		accounting.mu.Lock()
		if err != nil {
			if !accounting.closed {
				slog.Warn("receive connection accounting events failed", "error", err)
				accounting.failure = fmt.Errorf("conntrack event accounting interrupted: %w", err)
			}
			accounting.mu.Unlock()
			return
		}
		for _, message := range messages {
			var event conntrack.Event
			if err := event.Unmarshal(message); err != nil {
				slog.Warn("decode connection accounting event failed", "error", err)
				accounting.failure = err
				continue
			}
			accounting.observeEvent(event)
		}
		accounting.mu.Unlock()
	}
}

func (accounting *talkerAccounting) observeEvent(event conntrack.Event) {
	if event.Flow == nil {
		return
	}
	switch event.Type {
	case conntrack.EventNew:
		client, _, _ := accounting.client(*event.Flow)
		if !client.IsValid() {
			return
		}
		if len(accounting.previous) >= flowLimit {
			accounting.failure = errors.New("conntrack reporting state exceeds its limit")
			return
		}
		key := flowKey(*event.Flow)
		if _, exists := accounting.previous[key]; !exists {
			accounting.previous[key] = flowCount{uploaded: 0, downloaded: 0}
		}
	case conntrack.EventDestroy:
		accounting.observe(*event.Flow, false, true)
	case conntrack.EventUnknown, conntrack.EventUpdate, conntrack.EventExpNew, conntrack.EventExpDestroy:
		return
	}
}

func (accounting *talkerAccounting) client(flow conntrack.Flow) (netip.Addr, netip.Addr, bool) {
	source, destination := flow.TupleOrig.IP.SourceAddress.Unmap(), flow.TupleOrig.IP.DestinationAddress.Unmap()
	if accounting.local[source] {
		return netip.Addr{}, netip.Addr{}, false
	}
	for _, prefix := range accounting.prefixes {
		if prefix.Contains(source) {
			return source, destination, true
		}
		if prefix.Contains(destination) && !accounting.local[destination] {
			return destination, source, false
		}
		if prefix.Contains(flow.TupleReply.IP.SourceAddress.Unmap()) {
			return flow.TupleReply.IP.SourceAddress.Unmap(), source, false
		}
	}
	return netip.Addr{}, netip.Addr{}, false
}

func (accounting *talkerAccounting) observe(flow conntrack.Flow, baseline, destroyed bool) {
	if destroyed {
		defer delete(accounting.previous, flowKey(flow))
	}
	wan, matched := accounting.marks[flow.Mark]
	client, destination, outbound := accounting.client(flow)
	if !client.IsValid() {
		return
	}
	if !matched {
		for _, id := range accounting.marks {
			accounting.gaps[id] = "A client connection did not identify its WAN in its connection mark."
		}
		return
	}
	key := flowKey(flow)
	if _, exists := accounting.destroyed[key]; exists {
		return
	}
	next := flowCount{uploaded: flow.CountersOrig.Bytes, downloaded: flow.CountersReply.Bytes}
	if !flow.CountersReply.Direction {
		accounting.gaps[wan] = "A connection does not provide byte-counter attributes."
	}
	previous, exists := accounting.previous[key]
	if !exists && !baseline {
		accounting.gaps[wan] = "A connection did not have a preceding accounting baseline."
		baseline = true
	}
	if next.uploaded < previous.uploaded || next.downloaded < previous.downloaded {
		accounting.gaps[wan] = "Connection byte counters decreased."
		baseline = true
	}
	if (flow.CountersOrig.Packets > 0 && next.uploaded == 0) || (flow.CountersReply.Packets > 0 && next.downloaded == 0) {
		accounting.gaps[wan] = "Connection packet counters increased without byte accounting."
	}
	if uint32(flow.Status)&32768 != 0 {
		accounting.gaps[wan] = "Hardware-offloaded connection counters do not establish complete client attribution."
	}
	if len(accounting.previous) >= flowLimit {
		accounting.failure = errors.New("conntrack reporting state exceeds its limit")
		return
	}
	accounting.previous[key] = next
	if !baseline {
		delta := flowCount{uploaded: next.uploaded - previous.uploaded, downloaded: next.downloaded - previous.downloaded}
		if !outbound {
			delta.uploaded, delta.downloaded = delta.downloaded, delta.uploaded
		}
		identity := talkerKey{wan: wan, client: client, destination: destination}
		total := accounting.current[identity]
		total.uploaded += delta.uploaded
		total.downloaded += delta.downloaded
		accounting.current[identity] = total
	}
	if destroyed {
		delete(accounting.previous, key)
		accounting.destroyed[key] = struct{}{}
	}
}

func (accounting *talkerAccounting) sample() ([]TalkerUsage, []AttributionCoverage) {
	accounting.mu.Lock()
	defer accounting.mu.Unlock()
	if accounting.failure == nil && accounting.dumps != nil {
		flows, err := accounting.dumps.Dump(nil)
		switch {
		case err != nil:
			slog.Warn("read connection accounting table failed", "error", err)
			accounting.failure = err
		case len(flows) > flowLimit:
			accounting.failure = errors.New("conntrack table exceeds reporting limit")
		default:
			for _, flow := range flows {
				accounting.observe(flow, false, false)
			}
		}
	}
	byClient := make(map[talkerKey]*TalkerUsage)
	for key, counts := range accounting.current {
		clientKey := key
		clientKey.destination = netip.Addr{}
		usage := byClient[clientKey]
		if usage == nil {
			usage = &TalkerUsage{WAN: key.wan, Client: key.client, Label: key.client.String(), Downloaded: 0, Uploaded: 0, Destinations: nil}
			byClient[clientKey] = usage
		}
		usage.Downloaded += counts.downloaded
		usage.Uploaded += counts.uploaded
		usage.Destinations = append(usage.Destinations, DestinationUsage{Address: key.destination, Downloaded: counts.downloaded, Uploaded: counts.uploaded})
	}
	result := make([]TalkerUsage, 0, len(byClient))
	for _, usage := range byClient {
		result = append(result, *usage)
	}
	coverage := make([]AttributionCoverage, 0, len(accounting.marks))
	for _, id := range accounting.marks {
		reason := accounting.gaps[id]
		if accounting.failure != nil {
			reason = accounting.failure.Error()
		}
		coverage = append(coverage, AttributionCoverage{WAN: id, Complete: reason == "", Reason: reason})
	}
	accounting.current = make(map[talkerKey]flowCount)
	accounting.gaps = make(map[connectionid.ID]string)
	accounting.destroyed = make(map[string]struct{})
	return result, coverage
}

func (accounting *talkerAccounting) close() error {
	accounting.mu.Lock()
	accounting.closed = true
	accounting.mu.Unlock()
	var failures []error
	if accounting.events != nil {
		failures = append(failures, accounting.events.Close())
	}
	if accounting.dumps != nil {
		failures = append(failures, accounting.dumps.Close())
	}
	accounting.workers.Wait()
	if err := errors.Join(failures...); err != nil {
		slog.Warn("close connection accounting sockets failed", "error", err)
		return fmt.Errorf("close connection accounting sockets: %w", err)
	}
	return nil
}
