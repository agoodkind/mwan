// Package ovsconfig defines Open vSwitch settings for network documents.
package ovsconfig

// Config specifies a local database, bridge, and ports.
type Config struct {
	Socket        string          `json:"database-socket"`
	TimeoutMillis uint32          `json:"startup-timeout-millis"`
	Bridge        string          `json:"bridge"`
	DatapathType  string          `json:"datapath-type"`
	Offload       string          `json:"offload"`
	Ports         []Port          `json:"port"`
	OpenFlow      *OpenFlowConfig `json:"openflow"`
}

// OpenFlowConfig specifies a local management socket and forwarding rules.
type OpenFlowConfig struct {
	Socket string  `json:"socket"`
	Flows  []Flow  `json:"flow"`
	Groups []Group `json:"group"`
}

// Flow matches packets in one OpenFlow table and defines their actions.
type Flow struct {
	Match          []Match    `json:"match,omitempty"`
	Actions        []Action   `json:"action,omitempty"`
	Condition      *Condition `json:"condition,omitempty"`
	Name           string     `json:"name"`
	Table          uint8      `json:"table"`
	Priority       uint16     `json:"priority"`
	InPort         string     `json:"in-port"`
	EtherType      *uint16    `json:"ether-type"`
	Output         []string   `json:"output"`
	GroupID        *uint32    `json:"group-id"`
	RawMatch       [][]byte   `json:"raw-match"`
	RawInstruction [][]byte   `json:"raw-instruction"`
}

// Group uses a logical ID that flows reference across OpenFlow tables.
type Group struct {
	ID                 uint32    `json:"id"`
	Type               GroupType `json:"type"`
	SelectionMethod    string    `json:"selection-method"`
	SelectionParameter uint64    `json:"selection-parameter,string"`
	RawProperties      [][]byte  `json:"raw-property"`
	Buckets            []Bucket  `json:"bucket"`
}

// GroupType selects OpenFlow's bucket execution behavior.
type GroupType string

// OpenFlow defines these group types.
const (
	GroupAll          GroupType = "all"
	GroupSelect       GroupType = "select"
	GroupIndirect     GroupType = "indirect"
	GroupFastFailover GroupType = "fast-failover"
)

// Bucket applies actions before sending packets to its output port.
type Bucket struct {
	Actions       []Action   `json:"action,omitempty"`
	Condition     *Condition `json:"condition,omitempty"`
	ID            uint32     `json:"id"`
	RawActions    [][]byte   `json:"raw-action"`
	Weight        *uint16    `json:"weight"`
	Output        string     `json:"output"`
	RawProperties [][]byte   `json:"raw-property"`
}

// Match selects one OVS packet field or protocol.
type Match struct {
	Field string `json:"field"`
	Value string `json:"value,omitempty"`
}

// Action specifies one operation in the configured execution order.
type Action struct {
	ID       uint32 `json:"id"`
	Name     string `json:"name"`
	Argument string `json:"argument,omitempty"`
}

// Condition selects a flow or bucket according to connection health.
// An unknown verdict preserves its current membership in OVS.
type Condition struct {
	HealthyConnection   string `json:"healthy-connection,omitempty"`
	UnhealthyConnection string `json:"unhealthy-connection,omitempty"`
}

// ConnectionID returns the WAN connection to look up in the health snapshot.
//
// For a backup route that activates when wan-a fails, read wan-a's verdict
// even if the route sends traffic through wan-b.
//
//	condition := Condition{UnhealthyConnection: "wan-a"}
//	verdict := snapshot.Health[condition.ConnectionID()].Verdict
func (condition Condition) ConnectionID() string {
	if condition.UnhealthyConnection != "" {
		return condition.UnhealthyConnection
	}

	return condition.HealthyConnection
}

// Port configures one OVS interface and its associated port.
type Port struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Options []Option `json:"options"`
}

// Option sets one OVS interface option.
type Option struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
