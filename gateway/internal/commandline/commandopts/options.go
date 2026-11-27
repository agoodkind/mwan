// Package commandopts defines the option types that operations read after
// Cobra parses command flags.
//
// Interface manager options supply the WANCONFIG file and YANG schema paths.
// WANCONFIG supplies forwarding settings, including the OVS selection.
package commandopts

// Ifmgr contains the options for starting the interface manager.
type Ifmgr struct {
	Role        string
	NetworkPath string
	SchemaDir   string
	Debug       bool
	DryRun      bool
}

// Health contains the options for running gateway health checks.
type Health struct {
	NetworkPath, SchemaDir, OutputFormat string
	Watch, Verbose                       bool
}

// Status contains the options for showing gateway status.
type Status struct {
	NetworkPath, SchemaDir, OutputFormat string
	Verbose                              bool
}

// Selftest contains the repository and model paths for WANCONFIG validation.
type Selftest struct {
	Repository string
	ModelsDir  string
}

// Install contains the options for installing gateway files and units.
type Install struct {
	Role        string
	Apply       bool
	PrintSchema string
	Root        string
}

// Debug contains the selected view and its arguments.
type Debug struct {
	View         DebugView
	Arguments    []string
	OutputFormat string
	Verbose      bool
}

// DebugView identifies a one-shot inspection or probe.
type DebugView string

const (
	// DebugViewConnectivity selects connectivity inspection.
	DebugViewConnectivity DebugView = "connectivity"
	// DebugViewCurl4 selects IPv4 HTTP probes.
	DebugViewCurl4 DebugView = "curl4"
	// DebugViewCurl6 selects IPv6 HTTP probes.
	DebugViewCurl6 DebugView = "curl6"
	// DebugViewLB4 selects IPv4 provider distribution probes.
	DebugViewLB4 DebugView = "lb4"
	// DebugViewLB4Ifaces selects IPv4 provider distribution probes by interface.
	DebugViewLB4Ifaces DebugView = "lb4-ifaces"
	// DebugViewLB6 selects IPv6 provider distribution probes.
	DebugViewLB6 DebugView = "lb6"
	// DebugViewLB6Ifaces selects IPv6 provider distribution probes by interface.
	DebugViewLB6Ifaces DebugView = "lb6-ifaces"
	// DebugViewNPT selects IPv6 prefix translation inspection.
	DebugViewNPT DebugView = "npt"
	// DebugViewPing4 selects IPv4 ping probes.
	DebugViewPing4 DebugView = "ping4"
	// DebugViewPing6 selects IPv6 ping probes.
	DebugViewPing6 DebugView = "ping6"
	// DebugViewPolicy selects routing policy inspection.
	DebugViewPolicy DebugView = "policy"
	// DebugViewPrefixes selects delegated-prefix inspection.
	DebugViewPrefixes DebugView = "prefixes"
	// DebugViewRoutes selects IPv6 route inspection.
	DebugViewRoutes DebugView = "routes"
	// DebugViewSim4 selects IPv4 provider selection simulation.
	DebugViewSim4 DebugView = "sim4"
	// DebugViewSim6 selects IPv6 provider selection simulation.
	DebugViewSim6 DebugView = "sim6"
	// DebugViewStats selects provider and connection counter inspection.
	DebugViewStats DebugView = "stats"
	// DebugViewStatus selects link, address, and prefix inspection.
	DebugViewStatus DebugView = "status"
	// DebugViewSystemd selects gateway systemd unit inspection.
	DebugViewSystemd DebugView = "systemd"
	// DebugViewTraceStart selects trace collection startup.
	DebugViewTraceStart DebugView = "trace-start"
	// DebugViewTraceStop selects trace collection shutdown.
	DebugViewTraceStop DebugView = "trace-stop"
	// DebugViewTraceTail selects trace output inspection.
	DebugViewTraceTail DebugView = "trace-tail"
)
