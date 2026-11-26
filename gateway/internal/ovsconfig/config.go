// Package ovsconfig defines Open vSwitch settings for network documents.
package ovsconfig

// Config specifies a local database, bridge, and ports.
type Config struct {
	Socket        string `json:"database-socket"`
	TimeoutMillis uint32 `json:"startup-timeout-millis"`
	Bridge        string `json:"bridge"`
	DatapathType  string `json:"datapath-type"`
	Offload       string `json:"offload"`
	Ports         []Port `json:"port"`
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
