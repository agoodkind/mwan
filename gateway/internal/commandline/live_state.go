package commandline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mwanv1 "goodkind.io/mwan/gateway/gen/mwan/v1"
	"goodkind.io/mwan/gateway/internal/clock"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/observation/contract"
	"goodkind.io/mwan/gateway/internal/wanconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
	"goodkind.io/mwan/gateway/internal/yangpub"
	"goodkind.io/mwan/gateway/networkjson"
)

const (
	// BGP state becomes stale after a poll misses this interval.
	bgpPollInterval = 15 * time.Second
	bgpStaleAfter   = 3 * bgpPollInterval
	bgpPollTimeout  = 5 * time.Second

	baseInterfaceStateItems = 4

	steeringPrefix = "goodkind-mwan-steering"

	moduleInterfaces = "ietf-interfaces"
	moduleNAT        = "ietf-nat"
	moduleSteering   = steeringPrefix
)

// Claim the steering module before operational reads use its settings.
var publishedModules = []string{moduleInterfaces, moduleNAT, moduleSteering}

// A failed claim leaves configuration reads on the running datastore.
// Operational reads continue to report live state.
func ownPublishedModules(ctx context.Context, log *slog.Logger, pub yangpub.Publisher) {
	for _, module := range publishedModules {
		if err := pub.OwnModule(ctx, module); err != nil {
			log.ErrorContext(
				ctx,
				"wanconfig: module ownership failed; serving operational state only",
				"module",
				module,
				"err",
				err,
			)
		}
	}
}

// Register providers that read the latest snapshot outside reconciliation.
func registerLiveStateProviders(
	ctx context.Context,
	log *slog.Logger,
	pub yangpub.Publisher,
	store *wanstate.Store,
	gateway wanconfig.Gateway,
	rejections []networkjson.Rejection,
) error {
	interfacesProvider := func(_ context.Context, _ string) ([]yangpub.Item, error) {
		return interfacesLiveItems(store.Snapshot(), gateway, rejections, clock.Real{}.Now()), nil
	}
	if err := pub.RegisterProvider(
		ctx,
		moduleInterfaces,
		"/ietf-interfaces:interfaces",
		interfacesProvider,
	); err != nil {
		log.ErrorContext(ctx, "wanconfig: register interfaces provider failed", "err", err)

		return fmt.Errorf("register interfaces provider: %w", err)
	}

	natProvider := func(_ context.Context, _ string) ([]yangpub.Item, error) {
		return natLiveItems(store.Snapshot(), gateway), nil
	}
	if err := pub.RegisterProvider(ctx, moduleNAT, "/ietf-nat:nat", natProvider); err != nil {
		log.ErrorContext(ctx, "wanconfig: register nat provider failed", "err", err)

		return fmt.Errorf("register nat provider: %w", err)
	}

	log.DebugContext(ctx, "wanconfig: providers registered")

	return registerObservationProvider(ctx, pub, store)
}

func registerObservationProvider(
	ctx context.Context,
	pub yangpub.Publisher,
	store *wanstate.Store,
) error {
	observationProvider := func(_ context.Context, _ string) ([]yangpub.Item, error) {
		return observationLiveItems(store.Snapshot())
	}
	if err := pub.RegisterProvider(
		ctx,
		moduleSteering,
		"/goodkind-mwan-steering:daemon/observation",
		observationProvider,
	); err != nil {
		slog.ErrorContext(ctx, "wanconfig: register observation provider failed", "err", err)

		return fmt.Errorf("register observation provider: %w", err)
	}

	return nil
}

func observationLiveItems(snapshot wanstate.Snapshot) ([]yangpub.Item, error) {
	if snapshot.ObservationError != nil {
		return nil, snapshot.ObservationError
	}

	var items []yangpub.Item

	checkIDs := make([]string, 0, len(snapshot.Checks))
	for checkID := range snapshot.Checks {
		checkIDs = append(checkIDs, checkID)
	}

	sort.Strings(checkIDs)

	for _, checkID := range checkIDs {
		state := snapshot.Checks[checkID]
		definition, err := wanconfig.MarshalObservationItems(state.Check, state.IntervalSeconds)
		if err != nil {
			slog.Warn(
				"wanconfig: observation definition could not be projected",
				"check_id",
				checkID,
				"err",
				err,
			)

			return nil, fmt.Errorf("project observation definition: %w", err)
		}

		for _, item := range definition {
			items = append(items, yangpub.Item{Path: item.Path, Value: item.Value})
		}

		base := "/goodkind-mwan-steering:daemon/observation/check[id='" +
			checkID + "']"
		resultItems, err := marshalObservationResult(base+"/result", state.Result)
		if err != nil {
			return nil, err
		}

		items = append(items, resultItems...)

		for _, transition := range state.Recent {
			transitionBase := base + "/recent-transition[id='" + transition.ID + "']"
			items = append(
				items,
				yangpub.Item{Path: transitionBase + "/id", Value: transition.ID},
				yangpub.Item{Path: transitionBase + "/previous", Value: transition.Previous},
			)
			resultItems, err := marshalObservationResult(
				transitionBase+"/result",
				transition.Result,
			)
			if err != nil {
				return nil, err
			}

			items = append(items, resultItems...)
		}
	}

	return items, nil
}

