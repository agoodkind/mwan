package vpp

import (
	"time"

	"go.fd.io/govpp/binapi/vlib"
)

// SampleClass distinguishes recognized packet instructions from other or unresolved code.
type SampleClass string

const (
	samplePacketWork SampleClass = "packet-work"
	sampleOther      SampleClass = "other"
	sampleUnresolved SampleClass = "unresolved"
)

// SampleCounts keeps lost and unresolved samples separate from recognized packet work.
type SampleCounts struct {
	Total        uint64 `json:"Total"`
	PacketWork   uint64 `json:"PacketWork"`
	EmptyPolling uint64 `json:"EmptyPolling"`
	Other        uint64 `json:"Other"`
	Unresolved   uint64 `json:"Unresolved"`
	Lost         uint64 `json:"Lost"`
}

// SymbolSamples records the mapped ELF module and function for native instruction samples.
type SymbolSamples struct {
	Module  string      `json:"Module"`
	Symbol  string      `json:"Symbol"`
	Class   SampleClass `json:"Class"`
	Samples uint64      `json:"Samples"`
}

// ThreadProfile keeps CPU clock validity independent of symbol resolution and sample counts.
type ThreadProfile struct {
	Thread            vlib.ThreadData `json:"Thread"`
	Available         bool            `json:"Available"`
	Reason            string          `json:"Reason"`
	CPUClockTime      time.Duration   `json:"CPUClockTime"`
	CPUClockAvailable bool            `json:"CPUClockAvailable"`
	Counts            SampleCounts    `json:"Counts"`
	Symbols           []SymbolSamples `json:"Symbols"`
}

// ProfileResult reports native sampling coverage. PacketWork counts recognized
// packet functions; receive functions do not establish exact empty polling time.
type ProfileResult struct {
	Available             bool            `json:"Available"`
	Reason                string          `json:"Reason"`
	Duration              time.Duration   `json:"Duration"`
	SamplePeriod          time.Duration   `json:"SamplePeriod"`
	EmptyPollingAvailable bool            `json:"EmptyPollingAvailable"`
	Counts                SampleCounts    `json:"Counts"`
	Threads               []ThreadProfile `json:"Threads"`
}
