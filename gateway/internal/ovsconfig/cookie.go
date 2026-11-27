package ovsconfig

import (
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
)

// FlowCookiePrefix reserves the upper 32 bits for MWAN rules.
const FlowCookiePrefix uint64 = 0x6d77616e00000000

var errCookieConflict = errors.New("OpenFlow rule cookie conflict")

// Cookie identifies this configured rule within the MWAN cookie range.
//
// The gateway writes this value when it installs the rule. The status
// command uses the same value to select the rule's counters.
//
// Cookie layout, from most to least significant bits:
//
//	32 bits: MWAN ownership prefix
//	32 bits: IEEE CRC-32 of the configured rule name
func (flow Flow) Cookie() uint64 {
	return FlowCookiePrefix | uint64(crc32.ChecksumIEEE([]byte(flow.Name)))
}

// RuleCookies maps each unique rule cookie to its configured name.
//
// A duplicate or legacy cookie prevents attribution. Reject the rule set
// before installing flows or displaying packet counters.
//
//	names, err := ovsconfig.RuleCookies(settings.OpenFlow.Flows)
//	if err != nil {
//	    return err
//	}
//	if name, found := names[installedCookie]; found {
//	    fmt.Printf("%s counted %d packets\n", name, packets)
//	}
func RuleCookies(flows []Flow) (map[uint64]string, error) {
	names := make(map[uint64]string, len(flows))
	for _, flow := range flows {
		cookie := flow.Cookie()
		if previous, exists := names[cookie]; exists || cookie == FlowCookiePrefix {
			slog.Error(
				"OpenFlow rule cookie conflict",
				"name",
				flow.Name,
				"previous",
				previous,
				"err",
				errCookieConflict,
			)

			return nil, fmt.Errorf(
				"%w for %q and %q: %#x",
				errCookieConflict,
				previous,
				flow.Name,
				cookie,
			)
		}

		names[cookie] = flow.Name
	}

	return names, nil
}
