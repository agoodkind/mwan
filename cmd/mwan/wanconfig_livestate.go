package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

const (
	// bgpPollInterval paces the agent poller. The provider marks the
	// answer stale after bgpStaleAfter without a successful read, so a
	// dead agent yields an explicit stale marker rather than a hang.
	bgpPollInterval = 15 * time.Second
	bgpStaleAfter   = 3 * bgpPollInterval
	// bgpPollTimeout bounds one poll so a wedged agent cannot pile up
	// concurrent polls.
	bgpPollTimeout = 5 * time.Second

	steeringPrefix = "goodkind-mwan-steering"

	// moduleInterfaces, moduleNAT, and moduleSteering name the modules
	// whose subtrees the daemon publishes, provides live state for, and
	// owns.
	moduleInterfaces = "ietf-interfaces"
	moduleNAT        = "ietf-nat"
	moduleSteering   = steeringPrefix
)

// publishedModules lists the modules the daemon owns in the operational
// view, in the order they are claimed. The steering module carries the
// daemon container, so owning it makes those settings readable beside
// the live state.
var publishedModules = []string{moduleInterfaces, moduleNAT, moduleSteering}

// ownPublishedModules marks each published module's running data in use
// so its configuration appears in the operational datastore beside the
// live state. A failure degrades to state-only operational reads; the
// running datastore still serves the configuration.
func ownPublishedModules(ctx context.Context, log *slog.Logger, pub yangpub.Publisher) {
	for _, module := range publishedModules {
		if err := pub.OwnModule(ctx, module); err != nil {
			log.ErrorContext(ctx, "wanconfig: owning module failed; operational reads carry state only",
				"module", module, "err", err)
		}
	}
}

// registerLiveStateProviders registers the operational-datastore
// providers for the subtrees the daemon owns. Each read answers from the
// store's latest snapshot: bounded, lock-cheap, and independent of the
// reconcile path.
func registerLiveStateProviders(
	ctx context.Context,
	log *slog.Logger,
	pub yangpub.Publisher,
	store *wanstate.Store,
	gateway wanconfig.Gateway,
) error {
	interfacesProvider := func(_ context.Context, _ string) ([]yangpub.Item, error) {
		return interfacesLiveItems(store.Snapshot(), gateway), nil
	}
	if err := pub.RegisterProvider(ctx, moduleInterfaces, "/ietf-interfaces:interfaces", interfacesProvider); err != nil {
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
	return nil
}

// interfacesLiveItems renders the steering state, delegated prefixes, and
// group state from one snapshot. A value the daemon does not hold is left
// out rather than served empty.
func interfacesLiveItems(snap wanstate.Snapshot, gateway wanconfig.Gateway) []yangpub.Item {
	items := make([]yangpub.Item, 0, len(gateway.Members)*8+4)
	for _, member := range gateway.Members {
		base := "/ietf-interfaces:interfaces/interface[name='" + member.Iface +
			"']/" + steeringPrefix + ":steering/state"
		if health, known := snap.Health[member.Name]; known {
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
				items = append(items, yangpub.Item{
					Path:  base + "/last-transition",
					Value: health.LastTransition.UTC().Format(time.RFC3339),
				})
			}
		}
		if routing, known := snap.Routing[member.Name]; known {
			items = append(items, yangpub.Item{
				Path:  base + "/carrying",
				Value: boolValue(routing.Carrying),
			})
			items = append(items, ownedAddressItems(member, routing.OwnedAddresses)...)
		}
		if translation, known := snap.Translation[member.Name]; known && translation.Delegated.IsValid() {
			items = append(items, yangpub.Item{
				Path: "/ietf-interfaces:interfaces/interface[name='" + member.Iface +
					"']/ietf-ip:ipv6/" + steeringPrefix + ":delegated-prefix",
				Value: translation.Delegated.String(),
			})
		}
	}
	groupBase := "/ietf-interfaces:interfaces/" + steeringPrefix + ":steering-group/state"
	if snap.TierValid {
		items = append(items, yangpub.Item{
			Path:  groupBase + "/active-tier",
			Value: strconv.FormatUint(uint64(snap.ActiveTier), 10),
		})
	}
	stale := !snap.BGP.Reached || time.Since(snap.BGP.ReadAt) > bgpStaleAfter
	for _, peer := range snap.BGP.Peers {
		peerBase := groupBase + "/bgp-peer[address='" + peer.Address + "']"
		items = append(
			items,
			yangpub.Item{Path: peerBase + "/established", Value: boolValue(peer.Established)},
			yangpub.Item{Path: peerBase + "/stale", Value: boolValue(stale)},
		)
	}
	if snap.IntendedRuleset != "" {
		items = append(items, yangpub.Item{
			Path:  groupBase + "/intended-ruleset",
			Value: snap.IntendedRuleset,
		})
	}
	return items
}