func marshalObservationResult(base string, result contract.Result) ([]yangpub.Item, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		slog.Error(
			"served observation could not be encoded",
			"check_id",
			result.CheckID,
			"err",
			err,
		)

		return nil, fmt.Errorf("encode served observation: %w", err)
	}

	items := []yangpub.Item{
		{Path: base + "/availability", Value: string(result.Availability)},
		{Path: base + "/outcome", Value: string(result.Outcome)},
		{Path: base + "/reason", Value: result.Reason},
		{Path: base + "/observer-kind", Value: string(result.Observer.Kind)},
		{Path: base + "/machine-id", Value: result.Observer.MachineID},
		{Path: base + "/interface", Value: result.Path.Interface},
		{Path: base + "/connection-id", Value: result.Path.ConnectionID},
		{Path: base + "/router", Value: string(result.Path.Router)},
		{Path: base + "/http-status", Value: strconv.Itoa(result.HTTPStatus)},
		{Path: base + "/ssh-version", Value: result.SSHVersion},
		{Path: base + "/evidence-json", Value: string(encoded)},
	}
	if !result.ObservedAt.IsZero() {
		items = append(
			items,
			yangpub.Item{
				Path:  base + "/observed-at",
				Value: result.ObservedAt.UTC().Format(time.RFC3339Nano),
			},
		)
	}

	for _, address := range []struct {
		leaf  string
		value netip.Addr
	}{
		{"source", result.Path.Source},
		{"destination", result.Path.Destination},
		{"next-hop", result.Path.NextHop},
		{"public-ip", result.PublicIP},
	} {
		if address.value.IsValid() {
			items = append(
				items,
				yangpub.Item{Path: base + "/" + address.leaf, Value: address.value.String()},
			)
		}
	}

	return items, nil
}

func interfacesLiveItems(
	snap wanstate.Snapshot,
	gateway wanconfig.Gateway,
	rejections []networkjson.Rejection,
	now time.Time,
) []yangpub.Item {
	items := make(
		[]yangpub.Item,
		0,
		len(gateway.Members)*8+len(rejections)*3+baseInterfaceStateItems,
	)

	items = append(items, ownershipLiveItems(snap, gateway)...)
	for _, member := range gateway.Members {
		items = append(items, interfaceMemberLiveItems(snap, member)...)
	}

	groupBase := "/ietf-interfaces:interfaces/" + steeringPrefix + ":steering-group/state"

	for _, rejection := range rejections {
		base := groupBase + "/rejected-provider[interface='" + rejection.Interface + "']"
		items = append(
			items,
			yangpub.Item{Path: base + "/interface", Value: rejection.Interface},
			yangpub.Item{Path: base + "/reason", Value: rejection.Err.Error()},
		)
		if rejection.Provider != "" {
			items = append(items, yangpub.Item{Path: base + "/provider", Value: rejection.Provider})
		}
	}

	if snap.TierValid {
		items = append(
			items,
			yangpub.Item{
				Path:  groupBase + "/active-tier",
				Value: strconv.FormatUint(uint64(snap.ActiveTier), 10),
			},
		)
	}

	stale := !snap.BGP.PollSucceeded || now.Sub(snap.BGP.ReadAt) > bgpStaleAfter
	for _, peer := range snap.BGP.Peers {
		peerBase := groupBase + "/bgp-peer[address='" + peer.Address + "']"
		items = append(
			items,
			yangpub.Item{Path: peerBase + "/established", Value: boolValue(peer.Established)},
			yangpub.Item{Path: peerBase + "/stale", Value: boolValue(stale)},
		)
	}

	if snap.IntendedRuleset != "" {
		items = append(
			items,
			yangpub.Item{
				Path:  groupBase + "/intended-ruleset",
				Value: snap.IntendedRuleset,
			},
		)
	}

	return items
}

