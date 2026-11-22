package netif

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/interfaceintent"
)

var (
	errOwnershipTagMismatch            = errors.New("has a different ownership tag")
	errBridgeTypeMismatch              = errors.New("is not a bridge")
	errVLANIdentityMismatch            = errors.New("has incompatible VLAN identity")
	errForeignAlias                    = errors.New("foreign alias")
	errTemporaryLinkKindMismatch       = errors.New("wrong kind")
	errTemporaryVLANMismatch           = errors.New("wrong VLAN identity")
	errUnsupportedLinkKind             = errors.New("unsupported link kind")
	errMultipleTaggedLinks             = errors.New("has multiple tagged links")
	errUnownedLinkName                 = errors.New("is occupied by an unowned link")
	errTemporaryNameOccupied           = errors.New("is occupied")
	errMissingOwnershipRecord          = errors.New("lacks a matching ownership record")
	errRenamedReservationMismatch      = errors.New("differs from its reservation")
	errRenamedVXLANReservationMismatch = errors.New("differs from its VXLAN reservation")
	errTaggedOwnershipMismatch         = errors.New("differs from its ownership record")
	errTemporaryJournalMismatch        = errors.New("journal identity differs")
	errTemporaryParentMismatch         = errors.New("parent identity differs")
	errTemporaryVLANTagMismatch        = errors.New("VLAN tag differs")
	errTemporaryUnderlayMismatch       = errors.New("underlay identity differs")
	errReservedTemporaryLinkMissing    = errors.New("is absent; retry reconciliation")
	errReservedLinkAliasMismatch       = errors.New("has incorrect alias")
)

const virtualBridgeKind = "bridge"

type virtualLinkResult struct {
	link    netlink.Link
	created bool
}

func validateVirtual(
	link netlink.Link,
	connection interfaceintent.Connection,
	parent netlink.Link,
	alias string,
) error {
	if alias == "" || link.Attrs().Alias != alias {
		slog.Warn("virtual link ownership check failed", "link", link.Attrs().Name)

		return fmt.Errorf("%s %w", link.Attrs().Name, errOwnershipTagMismatch)
	}

	if connection.Link.Kind == interfaceintent.KindTunnel {
		return validateTunnelLink(link, connection)
	}

	if connection.Link.Kind == interfaceintent.KindBridge {
		if link.Type() != virtualBridgeKind {
			slog.Warn("virtual bridge type check failed", "link", link.Attrs().Name)

			return fmt.Errorf("%s %w", link.Attrs().Name, errBridgeTypeMismatch)
		}

		return nil
	}

	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(connection.Link.VLAN.ID) ||
		vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q ||
		link.Attrs().ParentIndex != parent.Attrs().Index {
		slog.Warn("virtual VLAN identity check failed", "link", link.Attrs().Name)

		return fmt.Errorf("%s %w", link.Attrs().Name, errVLANIdentityMismatch)
	}

	return nil
}

func validateTemporaryLink(
	link netlink.Link,
	connection interfaceintent.Connection,
	parent netlink.Link,
	record virtualRecord,
) error {
	if record.Alias == "" || link.Attrs().Alias != "" && link.Attrs().Alias != record.Alias {
		slog.Warn("temporary link alias check failed", "link", link.Attrs().Name)

		return fmt.Errorf("quarantined temporary link %s: %w", link.Attrs().Name, errForeignAlias)
	}

	if connection.Link.Kind == interfaceintent.KindTunnel {
		return validateTemporaryTunnel(link, connection, record)
	}

	if connection.Link.Kind == interfaceintent.KindBridge {
		if link.Type() != virtualBridgeKind {
			slog.Warn("temporary bridge type check failed", "link", link.Attrs().Name)

			return fmt.Errorf(
				"quarantined temporary link %s: %w",
				link.Attrs().Name,
				errTemporaryLinkKindMismatch,
			)
		}

		return nil
	}

	vlan, ok := link.(*netlink.Vlan)
	if !ok || vlan.VlanId != int(connection.Link.VLAN.ID) ||
		vlan.VlanProtocol != netlink.VLAN_PROTOCOL_8021Q ||
		link.Attrs().ParentIndex != parent.Attrs().Index {
		slog.Warn("temporary VLAN identity check failed", "link", link.Attrs().Name)

		return fmt.Errorf(
			"quarantined temporary link %s: %w",
			link.Attrs().Name,
			errTemporaryVLANMismatch,
		)
	}

	return nil
}

