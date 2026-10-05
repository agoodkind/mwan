package netif

// TierMember is one steering member's tier membership: the name the health
// state file records a verdict under, and the tier the inventory puts it in.
type TierMember struct {
	Name string
	Tier uint8
}

// ActiveTier returns the lowest-numbered tier holding at least one healthy
// member, and whether any member is healthy at all. The tiers in inventory
// decide the failover order and nothing else does, so this function carries no
// tie-break of its own.
//
// An unknown verdict reads healthy, which is what makes every member usable
// before the health module writes its first state and is the startup behavior
// the gateway has today.
//
// Both the routing module and the steering module decide from this one
// function, so the catch-all route and the balancing marks can never disagree
// about which tier is carrying traffic.
func ActiveTier(members []TierMember, health HealthStates) (uint8, bool) {
	active := uint8(0)
	found := false
	for _, member := range members {
		if !HealthIsHealthy(health.State(member.Name)) {
			continue
		}
		if !found || member.Tier < active {
			active = member.Tier
			found = true
		}
	}
	return active, found
}