func interfaceMemberLiveItems(
	snapshot wanstate.Snapshot,
	member wanconfig.Member,
) []yangpub.Item {
	base := "/ietf-interfaces:interfaces/interface[name='" + member.Iface +
		"']/" + steeringPrefix + ":steering/state"
	selectionEnabled := config.ConnectionSelectionEnabled(member.SelectionEnabled)
	items := []yangpub.Item{
		{
			Path:  base + "/administratively-enabled",
			Value: boolValue(selectionEnabled),
		},
	}
	if health, known := snapshot.Health[member.Name]; known {
		items = append(
			items,
			yangpub.Item{Path: base + "/health", Value: string(health.Verdict)},
			yangpub.Item{
				Path:  base + "/consecutive-failures",
				Value: strconv.FormatUint(uint64(health.ConsecutiveFailures), 10),
			},
			yangpub.Item{
				Path:  base + "/probe[family='ipv4']/last-result",
				Value: string(health.V4),
			},
			yangpub.Item{
				Path:  base + "/probe[family='ipv6']/last-result",
				Value: string(health.V6),
			},
		)
		if !health.LastTransition.IsZero() {
			items = append(
				items,
				yangpub.Item{
					Path:  base + "/last-transition",
					Value: health.LastTransition.UTC().Format(time.RFC3339),
				},
			)
		}
	}

	if routing, known := snapshot.Routing[member.Name]; known {
		items = append(
			items,
			yangpub.Item{
				Path:  base + "/carrying",
				Value: boolValue(selectionEnabled && routing.Carrying),
			},
		)
		items = append(items, ownedAddressItems(member, routing.OwnedAddresses)...)
	}

	if translation, known := snapshot.Translation[member.Name]; known {
		if member.TranslationV4 != nil {
			items = append(items, translationLiveItems(member.Iface, "ipv4", translation.V4)...)
		}

		if member.TranslationV6 != nil {
			items = append(items, translationLiveItems(member.Iface, "ipv6", translation.V6)...)
		}
	}

	return items
}

// Operational state includes the mandatory WAN name with owned addresses.
// It uses the loaded interface name when the provider name is empty.
func ownedAddressItems(member wanconfig.Member, owned []netip.Addr) []yangpub.Item {
	if len(owned) == 0 {
		return nil
	}

	base := "/ietf-interfaces:interfaces/interface[name='" + member.Iface + "']/" + steeringPrefix + ":wan"
	items := make([]yangpub.Item, 0, len(owned)+1)
	providerName := member.ProviderName
	if providerName == "" {
		providerName = member.Name
	}

	items = append(items, yangpub.Item{Path: base + "/name", Value: providerName})

	for _, address := range owned {
		items = append(items, yangpub.Item{Path: base + "/owned-address", Value: address.String()})
	}

	return items
}

func translationLiveItems(
	iface string,
	family string,
	state wanstate.FamilyTranslation,
) []yangpub.Item {
	base := "/ietf-interfaces:interfaces/interface[name='" + iface +
		"']/ietf-ip:" + family + "/goodkind-mwan-steering:translation/state"
	items := []yangpub.Item{{Path: base + "/ready", Value: boolValue(state.Ready)}}
	if state.Reason != "" {
		items = append(items, yangpub.Item{Path: base + "/reason", Value: state.Reason})
	}

	if state.InternalPrefix.IsValid() {
		items = append(
			items,
			yangpub.Item{
				Path:  base + "/resolved-internal-prefix",
				Value: state.InternalPrefix.String(),
			},
		)
	}

	if state.ExternalPrefix.IsValid() {
		items = append(
			items,
			yangpub.Item{
				Path:  base + "/resolved-external-prefix",
				Value: state.ExternalPrefix.String(),
			},
		)
	}

	return items
}

