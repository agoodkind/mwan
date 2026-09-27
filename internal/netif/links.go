package netif

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/interfaceintent"
)

// sysClassNet is where the kernel exposes each link's device, whose driver
// symlink names the driver bound to it.
const sysClassNet = "/sys/class/net"

// LinkIdentity is what the network manager matches a device by: the name it
// carries now, its hardware address in lower-case colon form, and the driver
// bound to its device. A link with no device, such as a bridge or a VLAN,
// has an empty driver.
type LinkIdentity struct {
	Name            string
	HardwareAddress string
	Driver          string
}

// ListLinkIdentities lists every link the kernel holds with the identity
// the network manager matches it by.
func ListLinkIdentities(log *slog.Logger) ([]LinkIdentity, error) {
	links, err := netlink.LinkList()
	if err != nil {
		log.Warn("link: LinkList failed", "err", err)
		return nil, fmt.Errorf("LinkList: %w", err)
	}
	identities := make([]LinkIdentity, 0, len(links))
	for _, link := range links {
		attrs := link.Attrs()
		identities = append(identities, LinkIdentity{
			Name:            attrs.Name,
			HardwareAddress: strings.ToLower(attrs.HardwareAddr.String()),
			Driver:          linkDriver(attrs.Name),
		})
	}
	return identities, nil
}

// linkDriver reads the driver bound to a link's device from sysfs. A link
// whose device has no driver symlink, which is every virtual link, reports
// an empty driver rather than an error, because that is the answer.
func linkDriver(name string) string {
	target, err := os.Readlink(filepath.Join(sysClassNet, name, "device", "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// resolveConnectionLink matches a configured connection against current kernel links.
// A physical match is unique by the configured MAC or driver; virtual links
// must also satisfy their configured kernel type and VLAN parent and tag.
func resolveConnectionLink(log *slog.Logger, connection interfaceintent.Connection) (netlink.Link, error) {
	return resolveConnectionLinkAtIndex(log, connection, 0)
}

// resolveConnectionLinkAtIndex keeps a bound virtual device across a rename.
// Callers must clear priorIndex after deletion or loss of event continuity.
func resolveConnectionLinkAtIndex(
	log *slog.Logger, connection interfaceintent.Connection, priorIndex int,
) (netlink.Link, error) {
	if connection.Link == nil {
		return nil, nil
	}
	if priorIndex > 0 {
		prior, err := netlink.LinkByIndex(priorIndex)
		if err == nil && priorVirtualLinkMatches(prior, connection) {
			return prior, nil
		}
	}
	links, err := netlink.LinkList()
	if err != nil {
		log.Warn("link: LinkList failed", "connection", connection.ID, "err", err)
		return nil, fmt.Errorf("list links for %s: %w", connection.ID, err)
	}
	var matches []netlink.Link
	for _, link := range links {
		if linkMatchesConnection(link, connection) {
			matches = append(matches, link)
		}
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("connection %s matches %d kernel links", connection.ID, len(matches))
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return matches[0], nil
}

func priorVirtualLinkMatches(link netlink.Link, connection interfaceintent.Connection) bool {
	configured := connection.Link
	attrs := link.Attrs()
	if configured == nil || attrs == nil {
		return false
	}
	switch configured.Kind {
	case interfaceintent.KindBridge:
		return link.Type() == "bridge"
	case interfaceintent.KindVLAN:
		vlan, ok := link.(*netlink.Vlan)
		if !ok || configured.VLAN == nil || attrs.ParentIndex == 0 || vlan.VlanId != int(configured.VLAN.ID) {
			return false
		}
		parent, err := netlink.LinkByName(configured.VLAN.Parent)
		return IsLinkNotFound(err) || (err == nil && parent.Attrs() != nil && parent.Attrs().Index == attrs.ParentIndex)
	}
	return false
}

func linkMatchesConnection(link netlink.Link, connection interfaceintent.Connection) bool {
	configured := connection.Link
	attrs := link.Attrs()
	if configured == nil || attrs == nil {
		return false
	}
	switch configured.Kind {
	case interfaceintent.KindBridge:
		return link.Type() == "bridge" && attrs.Name == connection.Name
	case interfaceintent.KindVLAN:
		vlan, ok := link.(*netlink.Vlan)
		if !ok || configured.VLAN == nil || attrs.Name != connection.Name || vlan.VlanId != int(configured.VLAN.ID) {
			return false
		}
		parent, err := netlink.LinkByIndex(attrs.ParentIndex)
		return err == nil && parent.Attrs() != nil && parent.Attrs().Name == configured.VLAN.Parent
	case interfaceintent.KindPhysical, "":
		if link.Type() == "vlan" || link.Type() == "bridge" {
			return false
		}
		match := configured.Match
		if match.HardwareAddress != "" {
			address := attrs.PermHWAddr
			if len(address) == 0 {
				address = attrs.HardwareAddr
			}
			return strings.EqualFold(address.String(), match.HardwareAddress)
		}
		return match.Driver != "" && linkDriver(attrs.Name) == match.Driver
	}
	return false
}
