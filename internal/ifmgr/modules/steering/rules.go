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

// steerRule is one balancing rule in typed form, independent of the nftables
// wire encoding. Every rule carries the same two guards, so they are not
// fields: the mark must still be zero, which preserves the control-plane pins
// an earlier chain set, and the flow must be new, which leaves an established
// flow on the provider conntrack already gave it.
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
}

// String renders one rule the way the ruleset file wrote its equivalent, for
// error messages and debug logging.
func (r steerRule) String() string {
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
	Assign         balancer
}

// buildRules returns the three rules the module programs, in the order the
// ruleset file wrote them. The first covers internal IPv4 traffic arriving on
// the internal link. The second covers replies the router sources from its own
// edge address, which a hairpinned inbound flow produces and which must leave
// over the provider that carried it in. The third covers internal IPv6 traffic.
func buildRules(in ruleInput) []steerRule {
	return []steerRule{
		{
			IifName: in.InternalIface,
			Source:  in.InternalNetV4,
			Mode:    in.Mode,
			Assign:  in.Assign,
		},
		{
			IifName: "",
			Source:  netip.PrefixFrom(in.OpnsenseEdgeV6, 128),
			Mode:    in.Mode,
			Assign:  in.Assign,
		},
		{
			IifName: "",
			Source:  in.InternalPrefix,
			Mode:    in.Mode,
			Assign:  in.Assign,
		},
	}
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
