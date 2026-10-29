package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// DesiredRule describes one ip rule entry the daemon wants to keep present.
// Matches what `ip [-6] rule add ...` accepts. Only the fields the daemon
// actively manages are modeled; foreign rules are left alone.
type DesiredRule struct {
	Family   string // "inet" or "inet6"; empty defaults to "inet6"
	Priority int    // numeric priority (0..32766)
	From     string // source selector (CIDR or single addr); empty == "all"
	Mark     uint32 // fwmark selector; zero means no fwmark clause
	IifName  string // input interface selector; empty means no iif clause
	UIDRange string // "lo-hi" or "u-u"; empty means no uidrange clause
	Table    string // table name (kept for log readability; not authoritative)
	TableID  int    // numeric routing table ID; required (was Table string)
}

// CurrentRule is one rule observed via netlink.RuleList. Mirrors the fields
// the daemon manages so equality checks are unambiguous.
type CurrentRule struct {
	Priority int
	From     string
	Mark     uint32
	IifName  string
	UIDRange string
	Table    string // historical name, parsed reverse-mapped from TableID where possible
	TableID  int
}

// ReconcileRules ensures every rule in desired is present, and removes
// daemon-owned rules (matching priority+family) that do not match desired.
// Foreign rules at unrelated priorities are preserved.
//
// Strategy:
//   - For each family (inet/inet6) seen in desired, list current rules.
//   - For each desired rule: if an exact match exists, do nothing.
//   - If a rule exists at the same priority but doesn't match: del + add.
//   - If no rule at that priority: add.
//
// All actions log at DEBUG with full diff context.
func ReconcileRules(
	ctx context.Context, log *slog.Logger,
	desired []DesiredRule,
) error {
	log = log.With("component", "rules")
	log.DebugContext(ctx, "rules: reconcile entry", "desired_count", len(desired))

	// Group desired by family so we list each family at most once.
	byFamily := map[string][]DesiredRule{}
	for _, r := range desired {
		fam := r.Family
		if fam == "" {
			fam = "inet6"
		}
		byFamily[fam] = append(byFamily[fam], r)
	}

	for fam, want := range byFamily {
		flog := log.With("family", fam)
		flog.DebugContext(ctx, "rules: starting family reconcile", "want_count", len(want))

		current, err := listRulesNetlink(flog, fam)
		if err != nil {
			flog.WarnContext(ctx, "rules: listRulesNetlink failed", "err", err)
			return fmt.Errorf("list %s rules: %w", fam, err)
		}
		flog.DebugContext(ctx, "rules: current snapshot",
			"count", len(current), "rules", current)

		byPrio := map[int]CurrentRule{}
		for _, c := range current {
			byPrio[c.Priority] = c
		}

		for _, w := range want {
			cur, exists := byPrio[w.Priority]
			matches := exists && rulesMatch(cur, w)
			if matches {
				flog.DebugContext(ctx, "rules: already present",
					"priority", w.Priority, "table_id", w.TableID)
				continue
			}
			if exists {
				flog.DebugContext(ctx, "rules: replacing rule at priority",
					"priority", w.Priority, "old", cur, "new", w)
				if err := delRuleNetlink(ctx, flog, fam, cur); err != nil {
					flog.WarnContext(ctx, "rules: delRuleNetlink failed",
						"priority", w.Priority, "err", err)
					return fmt.Errorf("del rule prio=%d: %w", w.Priority, err)
				}
			} else {
				flog.DebugContext(ctx, "rules: adding new rule",
					"priority", w.Priority,
					"from", w.From, "uidrange", w.UIDRange, "table_id", w.TableID)
			}
			if err := addRuleNetlink(ctx, flog, fam, w); err != nil {
				flog.WarnContext(ctx, "rules: addRuleNetlink failed",
					"priority", w.Priority, "err", err)
				return fmt.Errorf("add rule prio=%d: %w", w.Priority, err)
			}
		}
	}
	log.DebugContext(ctx, "rules: reconcile complete")
	return nil
}

