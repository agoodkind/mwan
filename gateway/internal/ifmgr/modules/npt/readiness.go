package npt

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/nptpolicy"
	"goodkind.io/mwan/internal/wanstate"
)

func attachmentsReady(states []bpf.AttachmentState, index int) bool {
	if index == 0 {
		return false
	}

	ingress := false
	egress := false

	for _, state := range states {
		if state.IfIndex != index || !state.Ready {
			continue
		}

		if state.Direction == bpf.Ingress {
			ingress = true
		}

		if state.Direction == bpf.Egress {
			egress = true
		}
	}

	return ingress && egress
}

// Native readiness requires verified nft absence even after an apply fails.
func (m *Module) publishLiveState(
	ctx context.Context,
	log *slog.Logger,
	delegated map[string]netip.Prefix,
	desired desiredRules,
	applied bool,
	bpfReady map[string]bool,
	nativeReady bool,
	pairs map[string]nptpolicy.PrefixPair,
) {
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}

	// The intended ruleset is this pass's desired rules rendered in the
	// same text form the inspector renders live rules, so the served
	// intent and any live listing read alike.
	m.Env.LiveState.SetIntendedRuleset(renderIntended(desired))
	rendered, inspectErr := RenderTable(ctx, log)
	if inspectErr != nil {
		log.WarnContext(ctx, "npt: kernel read-back for the surface failed", "err", inspectErr)
	}

	members := make(map[string]wanstate.MemberTranslation, len(m.cfg.WANs))
	verifiedPairs := make(map[string]nptpolicy.PrefixPair, len(pairs))

	internalV4, internalV4Err := netip.ParsePrefix(m.cfg.InternalNetV4)
	for _, wan := range m.cfg.WANs {
		var member wanstate.MemberTranslation
		if wan.TranslationV4 != nil {
			member.V4 = IPv4TranslationReadiness(ctx, wan, internalV4)
			if internalV4Err != nil && wan.TranslationV4.Mode == config.TranslationNAPT44 {
				member.V4.Ready = false
				member.V4.Reason = "internal IPv4 source network is unavailable"
			}

			if wan.Owned &&
				(m.Env.OwnedAddresses == nil || !m.Env.OwnedAddresses.FamilyReady(
					wan.Key(),
					"ipv4",
				)) {
				member.V4.Ready = false
				member.V4.Reason = "MWAN-owned IPv4 addresses are not verified"
			}
		}

		member.V6 = ipv6TranslationReadiness(
			wan,
			delegated[wan.Key()],
			applied && rendered.HasInterface(wan.Iface),
			bpfReady[wan.Key()],
			nativeReady && inspectErr == nil && rendered.interfaceAbsent(wan.Iface),
		)
		m.updateMasqueradeReadiness(
			wan,
			&member.V6,
			rendered,
			applied && inspectErr == nil && nativeReady,
		)
		if pair, found := pairs[wan.Key()]; found && member.V6.Ready {
			if pair.Internal == member.V6.InternalPrefix &&
				pair.External == member.V6.ExternalPrefix {
				verifiedPairs[wan.Key()] = pair
			} else {
				member.V6.Ready = false
				member.V6.Reason = "NPT prefixes differ from the verified policy"
			}
		}

		members[wan.Key()] = member
	}

	if m.Env.LiveState.SetNPTTranslation(members, verifiedPairs) && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("NPT translation readiness changed")
	}
}

func (m *Module) updateMasqueradeReadiness(
	wan WAN,
	state *wanstate.FamilyTranslation,
	rendered RenderedTable,
	verified bool,
) {
	if !isMasquerade(wan) {
		return
	}

	expected := formatRule(masqueradeRule(wan.Iface))
	state.Ready = verified && slices.Contains(rendered.Postrouting, expected)
	if !state.Ready {
		state.Reason = "IPv6 masquerade is not verified"
	}

	if wan.Owned &&
		(m.Env.OwnedAddresses == nil || !m.Env.OwnedAddresses.FamilyReady(wan.Key(), "ipv6")) {
		state.Ready = false
		state.Reason = "MWAN-owned IPv6 addresses are not verified"
	}
}

func ipv6TranslationReadiness(
	wan WAN,
	external netip.Prefix,
	kernelReady bool,
	bpfReady bool,
	nativeReady bool,
) wanstate.FamilyTranslation {
	var result wanstate.FamilyTranslation
	if wan.Translation == nil {
		return result
	}

	result.Mode = string(wan.Translation.Mode)
	if wan.Translation.Mode == config.TranslationNative {
		result.Ready = nativeReady
		if !nativeReady {
			result.Reason = "stale IPv6 translation cleanup is unverified"
		}

		return result
	}

	if wan.Translation.NPT == nil {
		return result
	}

	result.InternalPrefix = wan.Translation.NPT.InternalPrefix
	result.ExternalPrefix = external
	result.Ready = kernelReady && bpfReady
	if !result.Ready {
		result.Reason = "required IPv6 translation is unavailable"
	}

	return result
}
