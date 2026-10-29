//go:build linux && netns

package offload

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/netif"
)

type forwardingNeighborLog struct {
	Message    string                             `json:"msg"`
	Generation uint64                             `json:"generation"`
	Count      int                                `json:"mutation_count"`
	Mutations  map[string]forwardingNeighborEntry `json:"mutations"`
	readError  error
}

type forwardingNeighborEntry struct {
	Type        uint16 `json:"message_type"`
	Invalidates bool   `json:"invalidates"`
	Object      struct {
		Family      int    `json:"family"`
		Index       int    `json:"interface_index"`
		State       int    `json:"state"`
		Destination string `json:"destination"`
		LinkAddress string `json:"link_address"`
	} `json:"object"`
}

func TestForwardingEventsPreserveEquivalentNeighbors(t *testing.T) {
	for _, family := range []struct {
		name  string
		value int
	}{{name: "ipv4", value: unix.AF_INET}, {name: "ipv6", value: unix.AF_INET6}} {
		t.Run(family.name, func(t *testing.T) {
			if !forwardingEventsNamespace(t) {
				return
			}
			checkEquivalentForwardingNeighbors(t, family.value)
		})
	}
}

func checkEquivalentForwardingNeighbors(t *testing.T, family int) {
	t.Helper()
	link := forwardingEventsLink(t)
	selector := forwardingNeighborPath(t, family, link)
	ctx, cancel := context.WithTimeout(t.Context(), forwardingEventsTimeout)
	defer cancel()
	records := forwardingNeighborLogs(t, ctx)
	source := forwardingEventsSource(t, ctx)
	neighbor := netlink.Neigh{
		LinkIndex: link.Attrs().Index, Family: family,
		IP: net.IP(selector.Destination.Addr().AsSlice()), HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 2}, State: unix.NUD_PERMANENT,
	}
	previous := source.Generation()
	batch, entry := changeForwardingNeighbor(t, ctx, records, neighbor, unix.RTM_NEWNEIGH)
	if !entry.Invalidates || batch.Generation <= previous {
		t.Fatal("first neighbor observation did not invalidate forwarding evidence")
	}
	baseline := batch.Generation
	for _, state := range []int{unix.NUD_NOARP, unix.NUD_REACHABLE, unix.NUD_STALE, unix.NUD_DELAY, unix.NUD_PROBE, unix.NUD_PERMANENT} {
		neighbor.State = state
		batch, entry = changeForwardingNeighbor(t, ctx, records, neighbor, unix.RTM_NEWNEIGH)
		if entry.Invalidates || batch.Generation != baseline || source.Generation() != baseline {
			t.Fatalf("equivalent state %d invalidated evidence: batch=%+v entry=%+v generation=%d", state, batch, entry, source.Generation())
		}
		assertForwardingNeighborPath(t, ctx, selector, neighbor, true)
	}
	neighbor.HardwareAddr = net.HardwareAddr{2, 0, 0, 0, 0, 3}
	batch, entry = changeForwardingNeighbor(t, ctx, records, neighbor, unix.RTM_NEWNEIGH)
	if !entry.Invalidates || batch.Generation <= baseline {
		t.Fatalf("changed MAC did not invalidate its exact neighbor notification: %+v %+v", batch, entry)
	}
	baseline = batch.Generation
	neighbor.State = unix.NUD_NOARP
	batch, entry = changeForwardingNeighbor(t, ctx, records, neighbor, unix.RTM_NEWNEIGH)
	if entry.Invalidates || batch.Generation != baseline || source.Generation() != baseline {
		t.Fatalf("equivalent update changed the confirmed MAC-change generation: %+v %+v", batch, entry)
	}
	t.Logf("ordered neighbor notification summary contained %d records", batch.Count)
	assertForwardingNeighborPath(t, ctx, selector, neighbor, true)
	for _, state := range []int{unix.NUD_FAILED, unix.NUD_INCOMPLETE} {
		previous = source.Generation()
		neighbor.State = state
		unusable := neighbor
		unusable.HardwareAddr = nil
		batch, entry = changeForwardingNeighbor(t, ctx, records, unusable, unix.RTM_NEWNEIGH)
		if !entry.Invalidates || batch.Generation <= previous {
			t.Fatalf("unusable neighbor state %d did not invalidate evidence", state)
		}
		assertForwardingNeighborPath(t, ctx, selector, neighbor, false)
		neighbor.State = unix.NUD_PERMANENT
		batch, entry = changeForwardingNeighbor(t, ctx, records, neighbor, unix.RTM_NEWNEIGH)
		if !entry.Invalidates {
			t.Fatal("recovery after unusable state reused a revoked baseline")
		}
	}
	previous = source.Generation()
	batch, entry = changeForwardingNeighbor(t, ctx, records, neighbor, unix.RTM_DELNEIGH)
	if !entry.Invalidates || batch.Generation <= previous {
		t.Fatal("neighbor deletion did not invalidate evidence")
	}
	assertForwardingNeighborPath(t, ctx, selector, neighbor, false)
	batch, entry = changeForwardingNeighbor(t, ctx, records, neighbor, unix.RTM_NEWNEIGH)
	if !entry.Invalidates {
		t.Fatal("neighbor creation after deletion reused a revoked baseline")
	}
}