// RemoveRuleAtPriority deletes any rule(s) at the given (family, priority).
// No-op if no rule exists at that slot. Used by modules that own a single
// (family, priority) slot and need to vacate it (e.g. when the source
// address that justified the rule disappears). Foreign rules at unrelated
// priorities are not touched.
func RemoveRuleAtPriority(
	ctx context.Context, log *slog.Logger, family string, priority int,
) error {
	log = log.With("component", "rules", "op", "remove-at-priority",
		"family", family, "priority", priority)
	current, err := listRulesNetlink(log, family)
	if err != nil {
		log.WarnContext(ctx, "rules: listRulesNetlink failed", "err", err)
		return fmt.Errorf("list %s rules: %w", family, err)
	}
	removed := 0
	for _, c := range current {
		if c.Priority != priority {
			continue
		}
		log.DebugContext(ctx, "rules: removing rule at priority", "rule", c)
		if err := delRuleNetlink(ctx, log, family, c); err != nil {
			log.WarnContext(ctx, "rules: delRuleNetlink failed", "err", err)
			return fmt.Errorf("del rule prio=%d: %w", priority, err)
		}
		removed++
	}
	log.DebugContext(ctx, "rules: remove-at-priority done", "removed", removed)
	return nil
}

// rulesMatch returns true if a current rule equals a desired rule on the
// dimensions the daemon manages. Compares TableID (authoritative) and the
// optional From / Mark / IifName / UIDRange selectors.
func rulesMatch(c CurrentRule, w DesiredRule) bool {
	if c.Priority != w.Priority {
		return false
	}
	if c.TableID != w.TableID {
		return false
	}
	if c.From != w.From {
		return false
	}
	if c.Mark != w.Mark {
		return false
	}
	if c.IifName != w.IifName {
		return false
	}
	if c.UIDRange != w.UIDRange {
		return false
	}
	return true
}

// listRulesNetlink returns parsed rules for one family via netlink.RuleList.
func listRulesNetlink(log *slog.Logger, family string) ([]CurrentRule, error) {
	famConst := familyToNetlink(family)

	startTime := realClock{}.Now()
	rules, err := netlink.RuleList(famConst)
	dur := realClock{}.Now().Sub(startTime)
	log.Debug(
		"rules: RuleList",
		"family", family,
		"count", len(rules),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.Warn("rules: RuleList failed", "family", family, "err", err)
		return nil, fmt.Errorf("RuleList(%s): %w", family, err)
	}

	out := make([]CurrentRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, ruleToCurrent(r))
	}
	return out, nil
}

// ruleToCurrent converts a netlink.Rule into our CurrentRule, normalising
// the implicit "from all" to an empty From string so equality with
// DesiredRule (which uses "" for no selector) works without special-casing.
func ruleToCurrent(r netlink.Rule) CurrentRule {
	c := CurrentRule{
		Priority: r.Priority,
		From:     "",
		Mark:     r.Mark,
		IifName:  r.IifName,
		UIDRange: "",
		Table:    "",
		TableID:  r.Table,
	}
	if r.Src != nil && !isAllAddr(r.Src) {
		c.From = ipNetToString(r.Src)
	}
	if r.UIDRange != nil {
		// netlink stores Start/End as uint32 even though the original CLI
		// uses signed-looking strings. Re-emit in "lo-hi" form so it round-
		// trips with DesiredRule.UIDRange.
		c.UIDRange = strconv.FormatUint(uint64(r.UIDRange.Start), 10) +
			"-" + strconv.FormatUint(uint64(r.UIDRange.End), 10)
	}
	return c
}

// isAllAddr reports whether an IPNet is the wildcard ("from all").
func isAllAddr(n *net.IPNet) bool {
	if n == nil {
		return true
	}
	ones, _ := n.Mask.Size()
	if ones != 0 {
		return false
	}
	return n.IP.Equal(net.IPv4zero) || n.IP.Equal(net.IPv6unspecified)
}

// ipNetToString renders an IPNet as a single-address selector when the mask
// is /32 (v4) or /128 (v6), or as a CIDR otherwise. Matches CLI rendering.
func ipNetToString(n *net.IPNet) string {
	ones, bits := n.Mask.Size()
	if (bits == 32 && ones == 32) || (bits == 128 && ones == 128) {
		return n.IP.String()
	}
	return n.String()
}

