package networkjson

import (
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/bgpsession"
	"goodkind.io/mwan/internal/connectionid"
)

type bgpImportWire struct {
	Prefix    string `json:"prefix"`
	MinLength *uint8 `json:"min-length"`
	MaxLength *uint8 `json:"max-length"`
}

type bgpExportWire struct {
	Prefix           string   `json:"prefix"`
	Mode             string   `json:"mode"`
	BackupFor        []string `json:"backup-for"`
	NextHop          string   `json:"next-hop"`
	MED              *uint32  `json:"med"`
	LocalPreference  *uint32  `json:"local-preference"`
	PrependCount     uint8    `json:"prepend-count"`
	Communities      []string `json:"community"`
	LargeCommunities []string `json:"large-community"`
}

func parseBGPPrefix(label string, leaf string, raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		slog.Error("networkjson: bgp-session prefix unparsable", "session", label, "leaf", leaf, "value", raw, "err", err)
		return netip.Prefix{}, fmt.Errorf("%s: %s %q: %w", label, leaf, raw, err)
	}
	if !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("%s: %s %s is not an IPv6 prefix", label, leaf, raw)
	}
	return prefix, nil
}

func buildBGPImports(label string, wires []bgpImportWire) ([]bgpsession.ImportRule, error) {
	rules := make([]bgpsession.ImportRule, 0, len(wires))
	for _, wire := range wires {
		prefix, err := parseBGPPrefix(label, "import prefix", wire.Prefix)
		if err != nil {
			return nil, err
		}
		if wire.MinLength == nil || wire.MaxLength == nil {
			return nil, fmt.Errorf("%s: import %s requires min-length and max-length", label, prefix)
		}
		rules = append(rules, bgpsession.ImportRule{Prefix: prefix, MinLength: *wire.MinLength, MaxLength: *wire.MaxLength})
	}
	return rules, nil
}

func buildBGPExports(
	label string,
	wires []bgpExportWire,
) ([]bgpsession.ExportRule, map[netip.Prefix][]connectionid.ID, error) {
	rules := make([]bgpsession.ExportRule, 0, len(wires))
	backupFor := make(map[netip.Prefix][]connectionid.ID)
	for _, wire := range wires {
		prefix, err := parseBGPPrefix(label, "export prefix", wire.Prefix)
		if err != nil {
			return nil, nil, err
		}
		exportLabel := label + " export " + prefix.String()
		mode, known := bgpsession.ParseExportMode(wire.Mode)
		if !known {
			return nil, nil, fmt.Errorf("%s: mode %q is not always or backup", exportLabel, wire.Mode)
		}
		rule := bgpsession.ExportRule{
			Prefix: prefix, Mode: mode, LocalPreference: wire.LocalPreference, MED: wire.MED,
			PrependCount: wire.PrependCount, Communities: nil, LargeCommunities: nil, NextHop: netip.Addr{},
		}
		backup := mode == bgpsession.ExportBackup
		if backup && len(wire.BackupFor) == 0 {
			return nil, nil, fmt.Errorf("%s: mode backup requires backup-for", exportLabel)
		}
		if !backup && len(wire.BackupFor) != 0 {
			return nil, nil, fmt.Errorf("%s: backup-for requires mode backup", exportLabel)
		}
		for _, id := range wire.BackupFor {
			backupFor[prefix.Masked()] = append(backupFor[prefix.Masked()], connectionid.ID(id))
		}
		if wire.NextHop == "" {
			return nil, nil, fmt.Errorf("%s: next-hop is required", exportLabel)
		}
		if rule.NextHop, err = parseAddress(exportLabel, "next-hop", wire.NextHop); err != nil {
			return nil, nil, err
		}
		if rule.Communities, err = parseCommunities(exportLabel, wire.Communities); err != nil {
			return nil, nil, err
		}
		if rule.LargeCommunities, err = parseLargeCommunities(exportLabel, wire.LargeCommunities); err != nil {
			return nil, nil, err
		}
		rules = append(rules, rule)
	}
	return rules, backupFor, nil
}

func parseCommunities(label string, raws []string) ([]bgpsession.Community, error) {
	communities := make([]bgpsession.Community, 0, len(raws))
	for _, raw := range raws {
		community, canonical := bgpsession.ParseCommunity(raw)
		if !canonical {
			return nil, fmt.Errorf("%s: community %q is not ASN:value with two 16-bit decimal numbers", label, raw)
		}
		communities = append(communities, community)
	}
	return communities, nil
}

func parseLargeCommunities(label string, raws []string) ([]bgpsession.LargeCommunity, error) {
	communities := make([]bgpsession.LargeCommunity, 0, len(raws))
	for _, raw := range raws {
		community, canonical := bgpsession.ParseLargeCommunity(raw)
		if !canonical {
			return nil, fmt.Errorf("%s: large-community %q is not three 32-bit decimal numbers separated by colons", label, raw)
		}
		communities = append(communities, community)
	}
	return communities, nil
}
