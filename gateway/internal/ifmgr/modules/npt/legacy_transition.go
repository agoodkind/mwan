package npt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/pd"
)

// ReadLegacyNPTEdges verifies configured legacy edges against existing managed rules.
func ReadLegacyNPTEdges(ctx context.Context, log *slog.Logger, cfg Config, connections []interfaceintent.Connection) ([]netif.LegacyNPTEdge, error) {
	module, err := New(cfg)
	if err != nil {
		return nil, err
	}
	m, ok := module.(*Module)
	if !ok {
		return nil, fmt.Errorf("legacy NPT constructor returned another module")
	}
	m.src = pd.New(log)
	m.listAddrs = netif.ListAddrs
	if err := m.parse(); err != nil {
		return nil, err
	}
	installed, err := ReadLegacyNPTRules()
	if err != nil {
		return nil, err
	}
	var edges []netif.LegacyNPTEdge
	for _, wan := range cfg.WANs {
		if wan.Translation == nil || wan.Translation.Mode != config.TranslationNPTv6 {
			continue
		}
		var configured *interfaceintent.Connection
		for i := range connections {
			if connections[i].ID == wan.ID && connections[i].Name == wan.Iface {
				configured = &connections[i]
				break
			}
		}
		if configured == nil || configured.Owner != interfaceintent.OwnerNetworkd {
			return nil, fmt.Errorf("NPT producer transition requires unchanged networkd ownership for %s", wan.ID)
		}
		edge, err := m.ReadLegacyNPTEdge(ctx, log, wan, *configured, installed)
		if err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, nil
}

// ReadLegacyNPTRules rejects rules outside the recognized managed NAT forms.
func ReadLegacyNPTRules() ([]natRule, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, netif.NewLegacyNPTError("open legacy NPT rules", err)
	}
	table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: natTableName}
	var installed []natRule
	for _, name := range []string{preroutingChain, postroutingChain} {
		rules, err := conn.GetRules(table, &nftables.Chain{Name: name, Table: table})
		if err != nil {
			return nil, netif.NewLegacyNPTError("read legacy NPT chain "+name, err)
		}
		for _, rule := range rules {
			if rule == nil {
				return nil, fmt.Errorf("unreadable legacy NPT rule")
			}
			decoded, ok := decodeRule(ifaceNameByIndex, rule.Exprs)
			if !ok {
				return nil, fmt.Errorf("unrecognized legacy NPT rule in %s", name)
			}
			installed = append(installed, decoded)
		}
	}
	return installed, nil
}

// ReadLegacyNPTEdge requires the configured rules and ready address on the observed link.
func (m *Module) ReadLegacyNPTEdge(ctx context.Context, log *slog.Logger, wan WAN, configured interfaceintent.Connection, installed []natRule) (netif.LegacyNPTEdge, error) {
	link, err := netif.ObserveLegacyLink(configured)
	if err != nil {
		return netif.LegacyNPTEdge{}, netif.NewLegacyNPTError("read legacy NPT link", err)
	}
	built, present, err := m.buildWANDesired(ctx, log, wan, false)
	if err != nil {
		return netif.LegacyNPTEdge{}, err
	}
	if !present || len(built.ensure) != 1 {
		return netif.LegacyNPTEdge{}, fmt.Errorf("legacy NPT intent has no single edge for %s", wan.ID)
	}
	installedCount := 0
	for _, rule := range installed {
		if rule.Iface == wan.Iface {
			installedCount++
		}
	}
	if installedCount != len(built.rules) {
		return netif.LegacyNPTEdge{}, fmt.Errorf("legacy NPT rules differ from configured producer intent for %s", wan.ID)
	}
	for _, expected := range built.rules {
		if !slices.Contains(installed, expected) {
			return netif.LegacyNPTEdge{}, fmt.Errorf("legacy NPT rule does not match configured producer intent for %s", wan.ID)
		}
	}
	prefix, err := netip.ParsePrefix(built.ensure[0].CIDR)
	if err != nil {
		return netif.LegacyNPTEdge{}, netif.NewLegacyNPTError("parse legacy NPT edge", err)
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		return netif.LegacyNPTEdge{}, netif.NewLegacyNPTError("read legacy NPT addresses", err)
	}
	ready := false
	for _, address := range addresses {
		if address.IPNet.String() == prefix.String() && address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
			ready = true
		}
	}
	if !ready {
		return netif.LegacyNPTEdge{}, fmt.Errorf("legacy NPT edge is not ready for %s", wan.ID)
	}
	intent := struct {
		Connection        interfaceintent.Connection `json:"connection"`
		WAN               WAN                        `json:"wan"`
		InternalInterface string                     `json:"internal_interface"`
		OpnsenseEdge      string                     `json:"opnsense_edge"`
		MwanbrEdge        string                     `json:"mwanbr_edge"`
	}{configured, wan, m.cfg.InternalIface, m.cfg.OpnsenseEdgeV6, m.cfg.MwanbrEdgeV6}
	var intentBytes, ruleBytes bytes.Buffer
	err = gob.NewEncoder(&intentBytes).Encode(intent)
	if err != nil {
		return netif.LegacyNPTEdge{}, netif.NewLegacyNPTError("marshal legacy NPT intent", err)
	}
	err = gob.NewEncoder(&ruleBytes).Encode(built.rules)
	if err != nil {
		return netif.LegacyNPTEdge{}, netif.NewLegacyNPTError("marshal legacy NPT rules", err)
	}
	intentDigest := sha256.Sum256(intentBytes.Bytes())
	ruleDigest := sha256.Sum256(ruleBytes.Bytes())
	return netif.LegacyNPTEdge{Record: netif.NPTEdgeRecord{ConnectionID: wan.ID, Interface: wan.Iface, InterfaceIndex: link.Attrs().Index, LinkIdentity: netif.LinkOwnershipIdentity(link), Prefix: prefix}, IntentSHA256: hex.EncodeToString(intentDigest[:]), RulesSHA256: hex.EncodeToString(ruleDigest[:])}, nil
}
