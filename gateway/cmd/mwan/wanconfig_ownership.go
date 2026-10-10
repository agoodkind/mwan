package main

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

func ownershipLiveItems(snapshot wanstate.Snapshot, gateway wanconfig.Gateway) []yangpub.Item {
	var items []yangpub.Item
	for _, connection := range gateway.Connections {
		state, known := snapshot.Connections[connection.ID.String()]
		if !known {
			continue
		}
		base := "/ietf-interfaces:interfaces/interface[name='" + connection.Name + "']/goodkind-mwan-steering:ownership-state"
		items = append(
			items,
			yangpub.Item{Path: base + "/connection-id", Value: state.ID},
			yangpub.Item{Path: base + "/configured-owner", Value: string(state.Owner)},
			yangpub.Item{Path: base + "/link-state", Value: state.LinkState},
			yangpub.Item{Path: base + "/observation", Value: state.Observation},
		)
		if state.ActualName != "" {
			items = append(items, yangpub.Item{Path: base + "/actual-name", Value: state.ActualName})
		}
		if state.IfIndex > 0 {
			items = append(items, yangpub.Item{Path: base + "/actual-index", Value: strconv.Itoa(state.IfIndex)})
		}
		if !state.ObservedAt.IsZero() {
			items = append(items, yangpub.Item{Path: base + "/observed-at", Value: state.ObservedAt.UTC().Format(time.RFC3339Nano)})
		}
		if !state.ObservationAt.IsZero() {
			items = append(items, yangpub.Item{Path: base + "/observation-at", Value: state.ObservationAt.UTC().Format(time.RFC3339Nano)})
		}
		if state.ObservationReason != "" {
			items = append(items, yangpub.Item{Path: base + "/observation-reason", Value: state.ObservationReason})
		}
		if result := state.LastApply; result.Operation != "" {
			apply := base + "/last-apply"
			items = append(items, yangpub.Item{Path: apply + "/operation", Value: result.Operation}, yangpub.Item{Path: apply + "/result", Value: result.Result})
			if result.Dependency != "" {
				items = append(items, yangpub.Item{Path: apply + "/dependency", Value: result.Dependency})
			}
			if result.Reason != "" {
				items = append(items, yangpub.Item{Path: apply + "/reason", Value: result.Reason})
			}
			if !result.At.IsZero() {
				items = append(items, yangpub.Item{Path: apply + "/at", Value: result.At.UTC().Format(time.RFC3339Nano)})
			}
		}
		for _, transition := range state.Recent {
			path := base + "/recent-transition[id='" + transition.ID + "']"
			items = append(
				items,
				yangpub.Item{Path: path + "/id", Value: transition.ID},
				yangpub.Item{Path: path + "/at", Value: transition.At.UTC().Format(time.RFC3339Nano)},
				yangpub.Item{Path: path + "/family", Value: transition.Family},
				yangpub.Item{Path: path + "/previous", Value: transition.Previous},
				yangpub.Item{Path: path + "/current", Value: transition.Current},
				yangpub.Item{Path: path + "/operation", Value: transition.Operation},
				yangpub.Item{Path: path + "/dependency", Value: transition.Dependency},
				yangpub.Item{Path: path + "/reason", Value: transition.Reason},
			)
		}
		items = append(items, ownershipFamilyItems(connection.Name, "ipv4", state.IPv4, state.Observation)...)
		items = append(items, ownershipFamilyItems(connection.Name, "ipv6", state.IPv6, state.Observation)...)
		items = append(items, bgpSessionLiveItems(connection.Name, snapshot.BGPSessions[connection.ID.String()])...)
	}
	items = append(items, pendingRemovalItems(snapshot.PendingRemovals)...)
	return items
}

