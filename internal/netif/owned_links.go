package netif

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/installfile"
	"goodkind.io/mwan/internal/interfaceintent"
)

const ownedAliasPrefix = "mwan-link:"

// OwnedLinkStatus reports the kernel reconciliation state of one connection.
type OwnedLinkStatus string

const (
	// OwnedLinkReady means the configured link exists with applied settings.
	OwnedLinkReady OwnedLinkStatus = "ready"
	// OwnedLinkWaiting means a required kernel link is absent.
	OwnedLinkWaiting OwnedLinkStatus = "waiting"
	// OwnedLinkPendingRename means a physical link needs a boot-time rename.
	OwnedLinkPendingRename OwnedLinkStatus = "pending-rename"
	// OwnedLinkFailed means the requested operation failed.
	OwnedLinkFailed OwnedLinkStatus = "failed"
	// OwnedLinkRemoved means a stale owned virtual link was deleted.
	OwnedLinkRemoved OwnedLinkStatus = "removed"
)

// OwnedLinkResult records one connection's observed kernel operation.
type OwnedLinkResult struct {
	ConnectionID string
	Name         string
	ActualName   string
	IfIndex      int
	Status       OwnedLinkStatus
	Dependency   string
	Operation    string
	Err          error
}

func newOwnedLinkResult(id string, name string, status OwnedLinkStatus) OwnedLinkResult {
	return OwnedLinkResult{
		ConnectionID: id, Name: name, ActualName: "", IfIndex: 0,
		Status: status, Dependency: "", Operation: "", Err: nil,
	}
}