// virtualLink creates or adopts a Bridge, Vlan, Sittun, or Vxlan link.
// VLAN creation uses a parent link, while tunnel creation uses an underlay.
func (r *OwnedLinkReconciler) virtualLink(
	connection interfaceintent.Connection,
	parent netlink.Link,
	underlay netlink.Link,
) (virtualLinkResult, error) {
	if connection.Link.Kind != interfaceintent.KindBridge &&
		connection.Link.Kind != interfaceintent.KindVLAN &&
		connection.Link.Kind != interfaceintent.KindTunnel {
		return virtualLinkResult{}, fmt.Errorf(
			"%w %s",
			errUnsupportedLinkKind,
			connection.Link.Kind,
		)
	}

	connectionID := connection.ID.String()
	record := r.state.Virtuals[connectionID]
	links, err := netlink.LinkList()
	if err != nil {
		slog.Warn("virtual link listing failed", "connection_id", connectionID, "err", err)

		return virtualLinkResult{}, fmt.Errorf("list virtual links: %w", err)
	}

	var found netlink.Link

	for _, link := range links {
		if isAliasForConnection(link.Attrs().Alias, connectionID) {
			if found != nil {
				return virtualLinkResult{}, fmt.Errorf(
					"connection %s %w",
					connectionID,
					errMultipleTaggedLinks,
				)
			}

			found = link
		}

		if link.Attrs().Name == connection.Name &&
			(record.Alias == "" || link.Attrs().Alias != record.Alias) {
			return virtualLinkResult{}, fmt.Errorf(
				"name %s %w",
				connection.Name,
				errUnownedLinkName,
			)
		}
	}

	// The kernel does not restore virtual links after reboot, so an old reservation cannot be recovered.
	if record, ok := r.state.Virtuals[connectionID]; ok && found == nil && !record.Complete &&
		record.BootID == r.bootID {
		return r.recoverVirtual(connection, parent, record)
	}

	if err := r.validateVXLANReceiverAvailability(connection, found, underlay, links); err != nil {
		return virtualLinkResult{}, err
	}

	if found != nil {
		return r.adoptVirtualLink(found, connection, parent)
	}

	if _, ok := r.state.Virtuals[connectionID]; ok {
		delete(r.state.Virtuals, connectionID)
		if err := r.save(); err != nil {
			return virtualLinkResult{}, err
		}
	}

	record, err = r.reserveVirtual(connection, parent, underlay, links)
	if err != nil {
		return virtualLinkResult{}, err
	}

	requested, err := requestedVirtualLink(connection, record.TempName, parent, underlay)
	if err != nil {
		return r.handleVirtualAddError(connection, parent, record, err)
	}

	if err := netlink.LinkAdd(requested); err != nil {
		return r.handleVirtualAddError(connection, parent, record, err)
	}

	return r.recoverVirtual(connection, parent, record)
}

func (r *OwnedLinkReconciler) reserveVirtual(
	connection interfaceintent.Connection,
	parent netlink.Link,
	underlay netlink.Link,
	links []netlink.Link,
) (virtualRecord, error) {
	connectionID := connection.ID.String()
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		slog.Warn(
			"temporary link name reservation failed",
			"connection_id",
			connectionID,
			"err",
			err,
		)

		return virtualRecord{}, fmt.Errorf("reserve temporary link name: %w", err)
	}

	record := virtualRecord{
		BootID:         r.bootID,
		ConnectionID:   connectionID,
		Alias:          aliasFor(connectionID) + ":" + hex.EncodeToString(nonce[:]),
		TempName:       "mw-" + hex.EncodeToString(nonce[:]),
		Name:           connection.Name,
		Kind:           string(connection.Link.Kind),
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
	}
	recordTunnelIdentity(&record, connection.Link.Tunnel, underlay)
	if parent != nil {
		record.VLANID = connection.Link.VLAN.ID
		record.Parent, record.ParentMAC = parentRecord(parent)
		record.ParentBoot, record.ParentKind, record.ParentName = r.bootID, parent.Type(), parent.Attrs().Name
		record.ParentIndex = parent.Attrs().Index
	}

	for _, link := range links {
		if link.Attrs().Name == record.TempName {
			return virtualRecord{}, fmt.Errorf(
				"temporary name %s %w",
				record.TempName,
				errTemporaryNameOccupied,
			)
		}
	}

	r.state.Virtuals[connectionID] = record
	if err := r.save(); err != nil {
		delete(r.state.Virtuals, connectionID)

		return virtualRecord{}, err
	}

	return record, nil
}

