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
	Socket string `json:"socket"`
	Flows  []Flow `json:"flow"`
}

// Flow matches packets in one OpenFlow table and defines their actions.
type Flow struct {
	Name      string   `json:"name"`
	Table     uint8    `json:"table"`
	Priority  uint16   `json:"priority"`
	InPort    string   `json:"in-port"`
	EtherType *uint16  `json:"ether-type"`
	Output    []string `json:"output"`

	RawMatch       [][]byte `json:"raw-match"`
	RawInstruction [][]byte `json:"raw-instruction"`
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
