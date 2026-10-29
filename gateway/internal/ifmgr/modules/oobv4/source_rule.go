package oobv4

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const sourceRuleProtocol = 188

func (m *Module) reconcileSourceRule(ctx context.Context, address net.IP) error {
	if m.cfg.SourceRulePriority == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("oobv4: reconcile source rule: %w", err)
	}
	rules, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		m.Log.WarnContext(ctx, "oobv4: source rule listing failed", "err", err)
		return fmt.Errorf("oobv4: list source rules: %w", err)
	}
	desired, err := m.ensureSourceRule(ctx, address, rules)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !m.ownsSourceRule(rule) || desired != nil && rule.Src.IP.Equal(desired.Src.IP) {
			continue
		}
		if err := netlink.RuleDel(&rule); err != nil && !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ESRCH) {
			m.Log.WarnContext(ctx, "oobv4: source rule deletion failed", "err", err)
			return fmt.Errorf("oobv4: delete source rule: %w", err)
		}
	}
	return nil
}

func (m *Module) ensureSourceRule(ctx context.Context, address net.IP, rules []netlink.Rule) (*netlink.Rule, error) {
	if address == nil {
		return nil, nil
	}
	ip := address.To4()
	if ip == nil {
		return nil, fmt.Errorf("oobv4: lease source is not IPv4")
	}
	desired := netlink.NewRule()
	desired.Family = unix.AF_INET
	desired.Priority = m.cfg.SourceRulePriority
	desired.Table = m.cfg.OOBTableID
	desired.Protocol = sourceRuleProtocol
	desired.Src = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
	for _, rule := range rules {
		if m.ownsSourceRule(rule) && rule.Src.IP.Equal(ip) {
			return desired, nil
		}
	}
	if err := netlink.RuleAdd(desired); err != nil {
		m.Log.WarnContext(ctx, "oobv4: source rule add failed", "err", err)
		return nil, fmt.Errorf("oobv4: add source rule: %w", err)
	}
	return desired, nil
}

func (m *Module) ownsSourceRule(rule netlink.Rule) bool {
	if rule.Priority != m.cfg.SourceRulePriority || rule.Table != m.cfg.OOBTableID || rule.Protocol != sourceRuleProtocol || rule.Src == nil {
		return false
	}
	bits, size := rule.Src.Mask.Size()
	return bits == 32 && size == 32 && rule.UIDRange == nil && rule.Mark == 0 && rule.Mask == nil &&
		rule.IifName == "" && rule.OifName == "" && rule.Dst == nil && rule.IPProto == 0 &&
		rule.Sport == nil && rule.Dport == nil && !rule.Invert && rule.Tos == 0 && rule.TunID == 0 &&
		rule.Goto == -1 && rule.Flow == -1 && rule.SuppressIfgroup == -1 && rule.SuppressPrefixlen == -1 && (rule.Type == 0 || rule.Type == unix.RTN_UNICAST)
}
