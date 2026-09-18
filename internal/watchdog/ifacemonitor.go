package watchdog

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// collectIfaceAddrs returns a map[ifaceName][]cidr for all non-loopback interfaces.
func collectIfaceAddrs() map[string][]string {
	result := make(map[string][]string)
	ifaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var cidrs []string
		for _, a := range addrs {
			cidrs = append(cidrs, a.String())
		}
		result[iface.Name] = cidrs
	}
	return result
}

// diffIfaceAddrs returns a human-readable diff between two interface address snapshots.
// Returns empty string if nothing changed.
func diffIfaceAddrs(prev, curr map[string][]string) string {
	prevSet := func(m map[string][]string) map[string]map[string]struct{} {
		out := make(map[string]map[string]struct{})
		for iface, addrs := range m {
			out[iface] = make(map[string]struct{})
			for _, a := range addrs {
				out[iface][a] = struct{}{}
			}
		}
		return out
	}
	ps := prevSet(prev)
	cs := prevSet(curr)

	var changes []string

	// Check for removed addresses or removed interfaces.
	for iface, paddrs := range ps {
		caddrs, exists := cs[iface]
		if !exists {
			changes = append(changes, fmt.Sprintf("  REMOVED  %-20s  (interface gone)", iface))
			continue
		}
		for a := range paddrs {
			if _, ok := caddrs[a]; !ok {
				changes = append(changes,
					fmt.Sprintf("  REMOVED  %-20s  %s", iface, a))
			}
		}
	}

	// Check for added addresses or new interfaces.
	for iface, caddrs := range cs {
		paddrs, exists := ps[iface]
		if !exists {
			for a := range caddrs {
				changes = append(changes,
					fmt.Sprintf("  ADDED    %-20s  %s (new interface)", iface, a))
			}
			continue
		}
		for a := range caddrs {
			if _, ok := paddrs[a]; !ok {
				changes = append(changes,
					fmt.Sprintf("  ADDED    %-20s  %s", iface, a))
			}
		}
	}

	if len(changes) == 0 {
		return ""
	}
	return strings.Join(changes, "\n")
}

// runIfaceMonitor polls the vault host's interface addresses every 30 seconds.
// When any address is added or removed on any interface, it logs the change and
// sends an alert email so network changes are never silent.
func (w *watchdog) runIfaceMonitor(ctx context.Context) {
	log := w.tracedLogger(ctx)
	const pollInterval = 30 * time.Second
	prev := collectIfaceAddrs()
	log.InfoContext(ctx,
		"Interface monitor started",
		"poll_interval", pollInterval,
		"interface_count", len(prev),
	)
	for {
		select {
		case <-ctx.Done():
			break
		case <-time.After(pollInterval):
		}
		if ctx.Err() != nil {
			break
		}
		curr := collectIfaceAddrs()
		diff := diffIfaceAddrs(prev, curr)
		if diff == "" {
			prev = curr
			continue
		}
		log.WarnContext(ctx,
			"vault host interface address changed",
			"diff", diff,
			"vm_id", w.cfg.MwanVMID,
			"node", w.cfg.PVE.Node,
		)
		prev = curr
	}
}
