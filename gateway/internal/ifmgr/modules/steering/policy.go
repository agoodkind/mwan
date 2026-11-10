package steering

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"

	"github.com/google/nftables/binaryutil"

	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

// CompilePolicy projects the production rule builder for exact kernel inspection.
// Native admission checks this projection within its nftables generation barriers.
func CompilePolicy(configuration Config, snapshot wanstate.Snapshot) (firewall.Ruleset, error) {
	module := newConfiguredModule(configuration)
	if err := module.parse(); err != nil {
		slog.Warn("steering policy configuration invalid", "err", err)
		return firewall.Ruleset{}, fmt.Errorf("prepare steering policy inspection: %w", err)
	}
	health, err := netif.ReadHealthState(module.cfg.HealthStateFile)
	if err != nil {
		slog.Warn("steering policy health unavailable", "err", err)
		return firewall.Ruleset{}, fmt.Errorf("read steering policy health: %w", err)
	}
	return projectRules(module.rulesForSnapshot(health, snapshot)), nil
}

func projectRules(rules []steerRule) firewall.Ruleset {
	table := firewall.Table{Family: "inet", Name: steerTableName}
	prerouting := firewall.Chain{
		Table: table, Name: steerChainName, Type: "filter", Hook: "prerouting",
		Priority: strconv.Itoa(int(steerChainPriority)), Policy: "accept", Rules: nil,
	}
	forward := firewall.Chain{
		Table: table, Name: guardChainName, Type: "filter", Hook: "forward",
		Priority: strconv.Itoa(int(guardChainPriority)), Policy: "accept", Rules: nil,
	}
	for _, rule := range rules {
		projection := firewall.Rule{
			Purpose: "", Scope: "", Action: "", Expression: rule.policyExpression(),
			Interface: rule.IifName, OutputInterface: rule.DropIface, Source: rule.Source,
			Destination: netip.Prefix{}, Mark: nil,
		}
		if rule.DropIface != "" {
			forward.Rules = append(forward.Rules, projection)
		} else {
			prerouting.Rules = append(prerouting.Rules, projection)
		}
	}
	return firewall.Ruleset{Chains: []firewall.Chain{prerouting, forward}, Sets: nil}
}

func (rule steerRule) policyExpression() string {
	family, protocol := "ip6", "ipv6"
	if rule.Source.Addr().Is4() {
		family, protocol = "ip", "ipv4"
	}
	var expression strings.Builder
	if rule.IifName != "" {
		fmt.Fprintf(&expression, "iifname %q ", rule.IifName)
	}
	if rule.DropIface != "" {
		fmt.Fprintf(&expression, "oifname %q meta nfproto %s ", rule.DropIface, protocol)
		for _, mark := range rule.AllowedMarks {
			fmt.Fprintf(&expression, "meta mark != 0x%08x ", mark)
		}
		expression.WriteString("drop")
		return expression.String()
	}
	fmt.Fprintf(&expression, "%s saddr %s meta mark 0x%08x ", family, rule.Source.Masked(), rule.MatchMark)
	if rule.Source.Addr().Is6() {
		fmt.Fprintf(&expression, "meta priority & 0x%08x != 0x%08x ", hairpinPriorityMask, hairpinPriorityTag)
	}
	expression.WriteString("ct state new meta mark set ")
	if len(rule.Assign.Slots) == 0 {
		fmt.Fprintf(&expression, "0x%08x", rule.Assign.Mark)
		return expression.String()
	}
	expression.WriteString(rule.policyGenerator(family))
	expression.WriteString(" map { ")
	slot := uint32(0)
	for _, mark := range rule.Assign.Slots {
		if slot != 0 {
			expression.WriteString(", ")
		}
		fmt.Fprintf(&expression, "%d : 0x%08x", anonymousMapReadbackWord(slot), anonymousMapReadbackWord(mark))
		slot++
	}
	expression.WriteString(" }")
	return expression.String()
}

// Remove this conversion after the pinned library fixes key and data metadata:
// https://github.com/google/nftables/pull/355.
func anonymousMapReadbackWord(value uint32) uint32 {
	return binary.BigEndian.Uint32(binaryutil.NativeEndian.PutUint32(value))
}

func (rule steerRule) policyGenerator(family string) string {
	if rule.Mode == hashModeRandom {
		return fmt.Sprintf("numgen random mod %d", rule.Assign.Modulus)
	}
	key := family + " saddr"
	if rule.Mode == hashModeSourceDestination {
		key += " . " + family + " daddr"
	}
	return fmt.Sprintf("jhash %s mod %d seed 0x%x", key, rule.Assign.Modulus, hashSeed)
}
