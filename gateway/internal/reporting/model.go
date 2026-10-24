package reporting

import (
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/connectionid"
)

type Family uint8

const (
	IPv4 Family = 4
	IPv6 Family = 6
)

type Target string

const (
	Gateway    Target = "gateway"
	Cloudflare Target = "cloudflare"
)

type ProbeMode string

const (
	Fresh      ProbeMode = "fresh"
	Persistent ProbeMode = "persistent"
)

type Kind string

const (
	Daily    Kind = "daily"
	Incident Kind = "incident"
	Reminder Kind = "reminder"
	Recovery Kind = "recovery"
)

type ProbeKey struct {
	WAN    connectionid.ID
	Family Family
	Target Target
	Mode   ProbeMode
}

type ProbeMinute struct {
	Key                                ProbeKey
	Sent, Lost, DNSFailures, Replies   uint64
	LatencySum, LatencyMin, LatencyMax time.Duration
	Available                          bool
}

type WANTraffic struct {
	WAN                                  connectionid.ID
	Downloaded, Uploaded, Packets, Drops uint64
	Available                            bool
}

type DestinationUsage struct {
	Address              netip.Addr
	Downloaded, Uploaded uint64
}

type TalkerUsage struct {
	WAN                  connectionid.ID
	Client               netip.Addr
	Label                string
	Downloaded, Uploaded uint64
	Destinations         []DestinationUsage
}

type AttributionCoverage struct {
	WAN      connectionid.ID
	Complete bool
	Reason   string
}

type CPUUsage struct {
	Name      string
	Percent   float64
	Available bool
}

type HostUsage struct {
	CPU             []CPUUsage
	MemoryPercent   float64
	MemoryAvailable bool
}

type Minute struct {
	Start, End                       time.Time
	Partial                          bool
	Probes                           []ProbeMinute
	Traffic                          []WANTraffic
	Talkers                          []TalkerUsage
	Host                             HostUsage
	Attribution                      []AttributionCoverage
	UDPSendFailures, DNSReceiveDrops uint64
}

type IncidentView struct {
	Open           bool
	Start          time.Time
	Affected       []connectionid.ID
	RouterAffected bool
}
