package bgp

import (
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
)

const (
	communityASNShift  = 16
	rejectedNoRule     = "no import rule covers the prefix"
	rejectedExported   = "the session exports the prefix"
	rejectedNoNextHop  = "the path has no valid next hop"
	rejectedNotInstall = "kernel route installation failed"
)

func importRejection(cfg SessionConfig, prefix netip.Prefix) string {
	// GoBGP reports no event when the peer withdraws its path because the
	// originated path wins best-path selection over the peer's path for the
	// same prefix.
	for _, rule := range cfg.Export {
		if rule.Prefix == prefix {
			return rejectedExported
		}
	}
	reason := rejectedNoRule
	for _, rule := range cfg.Import {
		if prefix.Bits() < rule.Prefix.Bits() || !rule.Prefix.Contains(prefix.Addr()) {
			continue
		}
		if prefix.Bits() >= int(rule.MinLength) && prefix.Bits() <= int(rule.MaxLength) {
			return ""
		}
		reason = fmt.Sprintf(
			"prefix length %d is outside the bounds %d to %d of import rule %s",
			prefix.Bits(), rule.MinLength, rule.MaxLength, rule.Prefix,
		)
	}
	return reason
}

func exportAdvertised(mode ExportMode, eligible, backupActive bool) bool {
	if !eligible {
		return false
	}
	return mode == ExportAlways || backupActive
}

func exportPath(log *slog.Logger, cfg SessionConfig, rule ExportRule) (*apiutil.Path, error) {
	nlri, err := bgppkt.NewIPAddrPrefix(rule.Prefix)
	if err != nil {
		log.Error("build bgp session NLRI failed", "prefix", rule.Prefix, "error", err)
		return nil, fmt.Errorf("build NLRI for %s: %w", rule.Prefix, err)
	}
	mpReach, err := bgppkt.NewPathAttributeMpReachNLRI(
		bgppkt.RF_IPv6_UC,
		[]bgppkt.PathNLRI{{NLRI: nlri}},
		rule.NextHop,
	)
	if err != nil {
		log.Error("build bgp session MP_REACH_NLRI failed", "prefix", rule.Prefix, "error", err)
		return nil, fmt.Errorf("build MP_REACH_NLRI for %s: %w", rule.Prefix, err)
	}

	attributes := []bgppkt.PathAttributeInterface{
		bgppkt.NewPathAttributeOrigin(bgppkt.BGP_ORIGIN_ATTR_TYPE_IGP),
		mpReach,
	}
	if rule.PrependCount > 0 {
		prepended := make([]uint32, rule.PrependCount)
		for i := range prepended {
			prepended[i] = cfg.LocalASN
		}
		segment := bgppkt.NewAs4PathParam(bgppkt.BGP_ASPATH_ATTR_TYPE_SEQ, prepended)
		attributes = append(
			attributes,
			bgppkt.NewPathAttributeAsPath([]bgppkt.AsPathParamInterface{segment}),
		)
	}
	if rule.MED != nil {
		attributes = append(attributes, bgppkt.NewPathAttributeMultiExitDisc(*rule.MED))
	}
	if cfg.internal() && rule.LocalPreference != nil {
		attributes = append(attributes, bgppkt.NewPathAttributeLocalPref(*rule.LocalPreference))
	}
	if len(rule.Communities) > 0 {
		communities := make([]uint32, 0, len(rule.Communities))
		for _, community := range rule.Communities {
			encoded := uint32(community.ASN)<<communityASNShift | uint32(community.Value)
			communities = append(communities, encoded)
		}
		attributes = append(attributes, bgppkt.NewPathAttributeCommunities(communities))
	}
	if len(rule.LargeCommunities) > 0 {
		largeCommunities := make([]*bgppkt.LargeCommunity, 0, len(rule.LargeCommunities))
		for _, community := range rule.LargeCommunities {
			largeCommunities = append(largeCommunities, bgppkt.NewLargeCommunity(
				community.GlobalAdmin, community.LocalData1, community.LocalData2,
			))
		}
		attributes = append(attributes, bgppkt.NewPathAttributeLargeCommunities(largeCommunities))
	}

	return &apiutil.Path{Family: bgppkt.RF_IPv6_UC, Nlri: nlri, Attrs: attributes}, nil
}

func withdrawalPath(log *slog.Logger, prefix netip.Prefix) (*apiutil.Path, error) {
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		log.Error("build bgp session NLRI failed", "prefix", prefix, "error", err)
		return nil, fmt.Errorf("build NLRI for %s: %w", prefix, err)
	}
	return &apiutil.Path{Family: bgppkt.RF_IPv6_UC, Nlri: nlri, Withdrawal: true}, nil
}
