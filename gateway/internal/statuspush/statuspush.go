// Package statuspush defines local provider status data.
package statuspush

import (
	"time"

	"goodkind.io/mwan/internal/netif"
)

// Status stores provider verdicts and a timestamp. ActiveTier identifies the
// lowest tier among providers not marked unhealthy.
type Status struct {
	SentAt     time.Time         `json:"sent_at"`
	ActiveTier uint8             `json:"active_tier"`
	Providers  map[string]string `json:"providers"`
}

// NewStatus converts unrecognized provider states to unknown.
func NewStatus(
	sentAt time.Time,
	members []netif.TierMember,
	states netif.HealthStates,
) Status {
	verdicts := make(netif.HealthStates, len(members))
	for _, member := range members {
		verdict := states.State(member.Name)
		switch verdict {
		case netif.HealthStateHealthy, netif.HealthStateUnhealthy, netif.HealthStateUnknown:
		default:
			verdict = netif.HealthStateUnknown
		}

		verdicts[member.Name] = verdict
	}

	activeTier, _ := netif.ActiveTier(members, verdicts)

	return Status{
		SentAt:     sentAt,
		ActiveTier: activeTier,
		Providers:  verdicts,
	}
}