// handleVirtualAddError validates and recovers the requested Bridge, Vlan,
// Sittun, or Vxlan link after creation fails.
func (r *OwnedLinkReconciler) handleVirtualAddError(
	connection interfaceintent.Connection,
	parent netlink.Link,
	record virtualRecord,
	addErr error,
) (virtualLinkResult, error) {
	slog.Warn(
		"owned virtual link creation failed",
		"connection_id",
		record.ConnectionID,
		"temporary_name",
		record.TempName,
		"kind",
		record.Kind,
		"err",
		addErr,
	)
	temporary, lookupErr := netlink.LinkByName(record.TempName)
	if IsLinkNotFound(lookupErr) {
		delete(r.state.Virtuals, record.ConnectionID)
		if saveErr := r.save(); saveErr != nil {
			return virtualLinkResult{}, errors.Join(addErr, saveErr)
		}

		return virtualLinkResult{}, fmt.Errorf("create %s: %w", record.TempName, addErr)
	}

	if lookupErr != nil {
		return virtualLinkResult{}, errors.Join(addErr, lookupErr)
	}

	if identityErr := validateTemporaryLink(
		temporary,
		connection,
		parent,
		record,
	); identityErr != nil {
		record.Quarantined = true
		r.state.Virtuals[record.ConnectionID] = record
		if saveErr := r.save(); saveErr != nil {
			return virtualLinkResult{}, errors.Join(addErr, identityErr, saveErr)
		}

		return virtualLinkResult{}, errors.Join(addErr, identityErr)
	}

	return r.recoverVirtual(connection, parent, record)
}

// adoptVirtualLink validates and adopts a Bridge, Vlan, Sittun, or Vxlan link.
func (r *OwnedLinkReconciler) adoptVirtualLink(
	found netlink.Link,
	connection interfaceintent.Connection,
	parent netlink.Link,
) (virtualLinkResult, error) {
	connectionID := connection.ID.String()
	recorded, ok := r.state.Virtuals[connectionID]
	if !ok || recorded.ConnectionID != connectionID || recorded.Alias == "" ||
		found.Attrs().Alias != recorded.Alias {
		return virtualLinkResult{}, fmt.Errorf(
			"tagged link %s %w",
			found.Attrs().Name,
			errMissingOwnershipRecord,
		)
	}

	if !recorded.Complete &&
		found.Attrs().Name == recorded.TempName {
		return r.recoverVirtual(connection, parent, recorded)
	}

	if !recorded.Complete && found.Attrs().Name == recorded.Name {
		return r.adoptRenamedVirtualLink(found, connection, parent, recorded)
	}

	if !r.matchesRecordedVirtual(found, recorded) {
		return virtualLinkResult{}, fmt.Errorf(
			"tagged link %s %w",
			found.Attrs().Name,
			errTaggedOwnershipMismatch,
		)
	}

	if err := validateVirtual(found, connection, parent, recorded.Alias); err != nil {
		return virtualLinkResult{}, err
	}

	record := r.virtualRecordForAdoption(found, connection, parent, recorded)

	r.state.Virtuals[connectionID] = record
	if err := r.save(); err != nil {
		return virtualLinkResult{}, err
	}

	if found.Attrs().Name != connection.Name {
		if err := netlink.LinkSetName(found, connection.Name); err != nil {
			slog.Warn(
				"owned virtual link rename failed",
				"connection_id",
				connectionID,
				"current_name",
				found.Attrs().Name,
				"desired_name",
				connection.Name,
				"err",
				err,
			)

			return virtualLinkResult{}, fmt.Errorf(
				"rename owned link %s: %w",
				found.Attrs().Name,
				err,
			)
		}

		var err error
		found, err = netlink.LinkByName(connection.Name)
		if err != nil {
			return virtualLinkResult{}, fmt.Errorf(
				"refresh renamed link %s: %w",
				connection.Name,
				err,
			)
		}

		if err := validateVirtual(found, connection, parent, record.Alias); err != nil {
			return virtualLinkResult{}, err
		}
	}

	return virtualLinkResult{link: found, created: false}, nil
}

