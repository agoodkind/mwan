package netif

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

const nptEdgeScope = "npt-edge"

// NPTEdgeRequest selects one configured provider edge without acquiring ordinary addresses.
type NPTEdgeRequest struct {
	ConnectionID connectionid.ID
	Interface    string
	Prefix       netip.Prefix
}

// NPTEdgeRecord records the verified link identity for one scoped provider edge.
type NPTEdgeRecord struct {
	ConnectionID   connectionid.ID `json:"connection_id"`
	Interface      string          `json:"interface"`
	InterfaceIndex int             `json:"interface_index"`
	LinkIdentity   string          `json:"link_identity"`
	Prefix         netip.Prefix    `json:"prefix"`
}

// ObserveLegacyLink verifies configured identity without applying link settings.
func ObserveLegacyLink(connection interfaceintent.Connection) (result netlink.Link, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("legacy link identity inspection failed", "connection", connection.ID, "err", resultErr)
		}
	}()
	link, err := netlink.LinkByName(connection.Name)
	if err != nil {
		return nil, fmt.Errorf("observe legacy link %s: %w", connection.Name, err)
	}
	if connection.Link != nil && (connection.Link.Kind == interfaceintent.KindPhysical || connection.Link.Kind == "") && connection.Link.Match.HardwareAddress != "" {
		matched, err := physicalLink(connection)
		if err != nil {
			return nil, err
		}
		if matched == nil || matched.Attrs().Index != link.Attrs().Index || matched.Attrs().Name != connection.Name {
			return nil, fmt.Errorf("legacy connection %s permanent MAC does not match configured name %s", connection.ID, connection.Name)
		}
	}
	if connection.Link != nil && connection.Link.Match.Driver != "" {
		driver := link.Type()
		if driver != "veth" {
			path, err := filepath.EvalSymlinks(filepath.Join("/sys/class/net", connection.Name, "device/driver"))
			if err != nil {
				return nil, fmt.Errorf("observe legacy driver for %s: %w", connection.ID, err)
			}
			driver = filepath.Base(path)
		}
		if driver != connection.Link.Match.Driver {
			return nil, fmt.Errorf("legacy connection %s driver does not match", connection.ID)
		}
	}
	if connection.Link != nil && connection.Link.HardwareAddress != "" && !strings.EqualFold(link.Attrs().HardwareAddr.String(), connection.Link.HardwareAddress) {
		return nil, fmt.Errorf("legacy connection %s configured MAC does not match", connection.ID)
	}
	return link, nil
}

func nptEdgeObject(record NPTEdgeRecord) ownedStaticObject {
	return ownedStaticObject{Scope: nptEdgeScope, ConnectionID: record.ConnectionID.String(), Family: "ipv6", LinkName: record.Interface, LinkIndex: record.InterfaceIndex, LinkIdentity: record.LinkIdentity, Prefix: record.Prefix.String(), Destination: "", Gateway: "", Metric: 0}
}

// EnsureNPTEdge reserves ownership before installation and verifies kernel readiness.
func (r *OwnedStaticReconciler) EnsureNPTEdge(request NPTEdgeRequest, link netlink.Link) (result NPTEdgeRecord, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPT edge installation failed", "connection", request.ConnectionID, "err", resultErr)
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	record := NPTEdgeRecord{ConnectionID: request.ConnectionID, Interface: request.Interface, InterfaceIndex: link.Attrs().Index, LinkIdentity: LinkOwnershipIdentity(link), Prefix: request.Prefix}
	if !request.Prefix.IsValid() || !request.Prefix.Addr().Is6() || request.Prefix.Bits() != 128 || !request.Prefix.Addr().IsGlobalUnicast() || request.Interface != link.Attrs().Name {
		return NPTEdgeRecord{}, fmt.Errorf("invalid NPT edge request for %s", request.ConnectionID)
	}
	value := nptEdgeObject(record)
	for _, old := range r.journal.Objects {
		if staticObjectKey(old) == staticObjectKey(value) && old != value {
			return NPTEdgeRecord{}, fmt.Errorf("recorded NPT edge identity changed for %s", request.ConnectionID)
		}
	}
	if err := r.ensureAddress(link, nptEdgeObject(record), OwnedAddressLifetime{PreferredUntil: time.Time{}, ValidUntil: time.Time{}}); err != nil {
		return NPTEdgeRecord{}, err
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		return NPTEdgeRecord{}, fmt.Errorf("read NPT edge %s: %w", request.Prefix, err)
	}
	for _, address := range addresses {
		if addressMatches(address, request.Prefix) && address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
			return record, nil
		}
	}
	return NPTEdgeRecord{}, fmt.Errorf("NPT edge %s is not ready on %s", request.Prefix, request.Interface)
}

// RecordedNPTEdges excludes ordinary acquisition receipts.
func (r *OwnedStaticReconciler) RecordedNPTEdges() []NPTEdgeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var records []NPTEdgeRecord
	for _, value := range r.journal.Objects {
		if value.Scope != nptEdgeScope {
			continue
		}
		id := connectionid.ID(value.ConnectionID)
		prefix, err := netip.ParsePrefix(value.Prefix)
		if err != nil {
			continue
		}
		records = append(records, NPTEdgeRecord{ConnectionID: id, Interface: value.LinkName, InterfaceIndex: value.LinkIndex, LinkIdentity: value.LinkIdentity, Prefix: prefix})
	}
	return records
}

// ReleaseNPTEdge verifies the same boot and exact receipt before kernel deletion.
func (r *OwnedStaticReconciler) ReleaseNPTEdge(record NPTEdgeRecord) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPT edge release failed", "connection", record.ConnectionID, "err", resultErr)
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	value := nptEdgeObject(record)
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return fmt.Errorf("verify NPT edge boot identity: %w", err)
	}
	if string(bootID) != r.journal.BootID {
		return fmt.Errorf("NPT edge boot identity changed")
	}
	if !slices.Contains(r.journal.Objects, value) {
		return fmt.Errorf("NPT edge %s has no exact ownership record", record.Prefix)
	}
	link, err := netlink.LinkByIndex(record.InterfaceIndex)
	if IsLinkNotFound(err) {
		return r.forget(value)
	}
	if err != nil {
		return fmt.Errorf("read recorded NPT edge link: %w", err)
	}
	if !LinkMatchesIdentity(link, record.InterfaceIndex, record.LinkIdentity) {
		return r.forget(value)
	}
	if link.Attrs().Name != record.Interface {
		return fmt.Errorf("recorded NPT edge link %s was renamed to %s", record.Interface, link.Attrs().Name)
	}
	if err := removeStaticAddress(link, value); err != nil {
		return err
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		return fmt.Errorf("verify NPT edge deletion: %w", err)
	}
	for _, address := range addresses {
		if addressMatches(address, record.Prefix) {
			return fmt.Errorf("NPT edge %s remains on %s", record.Prefix, record.Interface)
		}
	}
	return r.forget(value)
}