type virtualRecord struct {
	BootID       string `json:"boot_id"`
	ConnectionID string `json:"connection_id"`
	Alias        string `json:"alias"`
	TempName     string `json:"temp_name"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	VLANID       uint16 `json:"vlan_id,omitempty"`
	Parent       string `json:"parent,omitempty"`
	ParentMAC    string `json:"parent_mac,omitempty"`
	ParentBoot   string `json:"parent_boot,omitempty"`
	ParentKind   string `json:"parent_kind,omitempty"`
	ParentName   string `json:"parent_name,omitempty"`
	ParentIndex  int    `json:"parent_index,omitempty"`
	LinkIndex    int    `json:"link_index,omitempty"`
	Complete     bool   `json:"complete"`
	Quarantined  bool   `json:"quarantined,omitempty"`
}

type membershipRecord struct {
	SlaveAlias  string `json:"slave_alias,omitempty"`
	SlaveMAC    string `json:"slave_mac,omitempty"`
	MasterAlias string `json:"master_alias,omitempty"`
	MasterName  string `json:"master_name"`
	MasterBoot  string `json:"master_boot"`
	MasterIndex int    `json:"master_index"`
}

func (r *OwnedLinkReconciler) matchesRecordedMaster(master netlink.Link, record membershipRecord) bool {
	if record.MasterAlias != "" {
		return record.MasterBoot == r.bootID && master.Attrs().Index == record.MasterIndex &&
			master.Attrs().Alias == record.MasterAlias
	}
	return record.MasterBoot == r.bootID && master.Attrs().Index == record.MasterIndex &&
		master.Attrs().Alias == "" && master.Attrs().Name == record.MasterName
}

type ownedLinkState struct {
	Virtuals    map[string]virtualRecord    `json:"virtuals"`
	Memberships map[string]membershipRecord `json:"memberships"`
}

// OwnedLinkReconciler serializes link mutations and persists their ownership records.
type OwnedLinkReconciler struct {
	mu        sync.Mutex
	statePath string
	bootID    string
	state     ownedLinkState
}

// NewOwnedLinkReconciler reads the durable ownership journal at statePath.
func NewOwnedLinkReconciler(statePath string) (*OwnedLinkReconciler, error) {
	if statePath == "" {
		return nil, errors.New("owned link state path is empty")
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, fmt.Errorf("read boot ID: %w", err)
	}
	r := &OwnedLinkReconciler{
		mu: sync.Mutex{}, statePath: statePath, bootID: strings.TrimSpace(string(bootID)),
		state: ownedLinkState{Virtuals: make(map[string]virtualRecord), Memberships: make(map[string]membershipRecord)},
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		slog.Warn("owned link state read failed", "path", statePath, "err", err)
		return nil, fmt.Errorf("read owned link state: %w", err)
	}
	if err := json.Unmarshal(data, &r.state); err != nil {
		return nil, fmt.Errorf("decode owned link state: %w", err)
	}
	if r.state.Virtuals == nil {
		r.state.Virtuals = make(map[string]virtualRecord)
	}
	if r.state.Memberships == nil {
		r.state.Memberships = make(map[string]membershipRecord)
	}
	return r, nil
}

// save writes and syncs the journal directory before any kernel mutation.
func (r *OwnedLinkReconciler) save() error {
	data, err := json.Marshal(r.state)
	if err != nil {
		slog.Warn("owned link state encode failed", "path", r.statePath, "err", err)
		return fmt.Errorf("encode owned link state: %w", err)
	}
	if _, err := installfile.Write(r.statePath, append(data, '\n'), 0o600); err != nil {
		slog.Warn("owned link state write failed", "path", r.statePath, "err", err)
		return fmt.Errorf("write owned link state: %w", err)
	}
	directory, err := os.Open(filepath.Dir(r.statePath))
	if err != nil {
		slog.Warn("owned link state directory open failed", "path", r.statePath, "err", err)
		return fmt.Errorf("open owned link state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		slog.Warn("owned link state directory sync failed", "path", r.statePath, "err", err)
		return fmt.Errorf("sync owned link state directory: %w", err)
	}
	return nil
}

func aliasFor(id string) string { return ownedAliasPrefix + id }

func isAliasForConnection(alias string, id string) bool {
	base := aliasFor(id)
	return alias == base || strings.HasPrefix(alias, base+":")
}

// Reconcile applies MWAN-owned link intent and reports each connection separately.
func (r *OwnedLinkReconciler) Reconcile(ctx context.Context, log *slog.Logger, connections []interfaceintent.Connection) ([]OwnedLinkResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	byName := make(map[string]interfaceintent.Connection, len(connections))
	owned := make(map[string]interfaceintent.Connection)
	for _, connection := range connections {
		byName[connection.Name] = connection
		if connection.Owner == interfaceintent.OwnerMWAN {
			owned[connection.ID.String()] = connection
		}
	}
	var results []OwnedLinkResult
	resolved := make(map[string]OwnedLinkResult)
	visiting := make(map[string]bool)
	for _, connection := range connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		result := r.reconcileOne(ctx, connection, byName, resolved, visiting)
		if result.Err != nil && log != nil {
			log.WarnContext(ctx, "owned link failed", "connection", result.ConnectionID, "operation", result.Operation, "err", result.Err)
		}
		results = append(results, result)
	}
	removed, err := r.prune(ctx, owned)
	results = append(results, removed...)
	return results, err
}

func (r *OwnedLinkReconciler) reconcileOne(ctx context.Context, connection interfaceintent.Connection, byName map[string]interfaceintent.Connection, resolved map[string]OwnedLinkResult, visiting map[string]bool) OwnedLinkResult {
	id := connection.ID.String()
	if result, ok := resolved[id]; ok {
		return result
	}
	result := newOwnedLinkResult(id, connection.Name, OwnedLinkWaiting)
	if err := ctx.Err(); err != nil {
		result.Status, result.Err = OwnedLinkFailed, err
		return result
	}
	if visiting[id] {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf("link dependency cycle at %s", id)
		return result
	}
	visiting[id] = true
	defer delete(visiting, id)
	if connection.Link == nil {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf("connection %s has no link intent", id)
		resolved[id] = result
		return result
	}
	var parent netlink.Link
	if connection.Link.Kind == interfaceintent.KindVLAN {
		parent = r.vlanParent(ctx, connection, byName, resolved, visiting, &result)
		if parent == nil {
			resolved[id] = result
			return result
		}
	}
	var link netlink.Link
	var created bool
	var err error
	if connection.Link.Kind == interfaceintent.KindPhysical || connection.Link.Kind == "" {
		link, err = physicalLink(connection)
	} else {
		link, created, err = r.virtualLink(connection, parent)
	}
	if err != nil {
		result.Status, result.Err, result.Operation = OwnedLinkFailed, err, "resolve"
		resolved[id] = result
		return result
	}
	if link == nil {
		result.Operation = "wait-for-link"
		resolved[id] = result
		return result
	}
	result.ActualName, result.IfIndex = link.Attrs().Name, link.Attrs().Index
	if created {
		result.Operation = "create"
	} else {
		result.Operation = "reconcile"
	}
	if !r.reconcileMembership(ctx, connection, link, byName, resolved, visiting, &result) {
		resolved[id] = result
		return result
	}
	if err := applyLinkSettings(link, connection); err != nil {
		result.Status, result.Err, result.Operation = OwnedLinkFailed, err, "configure"
		resolved[id] = result
		return result
	}
	result.Status = OwnedLinkReady
	if result.ActualName != result.Name {
		result.Status = OwnedLinkPendingRename
	}
	resolved[id] = result
	return result
}

func (r *OwnedLinkReconciler) vlanParent(ctx context.Context, connection interfaceintent.Connection, byName map[string]interfaceintent.Connection, resolved map[string]OwnedLinkResult, visiting map[string]bool, result *OwnedLinkResult) netlink.Link {
	if connection.Link.VLAN == nil || connection.Link.VLAN.ID == 0 || connection.Link.VLAN.ID > 4094 {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf("connection %s has invalid VLAN intent", connection.ID)
		return nil
	}
	result.Dependency = connection.Link.VLAN.Parent
	parent, dependency := r.dependency(ctx, result.Dependency, byName, resolved, visiting)
	if parent != nil {
		return parent
	}
	result.Operation = "wait-for-parent"
	result.Err = fmt.Errorf("VLAN parent %s is not present", result.Dependency)
	if dependency.Err != nil {
		result.Err = dependency.Err
	}
	if dependency.Status == OwnedLinkFailed {
		result.Status, result.Operation = OwnedLinkFailed, "resolve-parent"
	}
	return nil
}

func (r *OwnedLinkReconciler) reconcileMembership(ctx context.Context, connection interfaceintent.Connection, link netlink.Link, byName map[string]interfaceintent.Connection, resolved map[string]OwnedLinkResult, visiting map[string]bool, result *OwnedLinkResult) bool {
	if connection.Link.BridgeMaster == "" {
		if err := r.detach(connection.ID.String()); err != nil {
			result.Status, result.Err, result.Operation = OwnedLinkFailed, err, "detach-bridge"
			return false
		}
		return true
	}
	result.Dependency = connection.Link.BridgeMaster
	master, dependency := r.dependency(ctx, result.Dependency, byName, resolved, visiting)
	if master == nil {
		result.Operation = "wait-for-bridge"
		result.Err = fmt.Errorf("bridge master %s is not present", result.Dependency)
		if dependency.Err != nil {
			result.Err = dependency.Err
		}
		if dependency.Status == OwnedLinkFailed {
			result.Status, result.Operation = OwnedLinkFailed, "resolve-bridge"
		}
		return false
	}
	if master.Type() != "bridge" {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf("%s is not a bridge", result.Dependency)
		return false
	}
	if err := r.attach(connection.ID.String(), link, master); err != nil {
		result.Status, result.Err, result.Operation = OwnedLinkFailed, err, "attach-bridge"
		return false
	}
	return true
}

func (r *OwnedLinkReconciler) dependency(ctx context.Context, name string, byName map[string]interfaceintent.Connection, resolved map[string]OwnedLinkResult, visiting map[string]bool) (netlink.Link, OwnedLinkResult) {
	connection, ok := byName[name]
	if !ok {
		result := newOwnedLinkResult("", name, OwnedLinkWaiting)
		result.Dependency = name
		return nil, result
	}
	if connection.Owner == interfaceintent.OwnerMWAN {
		result := r.reconcileOne(ctx, connection, byName, resolved, visiting)
		if result.Status != OwnedLinkReady && result.Status != OwnedLinkPendingRename {
			return nil, result
		}
		link, err := netlink.LinkByIndex(result.IfIndex)
		if err != nil {
			missing := newOwnedLinkResult("", name, OwnedLinkWaiting)
			missing.Err = err
			return nil, missing
		}
		return link, result
	}
	if connection.Link != nil && (connection.Link.Kind == interfaceintent.KindPhysical || connection.Link.Kind == "") {
		link, err := resolveConnectionLink(slog.Default(), connection)
		if err != nil {
			failed := newOwnedLinkResult("", name, OwnedLinkFailed)
			failed.Err = err
			return nil, failed
		}
		return link, newOwnedLinkResult("", name, OwnedLinkReady)
	}
	link, err := netlink.LinkByName(connection.Name)
	if IsLinkNotFound(err) {
		return nil, newOwnedLinkResult("", name, OwnedLinkWaiting)
	}
	if err != nil {
		failed := newOwnedLinkResult("", name, OwnedLinkFailed)
		failed.Err = err
		return nil, failed
	}
	if connection.Link != nil && connection.Link.Kind == interfaceintent.KindBridge && link.Type() != "bridge" {
		failed := newOwnedLinkResult("", name, OwnedLinkFailed)
		failed.Err = fmt.Errorf("%s is not a bridge", connection.Name)
		return nil, failed
	}
	return link, newOwnedLinkResult("", name, OwnedLinkReady)
}

func physicalLink(connection interfaceintent.Connection) (netlink.Link, error) {
	match := connection.Link.Match
	if match.HardwareAddress == "" {
		return nil, fmt.Errorf("physical connection %s lacks a permanent MAC match", connection.ID)
	}
	links, err := netlink.LinkList()
	if err != nil {
		slog.Warn("physical link listing failed", "connection_id", connection.ID.String(), "err", err)
		return nil, fmt.Errorf("list physical links: %w", err)
	}
	var found netlink.Link
	for _, link := range links {
		if link.Type() == "bridge" || link.Type() == "vlan" ||
			len(link.Attrs().PermHWAddr) == 0 ||
			!strings.EqualFold(link.Attrs().PermHWAddr.String(), match.HardwareAddress) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("physical connection %s matches multiple links", connection.ID)
		}
		found = link
	}
	return found, nil
}

func linkMAC(link netlink.Link) string {
	address := link.Attrs().PermHWAddr
	if len(address) == 0 {
		address = link.Attrs().HardwareAddr
	}
	return strings.ToLower(address.String())
}

func parentRecord(link netlink.Link) (string, string) {
	return link.Attrs().Alias, linkMAC(link)
}

func (r *OwnedLinkReconciler) matchesParentRecord(record virtualRecord, parent netlink.Link) bool {
	if record.ParentIndex != parent.Attrs().Index {
		return false
	}
	if record.ParentKind == "bridge" {
		if record.ParentBoot != r.bootID || parent.Type() != "bridge" || parent.Attrs().Alias != record.Parent {
			return false
		}
		return record.Parent != "" || parent.Attrs().Name == record.ParentName
	}
	alias, mac := parentRecord(parent)
	if record.Parent != "" {
		return alias == record.Parent
	}
	return record.ParentMAC != "" && mac == record.ParentMAC
}

func (r *OwnedLinkReconciler) matchesRecordedVirtual(link netlink.Link, record virtualRecord) bool {
	if !record.Complete || record.Alias == "" || link.Attrs().Alias != record.Alias ||
		record.BootID != r.bootID || record.LinkIndex == 0 ||
		link.Attrs().Index != record.LinkIndex || link.Type() != record.Kind {
		return false
	}
	if record.Kind == string(interfaceintent.KindBridge) {
		return true
	}
	if record.Kind != string(interfaceintent.KindVLAN) || link.Attrs().ParentIndex == 0 {
		return false
	}
	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(record.VLANID) || vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q {
		return false
	}
	parent, err := netlink.LinkByIndex(link.Attrs().ParentIndex)
	if err != nil {
		return false
	}
	return r.matchesParentRecord(record, parent)
}

func validateVirtual(link netlink.Link, connection interfaceintent.Connection, parent netlink.Link, alias string) error {
	if alias == "" || link.Attrs().Alias != alias {
		return fmt.Errorf("%s has a different ownership tag", link.Attrs().Name)
	}
	if connection.Link.Kind == interfaceintent.KindBridge {
		if link.Type() != "bridge" {
			return fmt.Errorf("%s is not a bridge", link.Attrs().Name)
		}
		return nil
	}
	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(connection.Link.VLAN.ID) || vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q || link.Attrs().ParentIndex != parent.Attrs().Index {
		return fmt.Errorf("%s has incompatible VLAN identity", link.Attrs().Name)
	}
	return nil
}

func validateTemporaryLink(link netlink.Link, connection interfaceintent.Connection, parent netlink.Link, alias string) error {
	if alias == "" || link.Attrs().Alias != "" && link.Attrs().Alias != alias {
		return fmt.Errorf("quarantined temporary link %s: foreign alias", link.Attrs().Name)
	}
	if connection.Link.Kind == interfaceintent.KindBridge {
		if link.Type() != "bridge" {
			return fmt.Errorf("quarantined temporary link %s: wrong kind", link.Attrs().Name)
		}
		return nil
	}
	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(connection.Link.VLAN.ID) || vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q ||
		link.Attrs().ParentIndex != parent.Attrs().Index {
		return fmt.Errorf("quarantined temporary link %s: wrong VLAN identity", link.Attrs().Name)
	}
	return nil
}

func (r *OwnedLinkReconciler) virtualLink(connection interfaceintent.Connection, parent netlink.Link) (netlink.Link, bool, error) {
	if connection.Link.Kind != interfaceintent.KindBridge && connection.Link.Kind != interfaceintent.KindVLAN {
		return nil, false, fmt.Errorf("unsupported link kind %s", connection.Link.Kind)
	}
	id := connection.ID.String()
	record := r.state.Virtuals[id]
	links, err := netlink.LinkList()
	if err != nil {
		slog.Warn("virtual link listing failed", "connection_id", id, "err", err)
		return nil, false, fmt.Errorf("list virtual links: %w", err)
	}
	var found netlink.Link
	for _, link := range links {
		if isAliasForConnection(link.Attrs().Alias, id) {
			if found != nil {
				return nil, false, fmt.Errorf("connection %s has multiple tagged links", id)
			}
			found = link
		}
		if link.Attrs().Name == connection.Name && (record.Alias == "" || link.Attrs().Alias != record.Alias) {
			return nil, false, fmt.Errorf("name %s is occupied by an unowned link", connection.Name)
		}
	}
	if found != nil {
		return r.adoptVirtualLink(found, connection, parent)
	}
	if record, ok := r.state.Virtuals[id]; ok && !record.Complete {
		return r.recoverVirtual(connection, parent, record)
	}
	if record, ok := r.state.Virtuals[id]; ok && record.Complete {
		delete(r.state.Virtuals, id)
		if err := r.save(); err != nil {
			return nil, false, err
		}
	}
	record, err = r.reserveVirtual(connection, parent, links)
	if err != nil {
		return nil, false, err
	}
	attrs := netlink.LinkAttrs{Name: record.TempName}
	var requested netlink.Link
	if connection.Link.Kind == interfaceintent.KindVLAN {
		attrs.ParentIndex = parent.Attrs().Index
		requested = &netlink.Vlan{LinkAttrs: attrs, VlanId: int(connection.Link.VLAN.ID), VlanProtocol: netlink.VLAN_PROTOCOL_8021Q}
	} else {
		requested = &netlink.Bridge{LinkAttrs: attrs}
	}
	if err := netlink.LinkAdd(requested); err != nil {
		return r.handleVirtualAddError(connection, parent, record, err)
	}
	return r.recoverVirtual(connection, parent, record)
}

func (r *OwnedLinkReconciler) reserveVirtual(connection interfaceintent.Connection, parent netlink.Link, links []netlink.Link) (virtualRecord, error) {
	id := connection.ID.String()
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		slog.Warn("temporary link name reservation failed", "connection_id", id, "err", err)
		return virtualRecord{}, fmt.Errorf("reserve temporary link name: %w", err)
	}
	record := virtualRecord{
		BootID: r.bootID, ConnectionID: id,
		Alias:    aliasFor(id) + ":" + hex.EncodeToString(nonce[:]),
		TempName: "mw-" + hex.EncodeToString(nonce[:]),
		Name:     connection.Name, Kind: string(connection.Link.Kind), VLANID: 0,
		Parent: "", ParentMAC: "", ParentBoot: "", ParentKind: "", ParentName: "",
		ParentIndex: 0, LinkIndex: 0,
		Complete: false, Quarantined: false,
	}
	if parent != nil {
		record.VLANID = connection.Link.VLAN.ID
		record.Parent, record.ParentMAC = parentRecord(parent)
		record.ParentBoot, record.ParentKind, record.ParentName = r.bootID, parent.Type(), parent.Attrs().Name
		record.ParentIndex = parent.Attrs().Index
	}
	for _, link := range links {
		if link.Attrs().Name == record.TempName {
			return virtualRecord{}, fmt.Errorf("temporary name %s is occupied", record.TempName)
		}
	}
	r.state.Virtuals[id] = record
	if err := r.save(); err != nil {
		delete(r.state.Virtuals, id)
		return virtualRecord{}, err
	}
	return record, nil
}

func (r *OwnedLinkReconciler) handleVirtualAddError(connection interfaceintent.Connection, parent netlink.Link, record virtualRecord, addErr error) (netlink.Link, bool, error) {
	slog.Warn("owned virtual link creation failed", "connection_id", record.ConnectionID,
		"temporary_name", record.TempName, "kind", record.Kind, "err", addErr)
	temporary, lookupErr := netlink.LinkByName(record.TempName)
	if IsLinkNotFound(lookupErr) {
		delete(r.state.Virtuals, record.ConnectionID)
		if saveErr := r.save(); saveErr != nil {
			return nil, false, errors.Join(addErr, saveErr)
		}
		return nil, false, fmt.Errorf("create %s: %w", record.TempName, addErr)
	}
	if lookupErr != nil {
		return nil, false, errors.Join(addErr, lookupErr)
	}
	if identityErr := validateTemporaryLink(temporary, connection, parent, record.Alias); identityErr != nil {
		record.Quarantined = true
		r.state.Virtuals[record.ConnectionID] = record
		if saveErr := r.save(); saveErr != nil {
			return nil, false, errors.Join(addErr, identityErr, saveErr)
		}
		return nil, false, errors.Join(addErr, identityErr)
	}
	return r.recoverVirtual(connection, parent, record)
}

func (r *OwnedLinkReconciler) adoptVirtualLink(found netlink.Link, connection interfaceintent.Connection, parent netlink.Link) (netlink.Link, bool, error) {
	id := connection.ID.String()
	recorded, ok := r.state.Virtuals[id]
	if !ok || recorded.ConnectionID != id || recorded.Alias == "" || found.Attrs().Alias != recorded.Alias {
		return nil, false, fmt.Errorf("tagged link %s lacks a matching ownership record", found.Attrs().Name)
	}
	if !recorded.Complete &&
		found.Attrs().Name == recorded.TempName {
		return r.recoverVirtual(connection, parent, recorded)
	}
	if !recorded.Complete && found.Attrs().Name == recorded.Name {
		if recorded.Quarantined || recorded.BootID != r.bootID || recorded.Kind != string(connection.Link.Kind) ||
			recorded.Name != connection.Name ||
			parent != nil && !r.matchesParentRecord(recorded, parent) ||
			connection.Link.Kind == interfaceintent.KindVLAN && recorded.VLANID != connection.Link.VLAN.ID {
			return nil, false, fmt.Errorf("renamed link %s differs from its reservation", found.Attrs().Name)
		}
		if err := validateVirtual(found, connection, parent, recorded.Alias); err != nil {
			return nil, false, err
		}
		recorded.LinkIndex = found.Attrs().Index
		recorded.Complete = true
		r.state.Virtuals[id] = recorded
		if err := r.save(); err != nil {
			return nil, false, err
		}
		return found, false, nil
	}
	if !r.matchesRecordedVirtual(found, recorded) {
		return nil, false, fmt.Errorf("tagged link %s differs from its ownership record", found.Attrs().Name)
	}
	if err := validateVirtual(found, connection, parent, recorded.Alias); err != nil {
		return nil, false, err
	}
	record := virtualRecord{
		BootID: r.bootID, ConnectionID: id, Alias: recorded.Alias, TempName: recorded.TempName,
		Name: connection.Name, Kind: string(connection.Link.Kind), VLANID: 0,
		Parent: "", ParentMAC: "", ParentBoot: "", ParentKind: "", ParentName: "",
		ParentIndex: 0, LinkIndex: found.Attrs().Index,
		Complete: true, Quarantined: false,
	}
	if parent != nil {
		record.VLANID = connection.Link.VLAN.ID
		record.Parent, record.ParentMAC = parentRecord(parent)
		record.ParentBoot, record.ParentKind, record.ParentName = r.bootID, parent.Type(), parent.Attrs().Name
		record.ParentIndex = parent.Attrs().Index
	}
	r.state.Virtuals[id] = record
	if err := r.save(); err != nil {
		return nil, false, err
	}
	if found.Attrs().Name != connection.Name {
		if err := netlink.LinkSetName(found, connection.Name); err != nil {
			slog.Warn("owned virtual link rename failed", "connection_id", id,
				"current_name", found.Attrs().Name, "desired_name", connection.Name, "err", err)
			return nil, false, fmt.Errorf("rename owned link %s: %w", found.Attrs().Name, err)
		}
		var err error
		found, err = netlink.LinkByName(connection.Name)
		if err != nil {
			return nil, false, fmt.Errorf("refresh renamed link %s: %w", connection.Name, err)
		}
		if err := validateVirtual(found, connection, parent, record.Alias); err != nil {
			return nil, false, err
		}
	}
	return found, false, nil
}

func (r *OwnedLinkReconciler) recoverVirtual(connection interfaceintent.Connection, parent netlink.Link, record virtualRecord) (netlink.Link, bool, error) {
	if record.Quarantined || record.Alias == "" || record.BootID != r.bootID || record.ConnectionID != connection.ID.String() ||
		record.Name != connection.Name || record.Kind != string(connection.Link.Kind) {
		return nil, false, fmt.Errorf("quarantined temporary link %s: journal identity differs", record.TempName)
	}
	if parent != nil && !r.matchesParentRecord(record, parent) {
		return nil, false, fmt.Errorf("quarantined temporary link %s: parent identity differs", record.TempName)
	}
	if connection.Link.Kind == interfaceintent.KindVLAN && record.VLANID != connection.Link.VLAN.ID {
		return nil, false, fmt.Errorf("quarantined temporary link %s: VLAN tag differs", record.TempName)
	}
	link, err := netlink.LinkByName(record.TempName)
	if IsLinkNotFound(err) {
		delete(r.state.Virtuals, connection.ID.String())
		if err := r.save(); err != nil {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("reserved temporary link %s is absent; retry reconciliation", record.TempName)
	}
	if err != nil {
		return nil, false, fmt.Errorf("find reserved link %s: %w", record.TempName, err)
	}
	if err := validateTemporaryLink(link, connection, parent, record.Alias); err != nil {
		return nil, false, err
	}
	if link.Attrs().Alias == "" {
		if err := netlink.LinkSetAlias(link, record.Alias); err != nil {
			return nil, false, fmt.Errorf("tag reserved link %s: %w", record.TempName, err)
		}
	}
	link, err = netlink.LinkByName(record.TempName)
	if err != nil {
		return nil, false, fmt.Errorf("verify reserved link alias: %w", err)
	}
	if link.Attrs().Alias != record.Alias {
		return nil, false, fmt.Errorf("reserved link %s has incorrect alias", record.TempName)
	}
	if err := netlink.LinkSetName(link, connection.Name); err != nil {
		slog.Warn("temporary owned link rename failed", "connection_id", connection.ID.String(),
			"temporary_name", record.TempName, "desired_name", connection.Name, "err", err)
		return nil, false, fmt.Errorf("rename temporary link: %w", err)
	}
	link, err = netlink.LinkByName(connection.Name)
	if err != nil {
		return nil, false, fmt.Errorf("refresh renamed link: %w", err)
	}
	if err := validateVirtual(link, connection, parent, record.Alias); err != nil {
		return nil, false, err
	}
	record.Complete = true
	record.LinkIndex = link.Attrs().Index
	r.state.Virtuals[connection.ID.String()] = record
	if err := r.save(); err != nil {
		return nil, true, err
	}
	return link, true, nil
}

func applyLinkSettings(link netlink.Link, connection interfaceintent.Connection) error {
	settings := connection.Link
	if settings.MTU != nil && link.Attrs().MTU != int(*settings.MTU) {
		if err := netlink.LinkSetMTU(link, int(*settings.MTU)); err != nil {
			slog.Warn("link MTU update failed", "link", link.Attrs().Name, "mtu", *settings.MTU, "err", err)
			return fmt.Errorf("set MTU on %s: %w", link.Attrs().Name, err)
		}
	}
	if settings.HardwareAddress != "" {
		address, err := net.ParseMAC(settings.HardwareAddress)
		if err != nil {
			slog.Warn("configured link MAC is invalid", "link", link.Attrs().Name, "err", err)
			return fmt.Errorf("parse configured MAC: %w", err)
		}
		if !strings.EqualFold(link.Attrs().HardwareAddr.String(), address.String()) {
			if err := netlink.LinkSetHardwareAddr(link, address); err != nil {
				slog.Warn("link MAC update failed", "link", link.Attrs().Name, "err", err)
				return fmt.Errorf("set MAC on %s: %w", link.Attrs().Name, err)
			}
		}
	}
	return applyLinkEnabled(link, connection.Enabled)
}

func applyLinkEnabled(link netlink.Link, enabled *bool) error {
	if enabled == nil || *enabled == (link.Attrs().Flags&net.FlagUp != 0) {
		return nil
	}
	if *enabled {
		if err := netlink.LinkSetUp(link); err != nil {
			slog.Warn("link enable failed", "link", link.Attrs().Name, "err", err)
			return fmt.Errorf("raise link %s: %w", link.Attrs().Name, err)
		}
		return nil
	}
	if err := netlink.LinkSetDown(link); err != nil {
		slog.Warn("link disable failed", "link", link.Attrs().Name, "err", err)
		return fmt.Errorf("lower link %s: %w", link.Attrs().Name, err)
	}
	return nil
}

func (r *OwnedLinkReconciler) attach(id string, slave netlink.Link, master netlink.Link) error {
	if slave.Attrs().MasterIndex == master.Attrs().Index {
		return nil
	}
	if slave.Attrs().MasterIndex != 0 {
		record, recorded := r.state.Memberships[id]
		if !recorded {
			return fmt.Errorf("%s has an unrecorded bridge membership", slave.Attrs().Name)
		}
		prior, err := netlink.LinkByIndex(slave.Attrs().MasterIndex)
		if err != nil {
			slog.Warn("recorded bridge master lookup failed", "link", slave.Attrs().Name, "err", err)
			return fmt.Errorf("find recorded bridge master: %w", err)
		}
		if !r.matchesRecordedMaster(prior, record) {
			return fmt.Errorf("%s has a bridge membership outside its ownership record", slave.Attrs().Name)
		}
		if err := r.detach(id); err != nil {
			return err
		}
	}
	record := membershipRecord{
		SlaveAlias: "", SlaveMAC: "", MasterAlias: master.Attrs().Alias,
		MasterName: master.Attrs().Name, MasterBoot: r.bootID,
		MasterIndex: master.Attrs().Index,
	}
	if strings.HasPrefix(slave.Attrs().Alias, ownedAliasPrefix) {
		record.SlaveAlias = slave.Attrs().Alias
	} else {
		record.SlaveMAC = linkMAC(slave)
	}
	if record.SlaveAlias == "" && record.SlaveMAC == "" {
		return fmt.Errorf("%s lacks a stable membership identity", slave.Attrs().Name)
	}
	r.state.Memberships[id] = record
	if err := r.save(); err != nil {
		delete(r.state.Memberships, id)
		return err
	}
	if err := netlink.LinkSetMasterByIndex(slave, master.Attrs().Index); err != nil {
		return fmt.Errorf("attach %s to %s: %w", slave.Attrs().Name, master.Attrs().Name, err)
	}
	return nil
}

func (r *OwnedLinkReconciler) detach(id string) error {
	record, ok := r.state.Memberships[id]
	if !ok {
		return nil
	}
	links, err := netlink.LinkList()
	if err != nil {
		slog.Warn("bridge member listing failed", "connection_id", id, "err", err)
		return fmt.Errorf("list bridge members: %w", err)
	}
	var slave netlink.Link
	for _, link := range links {
		if record.SlaveAlias != "" && link.Attrs().Alias == record.SlaveAlias || record.SlaveMAC != "" && linkMAC(link) == record.SlaveMAC {
			if slave != nil {
				return fmt.Errorf("membership %s matches multiple slaves", id)
			}
			slave = link
		}
	}
	if slave != nil && slave.Attrs().MasterIndex != 0 {
		master, err := netlink.LinkByIndex(slave.Attrs().MasterIndex)
		if err != nil {
			slog.Warn("current bridge master lookup failed", "link", slave.Attrs().Name, "err", err)
			return fmt.Errorf("find current bridge master: %w", err)
		}
		if !r.matchesRecordedMaster(master, record) {
			return fmt.Errorf("bridge membership %s no longer matches its ownership record", id)
		}
		if err := netlink.LinkSetNoMaster(slave); err != nil {
			return fmt.Errorf("detach %s from bridge: %w", slave.Attrs().Name, err)
		}
	}
	delete(r.state.Memberships, id)
	if err := r.save(); err != nil {
		r.state.Memberships[id] = record
		return err
	}
	return nil
}

func (r *OwnedLinkReconciler) prune(ctx context.Context, owned map[string]interfaceintent.Connection) ([]OwnedLinkResult, error) {
	var results []OwnedLinkResult
	if err := r.pruneMemberships(owned); err != nil {
		return results, err
	}
	for {
		if err := ctx.Err(); err != nil {
			slog.Warn("owned link prune canceled", "err", err)
			return results, fmt.Errorf("prune owned links: %w", err)
		}
		links, err := netlink.LinkList()
		if err != nil {
			slog.Warn("owned link prune listing failed", "err", err)
			return results, fmt.Errorf("list links for pruning: %w", err)
		}
		deleted, passResults, err := r.pruneVirtualPass(links, owned)
		results = append(results, passResults...)
		if err != nil {
			return results, err
		}
		if !deleted {
			return results, nil
		}
	}
}

func (r *OwnedLinkReconciler) pruneMemberships(owned map[string]interfaceintent.Connection) error {
	for id := range r.state.Memberships {
		connection, exists := owned[id]
		if exists && connection.Link != nil && connection.Link.BridgeMaster != "" {
			continue
		}
		if err := r.detach(id); err != nil {
			return err
		}
	}
	return nil
}

func (r *OwnedLinkReconciler) pruneVirtualPass(links []netlink.Link, owned map[string]interfaceintent.Connection) (bool, []OwnedLinkResult, error) {
	var results []OwnedLinkResult
	for _, link := range links {
		if link.Type() != "vlan" && link.Type() != "bridge" || !strings.HasPrefix(link.Attrs().Alias, ownedAliasPrefix) {
			continue
		}
		id, record, recorded := r.recordForAlias(link.Attrs().Alias)
		if !recorded {
			continue
		}
		if _, exists := owned[id]; exists {
			continue
		}
		if !r.matchesPrunableVirtual(link, record) || hasLinkChildren(link, links) {
			continue
		}
		result := newOwnedLinkResult(id, record.Name, OwnedLinkRemoved)
		result.ActualName, result.IfIndex, result.Operation = link.Attrs().Name, link.Attrs().Index, "delete"
		if err := netlink.LinkDel(link); err != nil {
			result.Status, result.Err = OwnedLinkFailed, fmt.Errorf("delete owned link %s: %w", link.Attrs().Name, err)
			results = append(results, result)
			continue
		}
		delete(r.state.Virtuals, id)
		if err := r.save(); err != nil {
			return false, results, err
		}
		results = append(results, result)
		return true, results, nil
	}
	return false, results, nil
}

func (r *OwnedLinkReconciler) matchesPrunableVirtual(link netlink.Link, record virtualRecord) bool {
	if record.Complete {
		return r.matchesRecordedVirtual(link, record)
	}
	if record.Quarantined || record.BootID != r.bootID || record.Alias == "" ||
		link.Attrs().Alias != record.Alias || link.Type() != record.Kind ||
		link.Attrs().Name != record.TempName && link.Attrs().Name != record.Name {
		return false
	}
	if record.Kind == string(interfaceintent.KindBridge) {
		return true
	}
	if record.Kind != string(interfaceintent.KindVLAN) || link.Attrs().ParentIndex != record.ParentIndex {
		return false
	}
	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(record.VLANID) || vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q {
		return false
	}
	parent, err := netlink.LinkByIndex(record.ParentIndex)
	return err == nil && r.matchesParentRecord(record, parent)
}

func (r *OwnedLinkReconciler) recordForAlias(alias string) (string, virtualRecord, bool) {
	for id, record := range r.state.Virtuals {
		if record.Alias == alias && record.Alias != "" {
			return id, record, true
		}
	}
	return "", virtualRecord{
		BootID: "", ConnectionID: "", Alias: "", TempName: "", Name: "", Kind: "",
		VLANID: 0, Parent: "", ParentMAC: "", ParentBoot: "", ParentKind: "", ParentName: "",
		ParentIndex: 0, LinkIndex: 0,
		Complete: false, Quarantined: false,
	}, false
}

func hasLinkChildren(link netlink.Link, links []netlink.Link) bool {
	for _, child := range links {
		if child.Attrs().ParentIndex == link.Attrs().Index || child.Attrs().MasterIndex == link.Attrs().Index {
			return true
		}
	}
	return false
}
