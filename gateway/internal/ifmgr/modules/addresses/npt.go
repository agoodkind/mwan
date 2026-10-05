package addresses

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

// Ensure authorizes configured NPT intent independently of acquisition ownership.
func (module *Module) Ensure(ctx context.Context, log *slog.Logger, request ifmgr.NPTEdgeRequest) (record ifmgr.NPTEdgeRecord, resultErr error) {
	defer func() {
		if resultErr != nil {
			log.WarnContext(ctx, "addresses: NPT edge installation failed", "connection", request.ConnectionID, "err", resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return ifmgr.NPTEdgeRecord{}, fmt.Errorf("ensure NPT edge: %w", err)
	}
	for _, connection := range module.connections {
		if connection.ID != request.ConnectionID || connection.Name != request.Interface {
			continue
		}
		provider := module.providers[connection.ID.String()]
		if provider.IPv6 == nil || provider.IPv6.Mode != config.TranslationNPTv6 {
			return ifmgr.NPTEdgeRecord{}, fmt.Errorf("connection %s has no NPT intent", connection.ID)
		}
		link, err := module.nptLink(log, connection)
		if err != nil {
			return ifmgr.NPTEdgeRecord{}, err
		}
		record, err := module.reconciler.EnsureNPTEdge(request, link)
		if err != nil {
			return record, fmt.Errorf("install authorized NPT edge: %w", err)
		}
		return record, nil
	}
	return ifmgr.NPTEdgeRecord{}, fmt.Errorf("NPT edge request does not match a configured connection")
}

func (module *Module) nptLink(log *slog.Logger, connection interfaceintent.Connection) (result netlink.Link, resultErr error) {
	defer func() {
		if resultErr != nil {
			log.Warn("addresses: NPT link inspection failed", "connection", connection.ID, "err", resultErr)
		}
	}()
	if connection.Owner != interfaceintent.OwnerMWAN {
		link, err := netif.ObserveLegacyLink(connection)
		if err != nil {
			return nil, fmt.Errorf("inspect configured legacy NPT link: %w", err)
		}
		return link, nil
	}
	if module.Env.OwnedLinks == nil {
		return nil, fmt.Errorf("NPT connection %s has no owned link result", connection.ID)
	}
	ready, ok := module.Env.OwnedLinks.Get(connection.ID.String())
	if !ok || ready.Status != netif.OwnedLinkReady || ready.ConnectionID != connection.ID.String() || ready.Name != connection.Name {
		return nil, fmt.Errorf("NPT connection %s link is not ready", connection.ID)
	}
	link, err := netlink.LinkByIndex(ready.IfIndex)
	if err != nil {
		return nil, fmt.Errorf("read NPT owned link: %w", err)
	}
	if link.Attrs().Name != ready.ActualName {
		return nil, fmt.Errorf("NPT connection %s link identity changed", connection.ID)
	}
	return link, nil
}

// Recorded returns only separately scoped NPT producer receipts.
func (module *Module) Recorded() []ifmgr.NPTEdgeRecord {
	return module.reconciler.RecordedNPTEdges()
}

// RetainDuringRecovery authorizes an existing edge while its own cached delegation remains valid.
func (module *Module) RetainDuringRecovery(record ifmgr.NPTEdgeRecord) bool {
	for _, connection := range module.connections {
		if connection.ID != record.ConnectionID || connection.Name != record.Interface || connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		provider := module.providers[connection.ID.String()]
		if provider.IPv6 == nil || provider.IPv6.Mode != config.TranslationNPTv6 || provider.IPv6.NPT == nil || provider.IPv6.NPT.ExternalSource != config.PrefixDelegated {
			return false
		}
		module.sessionMu.Lock()
		defer module.sessionMu.Unlock()
		session := module.dhcpv6Sessions[connection.ID.String()]
		if session == nil || !session.recoveryPending || session.cachedLease == nil || module.Env.OwnedLinks == nil {
			return false
		}
		ready, present := module.Env.OwnedLinks.Get(connection.ID.String())
		if !present || ready.Status != netif.OwnedLinkReady || ready.ConnectionID != session.ready.ConnectionID || ready.Name != session.ready.Name || ready.IfIndex != session.ready.IfIndex || ready.ActualName != session.ready.ActualName || ready.IfIndex != record.InterfaceIndex || ready.ActualName != record.Interface || dhcpv6LinkIdentity(connection, ready, true) != session.identity {
			return false
		}
		link, err := netlink.LinkByIndex(record.InterfaceIndex)
		if err != nil || link.Attrs().Name != record.Interface || !netif.LinkMatchesIdentity(link, record.InterfaceIndex, record.LinkIdentity) {
			return false
		}
		now := module.clock.Now()
		for _, prefix := range session.cachedLease.Prefixes {
			if prefix.Prefix.Contains(record.Prefix.Addr()) && now.Before(prefix.ValidUntil) {
				return true
			}
		}
		return false
	}
	return false
}

// Release removes exact scoped receipts after the caller verifies translation removal.
func (module *Module) Release(ctx context.Context, log *slog.Logger, records []ifmgr.NPTEdgeRecord) (resultErr error) {
	defer func() {
		if resultErr != nil {
			log.WarnContext(ctx, "addresses: NPT edge release failed", "err", resultErr)
		}
	}()
	var failures error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, fmt.Errorf("release NPT edge: %w", err))
		}
		if err := module.reconciler.ReleaseNPTEdge(record); err != nil {
			failures = errors.Join(failures, fmt.Errorf("release NPT edge %s: %w", record.Prefix, err))
		}
	}
	return failures
}