func forwardingNeighborPath(t *testing.T, family int, link netlink.Link) netif.PacketSelector {
	t.Helper()
	source, destination, prefix := "192.0.2.10", "198.51.100.2", "198.51.100.0/24"
	local := "192.0.2.1/24"
	setting := "/proc/sys/net/ipv4/conf/all/forwarding"
	if family == unix.AF_INET6 {
		source, destination, prefix = "2001:db8:1::10", "2001:db8:2::2", "2001:db8:2::/64"
		local = "2001:db8:1::1/64"
		setting = "/proc/sys/net/ipv6/conf/all/forwarding"
	}
	if err := os.WriteFile(setting, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(local)
	if err != nil {
		t.Fatal(err)
	}
	if family == unix.AF_INET6 {
		address.Flags = unix.IFA_F_NODAD
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	_, network, err := net.ParseCIDR(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		Family: family, LinkIndex: link.Attrs().Index,
		Dst: network, Scope: netlink.SCOPE_LINK, Table: unix.RT_TABLE_MAIN,
	}); err != nil {
		t.Fatal(err)
	}
	return netif.PacketSelector{
		Source:      netip.AddrPortFrom(netip.MustParseAddr(source), 45001),
		Destination: netip.AddrPortFrom(netip.MustParseAddr(destination), 5201),
		Protocol:    unix.IPPROTO_TCP, InputInterface: link.Attrs().Index, Mark: 0, TrafficClass: 0, FlowLabel: 0,
	}
}

func assertForwardingNeighborPath(t *testing.T, ctx context.Context, selector netif.PacketSelector, neighbor netlink.Neigh, wantUsable bool) {
	t.Helper()
	path, usable, err := netif.ForwardPathLookup(ctx, slog.Default(), selector)
	if err != nil || usable != wantUsable {
		t.Fatalf("kernel forwarding path for neighbor state %d: usable=%v want=%v path=%+v error=%v", neighbor.State, usable, wantUsable, path, err)
	}
	if usable && (path.Link.Attrs().Index != neighbor.LinkIndex || path.Neighbor.HardwareAddr.String() != neighbor.HardwareAddr.String()) {
		t.Fatalf("kernel forwarding identity differs from observed neighbor: %+v", path)
	}
}

func changeForwardingNeighbor(t *testing.T, ctx context.Context, records <-chan forwardingNeighborLog, neighbor netlink.Neigh, kind uint16) (forwardingNeighborLog, forwardingNeighborEntry) {
	t.Helper()
	var err error
	if kind == unix.RTM_DELNEIGH {
		err = netlink.NeighDel(&neighbor)
	} else {
		err = netlink.NeighSet(&neighbor)
	}
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case batch, open := <-records:
			if !open {
				t.Fatal("real forwarding JSON stream closed before the target notification")
			}
			if batch.readError != nil {
				t.Fatalf("read real forwarding JSON stream: %v", batch.readError)
			}
			for _, entry := range batch.Mutations {
				if entry.Type == kind && entry.Object.Family == neighbor.Family && entry.Object.Index == neighbor.LinkIndex &&
					entry.Object.Destination == neighbor.IP.String() && (kind == unix.RTM_DELNEIGH || entry.Object.State == neighbor.State &&
					(len(neighbor.HardwareAddr) == 0 || entry.Object.LinkAddress == neighbor.HardwareAddr.String())) {
					return batch, entry
				}
			}
		case <-ctx.Done():
			t.Fatalf("target kernel notification did not complete processing: family=%d interface=%d address=%s state=%d type=%d: %v",
				neighbor.Family, neighbor.LinkIndex, neighbor.IP, neighbor.State, kind, ctx.Err())
		}
	}
}

func forwardingNeighborLogs(t *testing.T, ctx context.Context) <-chan forwardingNeighborLog {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	log, closer := logging.New(logging.Config{BuildVersion: "", Handlers: []slog.Handler{
		slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo, AddSource: false, ReplaceAttr: nil}),
	}})
	previous := slog.Default()
	slog.SetDefault(log)
	records := make(chan forwardingNeighborLog, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(records)
		decoder := json.NewDecoder(reader)
		for {
			var record forwardingNeighborLog
			if err := decoder.Decode(&record); err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					select {
					case records <- forwardingNeighborLog{readError: err}:
					case <-ctx.Done():
					}
				}
				return
			}
			if record.Message != "kernel forwarding notifications processed" {
				continue
			}
			select {
			case records <- record:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		if err := closer.Close(); err != nil {
			t.Error(err)
		}
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
		<-done
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	return records
}
