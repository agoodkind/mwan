package steering

import (
	"cmp"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"goodkind.io/mwan/internal/netif"
)

// hashMode selects how a new connection is assigned to a provider of the active
// tier. The values are the model's hash-mode enumeration, so the string the
// configuration carries is the value this module switches on.
type hashMode string

const (
	// hashModeRandom draws a fresh number per connection.
	hashModeRandom hashMode = "random"
	// hashModeSource derives the assignment from the source address, so one
	// internal host keeps using one provider.
	hashModeSource hashMode = "source"
	// hashModeSourceDestination derives it from source and destination, so one
	// host spreads across providers but each conversation stays put.
	hashModeSourceDestination hashMode = "source-destination"
)

// balancer is how one rule assigns a mark. A single carrying provider takes the
// immediate form; two or more take the generated form.
type balancer struct {
	// Mark is the single mark every matched connection takes. Meaningful only
	// when Slots is empty.
	Mark uint32
	// Modulus is what the generator divides by, always the length of Slots. It
	// is carried rather than derived so the rule builder never has to widen an
	// int at the point it programs the kernel.
	Modulus uint32
	// Slots maps slot index to mark, one entry per weight unit, in ascending
	// mark order. Ascending order is what makes two reconciles with the same
	// provider set produce the same map, which the hash modes depend on.
	Slots []uint32
}

// steerRule assigns a new-flow mark when DropIface is empty. Otherwise it
// guards provider egress.
type steerRule struct {
	// IifName is the incoming link the rule matches, or empty for no match.
	// The IPv6 rules carry none, because a hairpinned reply the router sources
	// from its own edge address does not arrive on the internal link.
	IifName string
	// Source is the source prefix the rule matches. Its family decides which
	// header offsets the rule reads.
	Source netip.Prefix
	// Mode is the hash mode the group is configured with.
	Mode hashMode
	// Assign is how the rule picks the mark.
	Assign balancer
	// MatchMark is the packet mark to replace on a new connection. Zero
	// selects an unmarked connection.
	MatchMark uint32
	// DropIface selects a provider egress guard. The guard accepts every
	// family-eligible mark because a priority-50 fallback rule precedes the
	// fwmark policy rules.
	DropIface    string
	AllowedMarks []uint32
}

// String renders one rule the way the ruleset file wrote its equivalent, for
// error messages and debug logging.
func (r steerRule) String() string {
	if r.DropIface != "" {
		marks := make([]string, 0, len(r.AllowedMarks))
		for _, mark := range r.AllowedMarks {
			marks = append(marks, strconv.FormatUint(uint64(mark), 10))
		}
		return "iif " + r.IifName + " oif " + r.DropIface + " family " + r.Source.String() + " mark outside {" + strings.Join(marks, ",") + "} drop"
	}
	where := "saddr " + r.Source.String()
	if r.IifName != "" {
		where = "iif " + r.IifName + " " + where
	}
	if len(r.Assign.Slots) == 0 {
		return where + " mark set " + strconv.FormatUint(uint64(r.Assign.Mark), 10)
	}
	marks := make([]string, 0, len(r.Assign.Slots))
	for _, mark := range r.Assign.Slots {
		marks = append(marks, strconv.FormatUint(uint64(mark), 10))
	}
	return where + " mark set " + string(r.Mode) +
		" mod " + strconv.FormatUint(uint64(r.Assign.Modulus), 10) +
		" map {" + strings.Join(marks, ",") + "}"
}

// ruleInput is everything the rule builder needs for one pass.
type ruleInput struct {
	InternalIface  string
	InternalNetV4  netip.Prefix
	InternalPrefix netip.Prefix
	OpnsenseEdgeV6 netip.Addr
	Mode           hashMode
	AssignV4       balancer
	AssignV6       balancer
	Members        []Member
	EligibleV4     map[uint32]bool
	EligibleV6     map[uint32]bool
}

