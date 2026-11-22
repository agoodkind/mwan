package netif

// TierMember is one steering member's tier membership: the name the health
// state file records a verdict under, and the tier the inventory puts it in.
type TierMember struct {
	Name string
	Tier uint8
}

// ActiveTier returns the lowest-numbered tier with a member that is not
// unhealthy. It also reports whether such a member exists.
//
// Unknown verdicts count as usable so providers can serve traffic before the
// health module writes its first state.
//
// Routing and steering use ActiveTier to select the same tier.
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
