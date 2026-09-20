package netif

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vishvananda/netlink"
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