// addRuleNetlink installs one rule via netlink.RuleAdd. Returns nil on
// EEXIST (idempotent add).
func addRuleNetlink(
	ctx context.Context, log *slog.Logger, family string, w DesiredRule,
) error {
	r, err := buildNetlinkRule(family, w)
	if err != nil {
		return err
	}
	startTime := realClock{}.Now()
	err = netlink.RuleAdd(r)
	dur := realClock{}.Now().Sub(startTime)
	log.DebugContext(
		ctx, "rules: RuleAdd",
		"family", family, "priority", w.Priority, "table_id", w.TableID,
		"from", w.From, "uidrange", w.UIDRange,
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil && errors.Is(err, syscall.EEXIST) {
		log.DebugContext(ctx, "rules: RuleAdd EEXIST (already present)")
		return nil
	}
	if err != nil {
		log.WarnContext(ctx, "rules: RuleAdd failed",
			"family", family, "priority", w.Priority, "err", err)
		return fmt.Errorf("RuleAdd(prio=%d): %w", w.Priority, err)
	}
	return nil
}

// delRuleNetlink removes the rule at the given (family, priority, table)
// triple. ENOENT is swallowed.
func delRuleNetlink(
	ctx context.Context, log *slog.Logger, family string, c CurrentRule,
) error {
	r, err := buildNetlinkRule(family, DesiredRule{
		Family:   family,
		Priority: c.Priority,
		From:     c.From,
		Mark:     c.Mark,
		IifName:  c.IifName,
		UIDRange: c.UIDRange,
		Table:    c.Table,
		TableID:  c.TableID,
	})
	if err != nil {
		return err
	}
	startTime := realClock{}.Now()
	err = netlink.RuleDel(r)
	dur := realClock{}.Now().Sub(startTime)
	log.DebugContext(
		ctx, "rules: RuleDel",
		"family", family, "priority", c.Priority, "table_id", c.TableID,
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH)) {
		log.DebugContext(ctx, "rules: RuleDel ENOENT (already absent)")
		return nil
	}
	if err != nil {
		log.WarnContext(ctx, "rules: RuleDel failed",
			"family", family, "priority", c.Priority, "err", err)
		return fmt.Errorf("RuleDel(prio=%d): %w", c.Priority, err)
	}
	return nil
}

// buildNetlinkRule constructs the netlink.Rule struct from our DesiredRule.
// Centralised so both add and del sides build identical structs (the kernel
// matches rules for deletion by all selector fields, not just priority).
func buildNetlinkRule(family string, w DesiredRule) (*netlink.Rule, error) {
	r := netlink.NewRule()
	r.Family = familyToNetlink(family)
	r.Priority = w.Priority
	r.Table = w.TableID
	r.Mark = w.Mark
	r.IifName = w.IifName

	if w.From != "" {
		ipnet, err := parseSelector(family, w.From)
		if err != nil {
			return nil, err
		}
		r.Src = ipnet
	}

	if w.UIDRange != "" {
		start, end, err := parseUIDRangeUint32(w.UIDRange)
		if err != nil {
			return nil, err
		}
		r.UIDRange = netlink.NewRuleUIDRange(start, end)
	}

	return r, nil
}

// parseSelector accepts either a bare address ("1.2.3.4" or "::1") or a
// CIDR, and returns a [net.IPNet] sized for the family.
func parseSelector(family, s string) (*net.IPNet, error) {
	if strings.Contains(s, "/") {
		_, ipnet, err := net.ParseCIDR(s)
		if err != nil {
			slog.Warn("rules: ParseCIDR failed", "selector", s, "err", err)
			return nil, fmt.Errorf("ParseCIDR(%q): %w", s, err)
		}
		return ipnet, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("not an IP: %q", s)
	}
	bits := 128
	if family == "inet" {
		bits = 32
		ip = ip.To4()
		if ip == nil {
			return nil, fmt.Errorf("not an IPv4 address: %q", s)
		}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

func parseUIDRangeUint32(s string) (uint32, uint32, error) {
	parts := strings.SplitN(s, "-", 2)
	lo, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		slog.Warn("rules: ParseUint failed", "uidrange", s, "part", parts[0], "err", err)
		return 0, 0, fmt.Errorf("ParseUint(%q): %w", parts[0], err)
	}
	if len(parts) == 1 {
		return uint32(lo), uint32(lo), nil
	}
	hi, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		slog.Warn("rules: ParseUint failed", "uidrange", s, "part", parts[1], "err", err)
		return 0, 0, fmt.Errorf("ParseUint(%q): %w", parts[1], err)
	}
	return uint32(lo), uint32(hi), nil
}

// af_unspec_unused is here to silence unused-import warnings on unix when
// compiled with build tags that strip earlier references.
var _ = unix.AF_UNSPEC
