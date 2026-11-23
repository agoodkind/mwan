package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
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
		ConnectionID: id,
		Name:         name,
		ActualName:   "",
		IfIndex:      0,
		Status:       status,
		Dependency:   "",
		Operation:    "",
		Err:          nil,
	}
}

type virtualRecord struct {
	BootID         string                 `json:"boot_id"`
	ConnectionID   string                 `json:"connection_id"`
	Alias          string                 `json:"alias"`
	TempName       string                 `json:"temp_name"`
	Name           string                 `json:"name"`
	Kind           string                 `json:"kind"`
	VLANID         uint16                 `json:"vlan_id,omitempty"`
	Parent         string                 `json:"parent,omitempty"`
	ParentMAC      string                 `json:"parent_mac,omitempty"`
	ParentBoot     string                 `json:"parent_boot,omitempty"`
	ParentKind     string                 `json:"parent_kind,omitempty"`
	ParentName     string                 `json:"parent_name,omitempty"`
	ParentIndex    int                    `json:"parent_index,omitempty"`
	LinkIndex      int                    `json:"link_index,omitempty"`
	Complete       bool                   `json:"complete"`
	Quarantined    bool                   `json:"quarantined,omitempty"`
	TunnelProtocol string                 `json:"tunnel_protocol,omitempty"`
	TunnelRemote   string                 `json:"tunnel_remote,omitempty"`
	TunnelLocal    string                 `json:"tunnel_local,omitempty"`
	TunnelTTL      uint8                  `json:"tunnel_ttl,omitempty"`
	VXLAN          *interfaceintent.VXLAN `json:"vxlan,omitempty"`
}

type membershipRecord struct {
	SlaveAlias  string `json:"slave_alias,omitempty"`
	SlaveMAC    string `json:"slave_mac,omitempty"`
	MasterAlias string `json:"master_alias,omitempty"`
	MasterName  string `json:"master_name"`
	MasterBoot  string `json:"master_boot"`
	MasterIndex int    `json:"master_index"`
}

func (r *OwnedLinkReconciler) matchesRecordedMaster(
	master netlink.Link,
	record membershipRecord,
) bool {
	if record.MasterAlias != "" {
		return record.MasterBoot == r.bootID && master.Attrs().Index == record.MasterIndex &&
			master.Attrs().Alias == record.MasterAlias
	}

	return record.MasterBoot == r.bootID && master.Attrs().Index == record.MasterIndex &&
		master.Attrs().Alias == "" && master.Attrs().Name == record.MasterName
}

func matchesRecordedSlave(slave netlink.Link, record membershipRecord) bool {
	if record.SlaveAlias != "" {
		return slave.Attrs().Alias == record.SlaveAlias
	}

	return record.SlaveMAC != "" && linkMAC(slave) == record.SlaveMAC
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
		mu:        sync.Mutex{},
		statePath: statePath,
		bootID:    strings.TrimSpace(string(bootID)),
		state: ownedLinkState{
			Virtuals:    make(map[string]virtualRecord),
			Memberships: make(map[string]membershipRecord),
		},
	}
	if _, err := readOwnedJournal(statePath, &r.state); err != nil {
		slog.Warn("owned link state read failed", "path", statePath, "err", err)
		return nil, fmt.Errorf("read owned link state: %w", err)
	}

	if r.state.Virtuals == nil {
		r.state.Virtuals = make(map[string]virtualRecord)
	}

	if r.state.Memberships == nil {
		r.state.Memberships = make(map[string]membershipRecord)
	}

	return r, nil
}

// save writes the journal and syncs its directory.
func (r *OwnedLinkReconciler) save() error {
	const state = "owned link state"
	if err := writeOwnedJournal(r.statePath, state, r.state); err != nil {
		return err
	}

	return syncOwnedDirectory(r.statePath, state, state)
}

func aliasFor(id string) string { return ownedAliasPrefix + id }

func isAliasForConnection(alias string, id string) bool {
	base := aliasFor(id)
	return alias == base || strings.HasPrefix(alias, base+":")
}

type ownedLinkPass struct {
	log      *slog.Logger
	prepare  OwnedLinkPreparer
	byName   map[string]interfaceintent.Connection
	resolved map[string]OwnedLinkResult
	visiting map[string]bool
}

// Reconcile applies MWAN-owned link intent and reports each connection separately.
// A nil prepare sets links up without preparation.
func (r *OwnedLinkReconciler) Reconcile(
	ctx context.Context,
	log *slog.Logger,
	connections []interfaceintent.Connection,
	prepare OwnedLinkPreparer,
) ([]OwnedLinkResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if log == nil {
		log = slog.Default()
	}

	pass := &ownedLinkPass{
		log:      log,
		prepare:  prepare,
		byName:   make(map[string]interfaceintent.Connection, len(connections)),
		resolved: make(map[string]OwnedLinkResult),
		visiting: make(map[string]bool),
	}
	owned := make(map[string]interfaceintent.Connection)

	for _, connection := range connections {
		pass.byName[connection.Name] = connection
		if connection.Owner == interfaceintent.OwnerMWAN {
			owned[connection.ID.String()] = connection
		}
	}

	var results []OwnedLinkResult

	for _, connection := range connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}

		result := r.reconcileOne(ctx, connection, pass)
		if result.Err != nil {
			log.WarnContext(
				ctx,
				"owned link failed",
				"connection",
				result.ConnectionID,
				"operation",
				result.Operation,
				"err",
				result.Err,
			)
		}

		results = append(results, result)
	}

	removed, err := r.prune(ctx, owned)
	results = append(results, removed...)

	return results, err
}

