package reporting

import (
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

// Family uses IP version numbers in saved observations.
type Family uint8

const (
	// IPv4 keeps IPv4 failures separate from IPv6 results on a dual-stack WAN.
	IPv4 Family = 4
	// IPv6 keeps IPv6 failures separate from IPv4 results on a dual-stack WAN.
	IPv6 Family = 6
)

// Target permits separate measurements for WAN gateway and external connectivity.
type Target string

const (
	// Gateway probes the next hop from the WAN routing table.
	Gateway Target = "gateway"
	// Cloudflare resolves the configured host before probing its address.
	Cloudflare Target = "cloudflare"
)

// ProbeMode lets reports compare newly created and reused ICMP sessions.
type ProbeMode string

const (
	// Fresh uses a new ICMP identifier for each attempt.
	Fresh ProbeMode = "fresh"
	// Persistent reuses an ICMP identifier while its endpoints and renewal interval permit it.
	Persistent ProbeMode = "persistent"
)

// Kind selects daily reporting or an incident state transition.
type Kind string

const (
	// Daily uses the current scheduled reporting period.
	Daily Kind = "daily"
	// Incident starts an alert after a complete breach window.
	Incident Kind = "incident"
	// Reminder repeats an open alert after the configured interval.
	Reminder Kind = "reminder"
	// Recovery follows a complete observation window without the active failures.
	Recovery Kind = "recovery"
)

// ProbeKey uses stable WAN identity rather than an interface name for saved history.
type ProbeKey struct {
	WAN    connectionid.ID `json:"WAN"`
	Family Family          `json:"Family"`
	Target Target          `json:"Target"`
	Mode   ProbeMode       `json:"Mode"`
}

// ProbeMinute counts attempts independently of replies and sums only reply latencies.
type ProbeMinute struct {
	Key         ProbeKey      `json:"Key"`
	Sent        uint64        `json:"Sent"`
	Lost        uint64        `json:"Lost"`
	DNSFailures uint64        `json:"DNSFailures"`
	Replies     uint64        `json:"Replies"`
	LatencySum  time.Duration `json:"LatencySum"`
	LatencyMin  time.Duration `json:"LatencyMin"`
	LatencyMax  time.Duration `json:"LatencyMax"`
	Available   bool          `json:"Available"`
}

// WANTraffic stores counter differences, not cumulative interface counters.
type WANTraffic struct {
	WAN        connectionid.ID `json:"WAN"`
	Downloaded uint64          `json:"Downloaded"`
	Uploaded   uint64          `json:"Uploaded"`
	Packets    uint64          `json:"Packets"`
	Drops      uint64          `json:"Drops"`
	Available  bool            `json:"Available"`
}

// DestinationUsage uses byte counts from the LAN client's perspective.
type DestinationUsage struct {
	Address    netip.Addr `json:"Address"`
	Downloaded uint64     `json:"Downloaded"`
	Uploaded   uint64     `json:"Uploaded"`
}

// TalkerUsage keeps each client's byte counts separate for every selected WAN.
type TalkerUsage struct {
	WAN          connectionid.ID    `json:"WAN"`
	Client       netip.Addr         `json:"Client"`
	Label        string             `json:"Label"`
	Downloaded   uint64             `json:"Downloaded"`
	Uploaded     uint64             `json:"Uploaded"`
	Destinations []DestinationUsage `json:"Destinations"`
}

// AttributionCoverage marks missing client counters independently of interface totals.
type AttributionCoverage struct {
	WAN      connectionid.ID `json:"WAN"`
	Complete bool            `json:"Complete"`
	Reason   string          `json:"Reason"`
}

// CPUUsage distinguishes unavailable readings from measured idle time.
type CPUUsage struct {
	Name      string  `json:"Name"`
	Percent   float64 `json:"Percent"`
	Available bool    `json:"Available"`
}

// HostUsage measures memory use after subtracting available memory from total memory.
type HostUsage struct {
	CPU             []CPUUsage `json:"CPU"`
	MemoryPercent   float64    `json:"MemoryPercent"`
	MemoryAvailable bool       `json:"MemoryAvailable"`
}

// Minute records explicit boundaries because startup can produce a partial interval.
type Minute struct {
	Start           time.Time             `json:"Start"`
	End             time.Time             `json:"End"`
	Partial         bool                  `json:"Partial"`
	Probes          []ProbeMinute         `json:"Probes"`
	Traffic         []WANTraffic          `json:"Traffic"`
	Talkers         []TalkerUsage         `json:"Talkers"`
	Host            HostUsage             `json:"Host"`
	Attribution     []AttributionCoverage `json:"Attribution"`
	UDPSendFailures uint64                `json:"UDPSendFailures"`
	DNSReceiveDrops uint64                `json:"DNSReceiveDrops"`
}

// IncidentView separates local gateway failures from failures attributed to a WAN.
type IncidentView struct {
	Open           bool              `json:"Open"`
	Start          time.Time         `json:"Start"`
	Affected       []connectionid.ID `json:"Affected"`
	RouterAffected bool              `json:"RouterAffected"`
	Signals        []string          `json:"Signals"`
}
