package commandline

import (
	"context"
	"log/slog"
	"strings"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/networkd"
)

type linkRename struct {
	Current string
	Wanted  string
}

// udev applies a .link file when it initializes a device.
// Reloading networkd does not rename an existing device.
func warnRenamedLinks(
	ctx context.Context,
	log *slog.Logger,
	changes []networkd.Change,
	connections []interfaceintent.Connection,
) {
	live, err := netif.ListLinkIdentities(log)
	if err != nil {
		log.WarnContext(
			ctx,
			"ifmgr: listing links to compare rendered .link names failed",
			"err",
			err,
		)

		return
	}

	for _, rename := range renamedLinks(changes, connections, live) {
		log.WarnContext(
			ctx,
			"ifmgr: a rendered .link file asks for a name the link does not carry; the new name takes effect at the next reboot",
			"current",
			rename.Current,
			"wanted",
			rename.Wanted,
		)
	}
}

func renamedLinks(
	changes []networkd.Change,
	connections []interfaceintent.Connection,
	live []netif.LinkIdentity,
) []linkRename {
	byName := make(map[string]interfaceintent.Connection, len(connections))
	for _, connection := range connections {
		byName[connection.Name] = connection
	}

	var renames []linkRename

	for _, change := range changes {
		if change.Kind != networkd.FileLink || change.Removed {
			continue
		}

		connection, known := byName[change.Interface]
		if !known || connection.Link == nil {
			continue
		}

		for _, link := range live {
			if !matches(connection.Link.Match, link) || link.Name == connection.Name {
				continue
			}

			renames = append(renames, linkRename{Current: link.Name, Wanted: connection.Name})
		}
	}

	return renames
}

func matches(match interfaceintent.Match, link netif.LinkIdentity) bool {
	if match.Driver != "" {
		return link.Driver == match.Driver
	}

	if match.HardwareAddress != "" {
		return link.HardwareAddress == strings.ToLower(match.HardwareAddress)
	}

	return false
}
