package reporting

import (
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

// Family distinguishes IPv4 and IPv6 measurements for the same WAN.
type Family uint8

const (
	// IPv4 selects measurements that use IPv4 addresses.
	IPv4 Family = 4
	// IPv6 selects measurements that use IPv6 addresses.
	IPv6 Family = 6
)

// Target distinguishes the first gateway from the external probe destination.
type Target string

const (
	// Gateway checks the WAN's immediate routing neighbor.
	Gateway Target = "gateway"
	// Cloudflare checks the configured external destination.
	Cloudflare Target = "cloudflare"
)

// ProbeMode separates new connections from connections reused across probes.
type ProbeMode string

const (
	// Fresh creates a connection for each probe.
	Fresh ProbeMode = "fresh"
	// Persistent reuses a connection until its renewal deadline.
	Persistent ProbeMode = "persistent"
)

// Kind selects the report's delivery reason and presentation.
type Kind string

const (
	// Daily summarizes the scheduled reporting period.
	Daily Kind = "daily"
	// Incident reports a newly detected service failure.
	Incident Kind = "incident"
	// Reminder reports an incident that remains open.
	Reminder Kind = "reminder"
	// Recovery reports the end of an incident.
	Recovery Kind = "recovery"
)

// ProbeKey identifies one independently measured probe series.
type ProbeKey struct {
	WAN    connectionid.ID `json:"WAN"`
	Family Family          `json:"Family"`
	Target Target          `json:"Target"`
	Mode   ProbeMode       `json:"Mode"`
}

// ProbeMinute retains counts and latency totals for one reporting interval.
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

// WANTraffic records interface counter deltas for one reporting interval.
type WANTraffic struct {
	WAN        connectionid.ID `json:"WAN"`
	Downloaded uint64          `json:"Downloaded"`
	Uploaded   uint64          `json:"Uploaded"`
	Packets    uint64          `json:"Packets"`
	Drops      uint64          `json:"Drops"`
	Available  bool            `json:"Available"`
}

// DestinationUsage attributes client traffic to an external address.
type DestinationUsage struct {
	Address    netip.Addr `json:"Address"`
	Downloaded uint64     `json:"Downloaded"`
	Uploaded   uint64     `json:"Uploaded"`
}

// TalkerUsage attributes WAN traffic to a LAN client and its destinations.
type TalkerUsage struct {
	WAN          connectionid.ID    `json:"WAN"`
	Client       netip.Addr         `json:"Client"`
	Label        string             `json:"Label"`
	Downloaded   uint64             `json:"Downloaded"`
	Uploaded     uint64             `json:"Uploaded"`
	Destinations []DestinationUsage `json:"Destinations"`
}

// AttributionCoverage records whether client accounting covers a WAN interval.
type AttributionCoverage struct {
	WAN      connectionid.ID `json:"WAN"`
	Complete bool            `json:"Complete"`
	Reason   string          `json:"Reason"`
}

// CPUUsage separates missing CPU samples from measured idle time.
type CPUUsage struct {
	Name      string  `json:"Name"`
	Percent   float64 `json:"Percent"`
	Available bool    `json:"Available"`
}

// HostUsage combines CPU samples with memory use for the reporting interval.
type HostUsage struct {
	CPU             []CPUUsage `json:"CPU"`
	MemoryPercent   float64    `json:"MemoryPercent"`
	MemoryAvailable bool       `json:"MemoryAvailable"`
}

// Minute groups measurements that share one collection interval.
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

// IncidentView supplies the incident context required to render a report.
type IncidentView struct {
	Open           bool              `json:"Open"`
	Start          time.Time         `json:"Start"`
	Affected       []connectionid.ID `json:"Affected"`
	RouterAffected bool              `json:"RouterAffected"`
	Signals        []string          `json:"Signals"`
}
