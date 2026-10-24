package reporting

import (
	"errors"
	"fmt"
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

type flowCount struct{ uploaded, downloaded uint64 }
type talkerKey struct {
	wan                 connectionid.ID
	client, destination netip.Addr
}

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
	accounting := &talkerAccounting{marks: make(map[uint32]connectionid.ID), previous: make(map[string]flowCount), destroyed: make(map[string]struct{}), current: make(map[talkerKey]flowCount), local: local, gaps: make(map[connectionid.ID]string)}
	for key, wan := range network.WAN {
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
	accounting.failure = accounting.open(proc)
	return accounting
}

func (accounting *talkerAccounting) open(proc string) error {
	setting, err := os.ReadFile(filepath.Join(proc, "sys/net/netfilter/nf_conntrack_acct"))
	if err != nil {
		return fmt.Errorf("read conntrack accounting setting: %w", err)
	}
	if strings.TrimSpace(string(setting)) != "1" {
		return errors.New("kernel conntrack byte accounting is disabled")
	}
	events, err := netfilter.Dial(nil)
	if err != nil {
		return err
	}
	if err := events.SetReadBuffer(1048576); err != nil {
		_ = events.Close()
		return err
	}
	if err := events.JoinGroups([]netfilter.NetlinkGroup{netfilter.GroupCTNew, netfilter.GroupCTDestroy}); err != nil {
		_ = events.Close()
		return err
	}
	dumps, err := conntrack.Dial(nil)
	if err != nil {
		_ = events.Close()
		return err
	}
	flows, err := dumps.Dump(nil)
	if err != nil {
		_ = events.Close()
		_ = dumps.Close()
		return err
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
	go accounting.readEvents()
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
				accounting.failure = fmt.Errorf("conntrack event accounting interrupted: %w", err)
			}
			accounting.mu.Unlock()
			return
		}
		for _, message := range messages {
			var event conntrack.Event
			if err := event.Unmarshal(message); err != nil {
				accounting.failure = err
				continue
			}
			if event.Flow == nil {
				continue
			}
			if event.Type == conntrack.EventNew {
				key := flowKey(*event.Flow)
				if _, exists := accounting.previous[key]; !exists {
					accounting.previous[key] = flowCount{}
				}
			}
			if event.Type == conntrack.EventDestroy {
				accounting.observe(*event.Flow, false, true)
			}
		}
		accounting.mu.Unlock()
	}
}

func (accounting *talkerAccounting) client(flow conntrack.Flow) (netip.Addr, netip.Addr, bool) {
	source, destination := flow.TupleOrig.IP.SourceAddress.Unmap(), flow.TupleOrig.IP.DestinationAddress.Unmap()
	if accounting.local[source] || accounting.local[destination] {
		return netip.Addr{}, netip.Addr{}, false
	}
	for _, prefix := range accounting.prefixes {
		if prefix.Contains(source) {
			return source, destination, true
		}
		if prefix.Contains(destination) {
			return destination, source, false
		}
		if prefix.Contains(flow.TupleReply.IP.SourceAddress.Unmap()) {
			return flow.TupleReply.IP.SourceAddress.Unmap(), source, false
		}
	}
	return netip.Addr{}, netip.Addr{}, false
}

func (accounting *talkerAccounting) observe(flow conntrack.Flow, baseline, destroyed bool) {
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
		if err != nil {
			accounting.failure = err
		} else if len(flows) > flowLimit {
			accounting.failure = errors.New("conntrack table exceeds reporting limit")
		} else {
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
			usage = &TalkerUsage{WAN: key.wan, Client: key.client, Label: key.client.String()}
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
	return errors.Join(failures...)
}