func (r *OwnedLinkReconciler) reconcileOne(
	ctx context.Context,
	connection interfaceintent.Connection,
	pass *ownedLinkPass,
) OwnedLinkResult {
	id := connection.ID.String()
	resolved := pass.resolved
	if result, ok := resolved[id]; ok {
		return result
	}

	result := newOwnedLinkResult(id, connection.Name, OwnedLinkWaiting)
	if err := ctx.Err(); err != nil {
		result.Status, result.Err = OwnedLinkFailed, err
		return result
	}

	if pass.visiting[id] {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf("link dependency cycle at %s", id)
		return result
	}

	pass.visiting[id] = true
	defer delete(pass.visiting, id)
	if connection.Link == nil {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf(
			"connection %s has no link intent",
			id,
		)
		resolved[id] = result

		return result
	}

	parent, underlay, present := r.requiredLinks(ctx, connection, pass, &result)
	if !present {
		resolved[id] = result
		return result
	}

	var link netlink.Link
	var created bool
	var err error
	if connection.Link.Kind == interfaceintent.KindPhysical || connection.Link.Kind == "" {
		link, err = physicalLink(connection)
	} else {
		link, created, err = r.convergedVirtualLink(connection, parent, underlay)
	}

	if err != nil {
		result.Status, result.Err, result.Operation = OwnedLinkFailed, err, "resolve"
		if errors.Is(err, errTunnelEndpointsHeld) {
			result.Status, result.Operation = OwnedLinkWaiting, "wait-for-tunnel"
		}

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

	if !r.reconcileMembership(ctx, connection, link, pass, &result) {
		resolved[id] = result
		return result
	}

	if err := applyLinkSettings(ctx, pass, link, connection, result); err != nil {
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

func (r *OwnedLinkReconciler) vlanParent(
	ctx context.Context,
	connection interfaceintent.Connection,
	pass *ownedLinkPass,
	result *OwnedLinkResult,
) netlink.Link {
	if connection.Link.VLAN == nil || connection.Link.VLAN.ID == 0 ||
		connection.Link.VLAN.ID > 4094 {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf(
			"connection %s has invalid VLAN intent",
			connection.ID,
		)

		return nil
	}

	result.Dependency = connection.Link.VLAN.Parent
	parent, dependency := r.dependency(ctx, result.Dependency, pass)
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

func (r *OwnedLinkReconciler) reconcileMembership(
	ctx context.Context,
	connection interfaceintent.Connection,
	link netlink.Link,
	pass *ownedLinkPass,
	result *OwnedLinkResult,
) bool {
	if connection.Link.BridgeMaster == "" {
		if err := r.detach(connection.ID.String()); err != nil {
			result.Status, result.Err, result.Operation = OwnedLinkFailed, err, "detach-bridge"
			return false
		}

		return true
	}

	result.Dependency = connection.Link.BridgeMaster
	master, dependency := r.dependency(ctx, result.Dependency, pass)
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

	if master.Type() != virtualBridgeKind {
		result.Status, result.Err = OwnedLinkFailed, fmt.Errorf(
			"%s is not a bridge",
			result.Dependency,
		)

		return false
	}

	if err := r.attach(connection.ID.String(), link, master); err != nil {
		result.Status, result.Err, result.Operation = OwnedLinkFailed, err, "attach-bridge"
		return false
	}

	return true
}

func (r *OwnedLinkReconciler) dependency(
	ctx context.Context,
	name string,
	pass *ownedLinkPass,
) (netlink.Link, OwnedLinkResult) {
	connection, ok := pass.byName[name]
	if !ok {
		result := newOwnedLinkResult("", name, OwnedLinkWaiting)
		result.Dependency = name

		return nil, result
	}

	if connection.Owner == interfaceintent.OwnerMWAN {
		result := r.reconcileOne(ctx, connection, pass)
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

	if connection.Link != nil &&
		(connection.Link.Kind == interfaceintent.KindPhysical || connection.Link.Kind == "") {
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

	if connection.Link != nil && connection.Link.Kind == interfaceintent.KindBridge &&
		link.Type() != virtualBridgeKind {
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
		slog.Warn(
			"physical link listing failed",
			"connection_id",
			connection.ID.String(),
			"err",
			err,
		)

		return nil, fmt.Errorf("list physical links: %w", err)
	}

	var found netlink.Link

	for _, link := range links {
		if link.Type() == virtualBridgeKind || link.Type() == "vlan" ||
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

	if record.ParentKind == virtualBridgeKind {
		if record.ParentBoot != r.bootID || parent.Type() != virtualBridgeKind ||
			parent.Attrs().Alias != record.Parent {
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
		link.Attrs().Index != record.LinkIndex || link.Type() != record.kernelType() {
		return false
	}

	if record.Kind == string(interfaceintent.KindBridge) ||
		record.Kind == string(interfaceintent.KindTunnel) {
		if record.TunnelProtocol == string(interfaceintent.TunnelProtocolVXLAN) {
			return matchesVXLANRecord(link, record) && r.matchesVXLANUnderlay(record)
		}

		return true
	}

	if record.Kind != string(interfaceintent.KindVLAN) || link.Attrs().ParentIndex == 0 {
		return false
	}

	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(record.VLANID) ||
		vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q {
		return false
	}

	parent, err := netlink.LinkByIndex(link.Attrs().ParentIndex)
	if err != nil {
		return false
	}

	return r.matchesParentRecord(record, parent)
}

func (r *OwnedLinkReconciler) attach(id string, slave netlink.Link, master netlink.Link) error {
	if slave.Attrs().MasterIndex == master.Attrs().Index {
		record, recorded := r.state.Memberships[id]
		if !recorded || !matchesRecordedSlave(slave, record) ||
			!r.matchesRecordedMaster(master, record) {
			return fmt.Errorf(
				"%s has a bridge membership outside its ownership record",
				slave.Attrs().Name,
			)
		}

		return nil
	}

	if slave.Attrs().MasterIndex != 0 {
		record, recorded := r.state.Memberships[id]
		if !recorded {
			return fmt.Errorf("%s has an unrecorded bridge membership", slave.Attrs().Name)
		}

		prior, err := netlink.LinkByIndex(slave.Attrs().MasterIndex)
		if err != nil {
			slog.Warn(
				"recorded bridge master lookup failed",
				"link",
				slave.Attrs().Name,
				"err",
				err,
			)

			return fmt.Errorf("find recorded bridge master: %w", err)
		}

		if !r.matchesRecordedMaster(prior, record) {
			return fmt.Errorf(
				"%s has a bridge membership outside its ownership record",
				slave.Attrs().Name,
			)
		}

		if err := r.detach(id); err != nil {
			return err
		}
	}

	record := membershipRecord{
		SlaveAlias:  "",
		SlaveMAC:    "",
		MasterAlias: master.Attrs().Alias,
		MasterName:  master.Attrs().Name,
		MasterBoot:  r.bootID,
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
		if matchesRecordedSlave(link, record) {
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

func (r *OwnedLinkReconciler) prune(
	ctx context.Context,
	owned map[string]interfaceintent.Connection,
) ([]OwnedLinkResult, error) {
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

func (r *OwnedLinkReconciler) pruneVirtualPass(
	links []netlink.Link,
	owned map[string]interfaceintent.Connection,
) (bool, []OwnedLinkResult, error) {
	var results []OwnedLinkResult

	for _, link := range links {
		if !isOwnedVirtualType(link.Type()) ||
			!strings.HasPrefix(link.Attrs().Alias, ownedAliasPrefix) {
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
			result.Status, result.Err = OwnedLinkFailed, fmt.Errorf(
				"delete owned link %s: %w",
				link.Attrs().Name,
				err,
			)
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
		link.Attrs().Alias != record.Alias || link.Type() != record.kernelType() ||
		link.Attrs().Name != record.TempName && link.Attrs().Name != record.Name {
		return false
	}

	if record.Kind == string(interfaceintent.KindBridge) ||
		record.Kind == string(interfaceintent.KindTunnel) {
		if record.TunnelProtocol == string(interfaceintent.TunnelProtocolVXLAN) {
			return matchesVXLANRecord(link, record) && r.matchesVXLANUnderlay(record)
		}

		return true
	}

	if record.Kind != string(interfaceintent.KindVLAN) ||
		link.Attrs().ParentIndex != record.ParentIndex {
		return false
	}

	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(record.VLANID) ||
		vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q {
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
		BootID:         "",
		ConnectionID:   "",
		Alias:          "",
		TempName:       "",
		Name:           "",
		Kind:           "",
		VLANID:         0,
		Parent:         "",
		ParentMAC:      "",
		ParentBoot:     "",
		ParentKind:     "",
		ParentName:     "",
		ParentIndex:    0,
		LinkIndex:      0,
		Complete:       false,
		Quarantined:    false,
		TunnelProtocol: "",
		TunnelRemote:   "",
		TunnelLocal:    "",
		TunnelTTL:      0,
		VXLAN:          nil,
	}, false
}

func hasLinkChildren(link netlink.Link, links []netlink.Link) bool {
	for _, child := range links {
		if vxlan, ok := child.(*netlink.Vxlan); ok && vxlan.VtepDevIndex == link.Attrs().Index {
			return true
		}

		if child.Attrs().ParentIndex == link.Attrs().Index ||
			child.Attrs().MasterIndex == link.Attrs().Index {
			return true
		}
	}

	return false
}