// buildRules omits assignments for unavailable families and blocks provider
// egress when a packet has no family-eligible mark.
func buildRules(in ruleInput) []steerRule {
	var rules []steerRule
	if in.AssignV4.Mark != 0 || len(in.AssignV4.Slots) != 0 {
		base := steerRule{IifName: in.InternalIface, Source: in.InternalNetV4, Mode: in.Mode, Assign: in.AssignV4, MatchMark: 0, DropIface: "", AllowedMarks: nil}
		rules = append(rules, base)
		for _, member := range in.Members {
			if !in.EligibleV4[member.Mark] {
				base.MatchMark = member.Mark
				rules = append(rules, base)
			}
		}
	}
	if in.AssignV6.Mark != 0 || len(in.AssignV6.Slots) != 0 {
		for _, source := range []netip.Prefix{netip.PrefixFrom(in.OpnsenseEdgeV6, 128), in.InternalPrefix} {
			base := steerRule{IifName: "", Source: source, Mode: in.Mode, Assign: in.AssignV6, MatchMark: 0, DropIface: "", AllowedMarks: nil}
			rules = append(rules, base)
			for _, member := range in.Members {
				if !in.EligibleV6[member.Mark] {
					base.MatchMark = member.Mark
					rules = append(rules, base)
				}
			}
		}
	}
	v4Marks := eligibleMarks(in.EligibleV4)
	v6Marks := eligibleMarks(in.EligibleV6)
	for _, member := range in.Members {
		var allowedV4 []uint32
		if in.EligibleV4[member.Mark] {
			allowedV4 = v4Marks
		}
		var allowedV6 []uint32
		if in.EligibleV6[member.Mark] {
			allowedV6 = v6Marks
		}
		rules = append(
			rules,
			steerRule{IifName: in.InternalIface, Source: netip.MustParsePrefix("0.0.0.0/0"), Mode: "", Assign: balancer{Mark: 0, Modulus: 0, Slots: nil}, MatchMark: 0, DropIface: member.Iface, AllowedMarks: allowedV4},
			steerRule{IifName: in.InternalIface, Source: netip.MustParsePrefix("::/0"), Mode: "", Assign: balancer{Mark: 0, Modulus: 0, Slots: nil}, MatchMark: 0, DropIface: member.Iface, AllowedMarks: allowedV6},
		)
	}
	return rules
}

func eligibleMarks(eligible map[uint32]bool) []uint32 {
	marks := make([]uint32, 0, len(eligible))
	for mark, ready := range eligible {
		if ready {
			marks = append(marks, mark)
		}
	}
	slices.Sort(marks)
	return marks
}

// balancerFor returns how the active tier's healthy providers share new
// connections: one slot per weight unit, ordered by ascending mark. ok is false
// when no provider is healthy anywhere, which programs no rules at all rather
// than a mark whose table holds no route.
func balancerFor(members []Member, health netif.HealthStates) (balancer, bool) {
	none := balancer{Mark: 0, Modulus: 0, Slots: nil}
	activeTier, anyHealthy := netif.ActiveTier(tierMembers(members), health)
	if !anyHealthy {
		return none, false
	}
	carrying := make([]Member, 0, len(members))
	for _, member := range members {
		if member.Tier != activeTier || !netif.HealthIsHealthy(health.State(member.Name)) {
			continue
		}
		carrying = append(carrying, member)
	}
	if len(carrying) == 0 {
		return none, false
	}
	slices.SortFunc(carrying, func(left Member, right Member) int {
		return cmp.Compare(left.Mark, right.Mark)
	})
	if len(carrying) == 1 {
		// One provider takes everything whatever its weight, because there is
		// nothing to divide. This is also the shape the gateway runs in while
		// only the fallback tier is up.
		return balancer{Mark: carrying[0].Mark, Modulus: 0, Slots: nil}, true
	}
	slots := make([]uint32, 0, len(carrying))
	modulus := uint32(0)
	for _, member := range carrying {
		for range member.Weight {
			slots = append(slots, member.Mark)
			modulus++
		}
	}
	return balancer{Mark: 0, Modulus: modulus, Slots: slots}, true
}

// tierMembers projects the configured providers onto the list the shared
// active-tier function reads.
func tierMembers(members []Member) []netif.TierMember {
	tiers := make([]netif.TierMember, 0, len(members))
	for _, member := range members {
		tiers = append(tiers, netif.TierMember{Name: member.Name, Tier: member.Tier})
	}
	return tiers
}
