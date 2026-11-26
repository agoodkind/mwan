package healthcheck

import (
	"time"

	"goodkind.io/mwan/gateway/internal/clock"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/health"
	"goodkind.io/mwan/gateway/internal/netif"
)

// Options shares daemon provider policy without initializing daemon state.
type Options struct {
	WANs    []health.WAN
	Timeout time.Duration
	Watch   bool
	Clock   clock.Clock
}

// Record associates a shared raw probe attempt with WAN identity and completion time.
type Record struct {
	WAN       ifmgr.WANRef       `json:"wan"`
	Timestamp time.Time          `json:"timestamp"`
	Ping      *netif.PingAttempt `json:"ping,omitempty"`
	HTTP      *netif.HTTPAttempt `json:"http,omitempty"`
}