func pendingRemovalItems(pendingRemovals map[string]wanstate.PendingRemoval) []yangpub.Item {
	keys := make([]string, 0, len(pendingRemovals))
	for key := range pendingRemovals {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var items []yangpub.Item
	for _, key := range keys {
		pending := pendingRemovals[key]
		base := "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/state/pending-removal[id='" + key + "']"
		items = append(items,
			yangpub.Item{Path: base + "/id", Value: key},
			yangpub.Item{Path: base + "/connection-id", Value: pending.ConnectionID},
			yangpub.Item{Path: base + "/family", Value: pending.Family},
			yangpub.Item{Path: base + "/interface", Value: pending.Name},
			yangpub.Item{Path: base + "/result", Value: pending.Apply.Result},
			yangpub.Item{Path: base + "/reason", Value: pending.Apply.Reason},
			yangpub.Item{Path: base + "/at", Value: pending.Apply.At.UTC().Format(time.RFC3339Nano)},
		)
	}
	return items
}

func ownershipFamilyItems(name, family string, state wanstate.FamilyState, observation string) []yangpub.Item {
	base := "/ietf-interfaces:interfaces/interface[name='" + name + "']/ietf-ip:" + family + "/goodkind-mwan-steering:ownership-family-state"
	items := []yangpub.Item{
		{Path: base + "/acquisition", Value: state.Acquisition},
		{Path: base + "/assignment-validity", Value: state.AssignmentValid},
		{Path: base + "/lease-persistence", Value: state.LeasePersistence},
		{Path: base + "/firewall-protection", Value: state.Firewall},
		{Path: base + "/routing", Value: state.Routing},
		{Path: base + "/readiness", Value: state.Readiness},
	}
	if family == "ipv6" {
		items = append(items, yangpub.Item{Path: base + "/router-validity", Value: observedRARouterValidity(observation, state.Routes)})
	}
	items = append(items, ownershipFamilyApplyItems(base, state.LastApply)...)
	for _, assignment := range state.Assignments {
		id := assignmentIdentity(assignment)
		path := base + "/assignment[id='" + id + "']"
		items = append(
			items,
			yangpub.Item{Path: path + "/id", Value: id},
			yangpub.Item{Path: path + "/kind", Value: string(assignment.Kind)},
			yangpub.Item{Path: path + "/source", Value: assignment.Source},
			yangpub.Item{Path: path + "/valid", Value: boolValue(assignment.Valid)},
		)
		if assignment.Route == nil {
			items = append(items, yangpub.Item{Path: path + "/value", Value: assignment.Value.String()})
		} else {
			routePath := path + "/route"
			items = append(items,
				yangpub.Item{Path: routePath + "/destination", Value: assignment.Route.Destination.String()},
				yangpub.Item{Path: routePath + "/table-id", Value: strconv.FormatUint(uint64(assignment.Route.TableID), 10)},
				yangpub.Item{Path: routePath + "/metric", Value: strconv.FormatUint(uint64(assignment.Route.Metric), 10)},
			)
			if assignment.Route.Gateway.IsValid() {
				items = append(items, yangpub.Item{Path: routePath + "/gateway", Value: assignment.Route.Gateway.String()})
			}
		}
		if !assignment.AcquiredAt.IsZero() {
			items = append(items, yangpub.Item{Path: path + "/acquired-at", Value: assignment.AcquiredAt.UTC().Format(time.RFC3339Nano)})
		}
		for _, deadline := range []struct {
			name string
			at   *time.Time
		}{
			{"renew-at", assignment.RenewAt}, {"rebind-at", assignment.RebindAt}, {"preferred-until", assignment.PreferredUntil}, {"valid-until", assignment.ValidUntil},
		} {
			if deadline.at != nil {
				items = append(items, yangpub.Item{Path: path + "/" + deadline.name, Value: deadline.at.UTC().Format(time.RFC3339Nano)})
			}
		}
	}
	for _, address := range state.Addresses {
		id := stableIdentity(address.CIDR)
		path := base + "/observed-address[id='" + id + "']"
		items = append(items, observedAddressItems(path, id, family, observation, address)...)
	}
	for _, route := range state.Routes {
		id := routeIdentity(route)
		path := base + "/observed-route[id='" + id + "']"
		items = append(
			items,
			yangpub.Item{Path: path + "/id", Value: id},
			yangpub.Item{Path: path + "/destination", Value: route.Dest},
			yangpub.Item{Path: path + "/table-id", Value: strconv.Itoa(route.TableID)},
			yangpub.Item{Path: path + "/protocol", Value: strconv.Itoa(route.Protocol)},
			yangpub.Item{Path: path + "/metric", Value: strconv.Itoa(route.Metric)},
			yangpub.Item{Path: path + "/scope", Value: strconv.Itoa(route.Scope)},
			yangpub.Item{Path: path + "/route-type", Value: strconv.Itoa(route.Type)},
		)
		if route.Via != "" {
			items = append(items, yangpub.Item{Path: path + "/via", Value: route.Via})
		}
		if route.Dev != "" {
			items = append(items, yangpub.Item{Path: path + "/device", Value: route.Dev})
		}
		for _, hop := range route.NextHops {
			hopID := hopIdentity(hop)
			hopPath := path + "/next-hop[id='" + hopID + "']"
			items = append(
				items,
				yangpub.Item{Path: hopPath + "/id", Value: hopID},
				yangpub.Item{Path: hopPath + "/index", Value: strconv.Itoa(hop.LinkIndex)},
				yangpub.Item{Path: hopPath + "/weight", Value: strconv.Itoa(hop.Weight)},
			)
			if hop.Via != "" {
				items = append(items, yangpub.Item{Path: hopPath + "/via", Value: hop.Via})
			}
			if hop.Dev != "" {
				items = append(items, yangpub.Item{Path: hopPath + "/device", Value: hop.Dev})
			}
		}
	}
	return items
}

func observedAddressItems(path, id, family, observation string, address netif.CurrentAddr) []yangpub.Item {
	items := []yangpub.Item{
		{Path: path + "/id", Value: id},
		{Path: path + "/cidr", Value: address.CIDR},
		{Path: path + "/origin", Value: address.Origin},
		{Path: path + "/flags", Value: strconv.Itoa(address.Flags)},
		{Path: path + "/preferred-lifetime", Value: strconv.Itoa(address.PreferredLifetime)},
		{Path: path + "/valid-lifetime", Value: strconv.Itoa(address.ValidLifetime)},
	}
	if family == "ipv6" {
		phase := "unknown"
		if observation == "fresh" {
			phase = observedIPv6AddressPhase(address)
		}
		items = append(items, yangpub.Item{Path: path + "/phase", Value: phase})
	}
	return items
}

func observedIPv6AddressPhase(address netif.CurrentAddr) string {
	if address.Flags&netif.IFAFDADFailed != 0 {
		return "dad-failed"
	}
	if address.Flags&netif.IFAFTentative != 0 {
		return "tentative"
	}
	if address.ValidLifetime == 0 {
		return "invalid"
	}
	if address.Flags&netif.IFAFDeprecated != 0 || address.PreferredLifetime == 0 {
		return "deprecated"
	}
	return "usable"
}

func observedRARouterValidity(observation string, routes []netif.CurrentRoute) string {
	if observation != "fresh" {
		return "unknown"
	}
	for _, route := range routes {
		if route.Family == "inet6" && route.TableID == unix.RT_TABLE_MAIN &&
			route.Protocol == unix.RTPROT_RA && (route.Dest == "default" || route.Dest == "::/0") && route.Via != "" {
			return "present"
		}
	}
	return "absent"
}

func ownershipFamilyApplyItems(base string, result wanstate.ApplyResult) []yangpub.Item {
	if result.Result == "" {
		return nil
	}
	path := base + "/last-apply"
	return []yangpub.Item{
		{Path: path + "/operation", Value: result.Operation},
		{Path: path + "/dependency", Value: result.Dependency},
		{Path: path + "/result", Value: result.Result},
		{Path: path + "/reason", Value: result.Reason},
		{Path: path + "/at", Value: result.At.UTC().Format(time.RFC3339Nano)},
	}
}

func stableIdentity(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(strconv.Itoa(len(part)) + ":" + part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func assignmentIdentity(assignment interfaceintent.Assignment) string {
	iaID := "absent"
	if assignment.IAID != nil {
		iaID = strconv.FormatUint(uint64(*assignment.IAID), 10)
	}
	parts := []string{assignment.Family, string(assignment.Kind), assignment.Source, string(assignment.Purpose), assignment.Value.String(), assignment.ClientID, assignment.DUID, iaID}
	if assignment.Route != nil {
		parts = append(parts, assignment.Route.Destination.String(), assignment.Route.Gateway.String(), strconv.FormatUint(uint64(assignment.Route.TableID), 10), strconv.FormatUint(uint64(assignment.Route.Metric), 10))
	}
	return stableIdentity(parts...)
}

func hopIdentity(hop netif.RouteNextHop) string {
	return stableIdentity(strconv.Itoa(hop.LinkIndex), hop.Via, hop.Dev, strconv.Itoa(hop.Weight))
}

func routeIdentity(route netif.CurrentRoute) string {
	hops := make([]string, 0, len(route.NextHops))
	for _, hop := range route.NextHops {
		hops = append(hops, hopIdentity(hop))
	}
	slices.Sort(hops)
	parts := []string{route.Family, route.Dest, route.Via, route.Dev, strconv.Itoa(route.TableID), strconv.Itoa(route.Protocol), strconv.Itoa(route.Metric), strconv.Itoa(route.Scope), strconv.Itoa(route.Type)}
	parts = append(parts, hops...)
	return stableIdentity(parts...)
}
