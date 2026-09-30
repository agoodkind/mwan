package npt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/netif"
)

func edgeLinkCurrent(record ifmgr.NPTEdgeRecord) bool {
	link, err := netlink.LinkByIndex(record.InterfaceIndex)
	return err == nil && link.Attrs().Name == record.Interface && netif.LinkMatchesIdentity(link, record.InterfaceIndex, record.LinkIdentity)
}

func (m *Module) markDesiredEdges(desired map[ifmgr.NPTEdgeRecord]bool, wan WAN, cidr string) {
	if m.Env.NPTAddresses == nil {
		return
	}
	for _, record := range m.Env.NPTAddresses.Recorded() {
		if record.ConnectionID == wan.ID && record.Interface == wan.Iface && (cidr == "" || record.Prefix.String() == cidr) && edgeLinkCurrent(record) {
			desired[record] = true
		}
	}
}

func (m *Module) journaledEdge(iface string, address netip.Addr) bool {
	if m.Env == nil || m.Env.NPTAddresses == nil {
		return false
	}
	for _, record := range m.Env.NPTAddresses.Recorded() {
		if record.Interface == iface && record.Prefix.Addr() == address {
			return true
		}
	}
	return false
}

func (m *Module) releaseObsoleteEdges(ctx context.Context, log *slog.Logger, desired map[ifmgr.NPTEdgeRecord]bool, ready map[string]bool, policies []bpf.InterfacePolicy) error {
	if m.Env == nil || m.Env.NPTAddresses == nil {
		return nil
	}
	var failures error
	for _, record := range m.Env.NPTAddresses.Recorded() {
		if desired[record] {
			continue
		}
		if err := verifyNFTEdgeUnused(ctx, record); err != nil {
			ready[record.ConnectionID.String()] = false
			failures = errors.Join(failures, err)
			continue
		}
		internal, err := netlink.LinkByName(m.cfg.InternalIface)
		if err != nil {
			ready[record.ConnectionID.String()] = false
			failures = errors.Join(failures, fmt.Errorf("inspect NPT internal link before edge release: %w", err))
			continue
		}
		reader, ok := m.translator.(interface {
			VerifyUnused([]int, netip.Addr, []bpf.InterfacePolicy) error
		})
		if !ok {
			ready[record.ConnectionID.String()] = false
			failures = errors.Join(failures, fmt.Errorf("NPT translator cannot verify edge release"))
			continue
		}
		if err := reader.VerifyUnused([]int{record.InterfaceIndex, internal.Attrs().Index}, record.Prefix.Addr(), policies); err != nil {
			ready[record.ConnectionID.String()] = false
			failures = errors.Join(failures, fmt.Errorf("inspect NPT policies before releasing %s: %w", record.Prefix, err))
			continue
		}
		if err := m.Env.NPTAddresses.Release(ctx, log, []ifmgr.NPTEdgeRecord{record}); err != nil {
			ready[record.ConnectionID.String()] = false
			failures = errors.Join(failures, err)
		} else if m.Env.RequestReconcile != nil {
			m.Env.RequestReconcile("NPT edge journal cleanup completed")
		}
	}
	return failures
}

func verifyNFTEdgeUnused(ctx context.Context, record ifmgr.NPTEdgeRecord) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "NPT edge translation inspection failed", "connection", record.ConnectionID, "err", resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inspect NPT edge translation: %w", err)
	}
	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open NPT edge translation inspection: %w", err)
	}
	table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: natTableName}
	for _, name := range []string{preroutingChain, postroutingChain} {
		rules, err := conn.GetRules(table, &nftables.Chain{Name: name, Table: table})
		if err != nil && isNFTObjectMissing(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect managed NPT chain %s: %w", name, err)
		}
		for _, rule := range rules {
			if rule == nil {
				return fmt.Errorf("managed NPT chain %s contains an unreadable rule", name)
			}
			_, iface, matched := decodeInterfaceMatch(ifaceNameByIndex, rule.Exprs)
			if matched && iface != record.Interface {
				continue
			}
			decoded, ok := decodeRule(ifaceNameByIndex, rule.Exprs)
			if !ok {
				return fmt.Errorf("managed NPT chain %s contains an unrecognized relevant rule", name)
			}
			address := record.Prefix.Addr()
			if decoded.Match.Contains(address) || decoded.ToAddr == address || decoded.ToPfx.Contains(address) {
				return fmt.Errorf("managed NPT chain %s still uses edge %s", name, address)
			}
		}
	}
	return nil
}