func (r *OwnedLinkReconciler) virtualRecordForAdoption(
	found netlink.Link,
	connection interfaceintent.Connection,
	parent netlink.Link,
	recorded virtualRecord,
) virtualRecord {
	record := virtualRecord{
		BootID:         r.bootID,
		ConnectionID:   recorded.ConnectionID,
		Alias:          recorded.Alias,
		TempName:       recorded.TempName,
		Name:           connection.Name,
		Kind:           string(connection.Link.Kind),
		VLANID:         0,
		Parent:         "",
		ParentMAC:      "",
		ParentBoot:     "",
		ParentKind:     "",
		ParentName:     "",
		ParentIndex:    0,
		LinkIndex:      found.Attrs().Index,
		Complete:       true,
		Quarantined:    false,
		TunnelProtocol: recorded.TunnelProtocol,
		TunnelRemote:   "",
		TunnelLocal:    "",
		TunnelTTL:      recorded.TunnelTTL,
		VXLAN:          recorded.VXLAN,
	}
	if recorded.VXLAN != nil {
		record.TunnelRemote, record.TunnelLocal = recorded.TunnelRemote, recorded.TunnelLocal
		record.Parent, record.ParentMAC = recorded.Parent, recorded.ParentMAC
		record.ParentBoot, record.ParentKind, record.ParentName = recorded.ParentBoot,
			recorded.ParentKind, recorded.ParentName
		record.ParentIndex = recorded.ParentIndex
	}

	if parent != nil {
		record.VLANID = connection.Link.VLAN.ID
		record.Parent, record.ParentMAC = parentRecord(parent)
		record.ParentBoot, record.ParentKind, record.ParentName = r.bootID, parent.Type(),
			parent.Attrs().Name
		record.ParentIndex = parent.Attrs().Index
	}

	return record
}

// adoptRenamedVirtualLink adopts a renamed Bridge, Vlan, Sittun, or Vxlan link.
func (r *OwnedLinkReconciler) adoptRenamedVirtualLink(
	found netlink.Link,
	connection interfaceintent.Connection,
	parent netlink.Link,
	recorded virtualRecord,
) (virtualLinkResult, error) {
	if recorded.Quarantined || recorded.BootID != r.bootID ||
		recorded.Kind != string(connection.Link.Kind) ||
		recorded.Name != connection.Name ||
		parent != nil && !r.matchesParentRecord(recorded, parent) ||
		connection.Link.Kind == interfaceintent.KindVLAN &&
			recorded.VLANID != connection.Link.VLAN.ID {
		slog.Warn(
			"renamed virtual link reservation check failed",
			"connection_id",
			recorded.ConnectionID,
		)

		return virtualLinkResult{}, fmt.Errorf(
			"renamed link %s %w",
			found.Attrs().Name,
			errRenamedReservationMismatch,
		)
	}

	if !r.matchesVXLANReservation(found, recorded) {
		slog.Warn("renamed virtual link VXLAN check failed", "connection_id", recorded.ConnectionID)

		return virtualLinkResult{}, fmt.Errorf(
			"renamed link %s %w",
			found.Attrs().Name,
			errRenamedVXLANReservationMismatch,
		)
	}

	if err := validateVirtual(found, connection, parent, recorded.Alias); err != nil {
		return virtualLinkResult{}, err
	}

	recorded.LinkIndex = found.Attrs().Index
	recorded.Complete = true
	r.state.Virtuals[recorded.ConnectionID] = recorded
	if err := r.save(); err != nil {
		return virtualLinkResult{}, err
	}

	return virtualLinkResult{link: found, created: false}, nil
}