// ownedAddressItems serves the addresses a member's link holds for its static
// mappings. The wan container is a presence container whose name leaf is
// mandatory. The configuration publish writes that container with its name,
// but a provider answers an operational read on its own, so the name is served
// beside the leaf-list rather than relying on the configuration to supply it.
// Leaf-list entries are addressed by value, the way the configuration publish
// addresses its own leaf-lists.
func ownedAddressItems(member wanconfig.Member, owned []netip.Addr) []yangpub.Item {
	if len(owned) == 0 {
		return nil
	}
	base := "/ietf-interfaces:interfaces/interface[name='" + member.Iface + "']/" + steeringPrefix + ":wan"
	items := make([]yangpub.Item, 0, len(owned)+1)
	items = append(items, yangpub.Item{Path: base + "/name", Value: member.Name})
	for _, address := range owned {
		items = append(items, yangpub.Item{Path: base + "/owned-address", Value: address.String()})
	}
	return items
}

// natLiveItems renders each translation instance's kernel presence, using
// the same instance numbering the configuration publish assigned:
// sequential ids over the translating members in member order.
func natLiveItems(snap wanstate.Snapshot, gateway wanconfig.Gateway) []yangpub.Item {
	items := make([]yangpub.Item, 0, len(gateway.Members))
	instanceID := 0
	for _, member := range gateway.Members {
		if !member.NPTInternal.IsValid() || !member.NPTExternal.IsValid() {
			continue
		}
		instanceID++
		translation, known := snap.Translation[member.Name]
		if !known {
			continue
		}
		items = append(items, yangpub.Item{
			Path: fmt.Sprintf("/ietf-nat:nat/instances/instance[id='%d']/%s:kernel-present",
				instanceID, steeringPrefix),
			Value: boolValue(translation.KernelPresent),
		})
	}
	return items
}

func boolValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// pollAgentBGP feeds the store's routing-session snapshot from the local
// agent over its gRPC surface. The speaker lives in the agent process,
// so this poller is the boundary between the two daemons; it runs
// outside every provider callback and every reconcile, and each poll is
// bounded. A failed poll records nothing, which lets the provider's
// staleness rule mark the last answer stale.
func pollAgentBGP(
	ctx context.Context,
	log *slog.Logger,
	cfg *config.Config,
	store *wanstate.Store,
) {
	target, err := agentDialTarget(cfg.Agent.TCPAddr)
	if err != nil {
		log.WarnContext(ctx, "wanconfig: agent address unusable; serving no routing-session state",
			"tcp_addr", cfg.Agent.TCPAddr, "err", err)
		return
	}
	ticker := time.NewTicker(bgpPollInterval)
	defer ticker.Stop()
	lastDropped := ""
	for {
		dropped := pollOnce(ctx, log, target, store)
		// A peer the agent reports without a usable address is dropped
		// rather than served: its address is the list key, and a key
		// libyang rejects would fail the whole subtree read. Log once per
		// change, not once per poll.
		if dropped != lastDropped {
			if dropped != "" {
				log.WarnContext(ctx, "wanconfig: agent reported peers without a usable address; not served",
					"addresses", dropped)
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

// agentDialTarget turns the agent's listen address into a dialable
// same-host target: a wildcard or empty host becomes the loopback.
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

// pollOnce reads the agent once and records the usable peers. It returns
// the addresses it dropped, joined for logging, so the caller can report
// a change without repeating itself every poll.
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

	resp, err := mwanv1.NewMWANAgentClient(conn).GetBGPStatus(pollCtx, &mwanv1.GetBGPStatusRequest{})
	if err != nil {
		log.DebugContext(pollCtx, "wanconfig: agent bgp poll failed", "err", err)
		return ""
	}
	peers, dropped := usablePeers(resp.GetPeers())
	store.SetBGP(wanstate.BGP{Peers: peers, ReadAt: time.Now(), Reached: true})
	return strings.Join(dropped, ",")
}

// usablePeers keeps the peers whose address parses as an IP address, the
// type the model's list key requires, and returns the raw addresses it
// dropped. The agent reports each session's address, so a drop here means
// the agent sent something that cannot be a key; serving it would fail
// the whole subtree read instead of one peer.
func usablePeers(reported []*mwanv1.BGPPeerStatus) ([]wanstate.BGPPeer, []string) {
	peers := make([]wanstate.BGPPeer, 0, len(reported))
	dropped := make([]string, 0)
	for _, peer := range reported {
		address, err := netip.ParseAddr(peer.GetAddress())
		if err != nil {
			dropped = append(dropped, peer.GetAddress())
			continue
		}
		peers = append(peers, wanstate.BGPPeer{
			Address:     address.String(),
			Established: peer.GetEstablished(),
		})
	}
	return peers, dropped
}