func natLiveItems(snap wanstate.Snapshot, gateway wanconfig.Gateway) []yangpub.Item {
	var items []yangpub.Item

	for _, member := range gateway.Members {
		state, known := snap.Translation[member.Name]
		if !known {
			continue
		}

		if member.TranslationV4 != nil && member.TranslationV4.Mode != config.TranslationNative {
			items = append(
				items,
				yangpub.Item{
					Path: fmt.Sprintf(
						"/ietf-nat:nat/instances/instance[id='%d']/%s:kernel-present",
						member.TranslationIDV4,
						steeringPrefix,
					),
					Value: boolValue(state.V4.Ready),
				},
			)
		}

		if member.TranslationV6 != nil && member.TranslationV6.Mode == config.TranslationNPTv6 {
			base := fmt.Sprintf("/ietf-nat:nat/instances/instance[id='%d']", member.TranslationIDV6)
			items = append(
				items,
				yangpub.Item{
					Path:  base + "/" + steeringPrefix + ":kernel-present",
					Value: boolValue(state.V6.Ready),
				},
			)
			if member.TranslationV6.NPT != nil &&
				member.TranslationV6.NPT.ExternalSource == config.PrefixDelegated &&
				state.V6.Ready &&
				state.V6.InternalPrefix.IsValid() &&
				state.V6.ExternalPrefix.IsValid() &&
				state.V6.InternalPrefix.Addr().Is6() &&
				state.V6.ExternalPrefix.Addr().Is6() &&
				state.V6.InternalPrefix.Bits() == state.V6.ExternalPrefix.Bits() {
				path := base + "/policy[id='1']/nptv6-prefixes[internal-ipv6-prefix='" + state.V6.InternalPrefix.String() + "']"
				items = append(
					items,
					yangpub.Item{
						Path:  path + "/external-ipv6-prefix",
						Value: state.V6.ExternalPrefix.String(),
					},
				)
			}
		}
	}

	return items
}

func boolValue(value bool) string {
	if value {
		return "true"
	}

	return "false"
}

// Poll BGP outside reconciliation and provider callbacks.
// A failed poll preserves the last successful snapshot and its timestamp.
func pollAgentBGP(
	ctx context.Context,
	log *slog.Logger,
	cfg *config.Config,
	store *wanstate.Store,
) {
	target, err := agentDialTarget(cfg.Agent.TCPAddr)
	if err != nil {
		log.WarnContext(
			ctx,
			"wanconfig: agent address unusable; serving no routing-session state",
			"tcp_addr",
			cfg.Agent.TCPAddr,
			"err",
			err,
		)

		return
	}

	ticker := time.NewTicker(bgpPollInterval)
	defer ticker.Stop()
	lastDropped := ""

	for {
		dropped := pollOnce(ctx, log, target, store)
		// Skip peer addresses that libyang cannot use as list keys. Log each distinct set of
		// invalid addresses once.
		if dropped != lastDropped {
			if dropped != "" {
				log.WarnContext(
					ctx,
					"wanconfig: agent reported peers without a usable address; not served",
					"addresses",
					dropped,
				)
			}

			lastDropped = dropped
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Use loopback for empty and wildcard listen hosts.
func agentDialTarget(listenAddr string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		slog.Warn("wanconfig: agent tcp_addr unparsable", "tcp_addr", listenAddr, "err", err)

		return "", fmt.Errorf("split agent tcp_addr %q: %w", listenAddr, err)
	}

	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "::1"
	}

	return net.JoinHostPort(host, port), nil
}

func pollOnce(ctx context.Context, log *slog.Logger, target string, store *wanstate.Store) string {
	pollCtx, cancel := context.WithTimeout(ctx, bgpPollTimeout)
	defer cancel()

	conn, err := grpc.NewClient(
		"passthrough:///"+target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(dialCtx context.Context, _ string) (net.Conn, error) {
			var dialer net.Dialer

			return dialer.DialContext(dialCtx, "tcp", target)
		}),
	)
	if err != nil {
		log.WarnContext(pollCtx, "wanconfig: agent client build failed", "err", err)

		return ""
	}

	defer func() { _ = conn.Close() }()

	resp, err := mwanv1.NewMWANAgentClient(conn).GetBGPStatus(
		pollCtx,
		&mwanv1.GetBGPStatusRequest{},
	)
	if err != nil {
		log.DebugContext(pollCtx, "wanconfig: agent bgp poll failed", "err", err)

		return ""
	}

	peers, dropped := usablePeers(resp.GetPeers())
	store.SetBGP(wanstate.BGP{
		Peers:         peers,
		ReadAt:        clock.Real{}.Now(),
		PollSucceeded: true,
	})

	return strings.Join(dropped, ",")
}

// The schema requires a valid IP address for each peer key.
func usablePeers(reported []*mwanv1.BGPPeerStatus) ([]wanstate.BGPPeer, []string) {
	peers := make([]wanstate.BGPPeer, 0, len(reported))
	dropped := make([]string, 0)

	for _, peer := range reported {
		address, err := netip.ParseAddr(peer.GetAddress())
		if err != nil {
			dropped = append(dropped, peer.GetAddress())

			continue
		}

		peers = append(
			peers,
			wanstate.BGPPeer{
				Address:     address.String(),
				Established: peer.GetEstablished(),
			},
		)
	}

	return peers, dropped
}