// recoverVirtual restores a reserved Bridge, Vlan, Sittun, or Vxlan link.
func (r *OwnedLinkReconciler) recoverVirtual(
	connection interfaceintent.Connection,
	parent netlink.Link,
	record virtualRecord,
) (virtualLinkResult, error) {
	if err := r.validateVirtualRecovery(connection, parent, record); err != nil {
		return virtualLinkResult{}, err
	}

	link, err := netlink.LinkByName(record.TempName)
	if IsLinkNotFound(err) {
		delete(r.state.Virtuals, connection.ID.String())
		if err := r.save(); err != nil {
			return virtualLinkResult{}, err
		}

		return virtualLinkResult{}, fmt.Errorf(
			"reserved temporary link %s %w",
			record.TempName,
			errReservedTemporaryLinkMissing,
		)
	}

	if err != nil {
		return virtualLinkResult{}, fmt.Errorf("find reserved link %s: %w", record.TempName, err)
	}

	if err := validateTemporaryLink(link, connection, parent, record); err != nil {
		return virtualLinkResult{}, err
	}

	if link.Attrs().Alias == "" {
		if err := netlink.LinkSetAlias(link, record.Alias); err != nil {
			return virtualLinkResult{}, fmt.Errorf("tag reserved link %s: %w", record.TempName, err)
		}
	}

	link, err = netlink.LinkByName(record.TempName)
	if err != nil {
		return virtualLinkResult{}, fmt.Errorf("verify reserved link alias: %w", err)
	}

	if link.Attrs().Alias != record.Alias {
		return virtualLinkResult{}, fmt.Errorf(
			"reserved link %s %w",
			record.TempName,
			errReservedLinkAliasMismatch,
		)
	}

	if err := netlink.LinkSetName(link, connection.Name); err != nil {
		slog.Warn(
			"temporary owned link rename failed",
			"connection_id",
			connection.ID.String(),
			"temporary_name",
			record.TempName,
			"desired_name",
			connection.Name,
			"err",
			err,
		)

		return virtualLinkResult{}, fmt.Errorf("rename temporary link: %w", err)
	}

	link, err = netlink.LinkByName(connection.Name)
	if err != nil {
		return virtualLinkResult{}, fmt.Errorf("refresh renamed link: %w", err)
	}

	if err := validateVirtual(link, connection, parent, record.Alias); err != nil {
		return virtualLinkResult{}, err
	}

	record.Complete = true
	record.LinkIndex = link.Attrs().Index
	r.state.Virtuals[connection.ID.String()] = record
	if err := r.save(); err != nil {
		return virtualLinkResult{link: nil, created: true}, err
	}

	return virtualLinkResult{link: link, created: true}, nil
}

func (r *OwnedLinkReconciler) validateVirtualRecovery(
	connection interfaceintent.Connection,
	parent netlink.Link,
	record virtualRecord,
) error {
	if record.Quarantined || record.Alias == "" || record.BootID != r.bootID ||
		record.ConnectionID != connection.ID.String() ||
		record.Name != connection.Name ||
		record.Kind != string(connection.Link.Kind) {
		slog.Warn("virtual recovery journal check failed", "connection_id", record.ConnectionID)

		return fmt.Errorf(
			"quarantined temporary link %s: %w",
			record.TempName,
			errTemporaryJournalMismatch,
		)
	}

	if parent != nil && !r.matchesParentRecord(record, parent) {
		slog.Warn("virtual recovery parent check failed", "connection_id", record.ConnectionID)

		return fmt.Errorf(
			"quarantined temporary link %s: %w",
			record.TempName,
			errTemporaryParentMismatch,
		)
	}

	if connection.Link.Kind == interfaceintent.KindVLAN &&
		record.VLANID != connection.Link.VLAN.ID {
		slog.Warn("virtual recovery VLAN check failed", "connection_id", record.ConnectionID)

		return fmt.Errorf(
			"quarantined temporary link %s: %w",
			record.TempName,
			errTemporaryVLANTagMismatch,
		)
	}

	if record.VXLAN != nil && !r.matchesVXLANUnderlay(record) {
		slog.Warn("virtual recovery underlay check failed", "connection_id", record.ConnectionID)

		return fmt.Errorf(
			"quarantined temporary link %s: %w",
			record.TempName,
			errTemporaryUnderlayMismatch,
		)
	}

	return nil
}
