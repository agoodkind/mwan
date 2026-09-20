package main

import (
	"context"
	"log/slog"
	"strings"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkd"
)

// linkRename is a device whose rendered .link file asks for a name the
// device does not carry.
type linkRename struct {
	Current string
	Wanted  string
}

// warnRenamedLinks logs every rendered .link file whose device is already
// present under another name. The new name takes effect at the next reboot:
// udev reads a .link file when the device appears, the network manager's
// reload does not revisit it, and the kernel refuses to rename a link that
// is up, so the daemon never attempts the rename.
func warnRenamedLinks(
	ctx context.Context,
	log *slog.Logger,
	changes []networkd.Change,
	specs []networkd.Spec,
) {
	live, err := netif.ListLinkIdentities(log)
	if err != nil {
		log.WarnContext(ctx, "ifmgr: listing links to compare rendered .link names failed", "err", err)
		return
	}
	for _, rename := range renamedLinks(changes, specs, live) {
		log.WarnContext(ctx,
			"ifmgr: a rendered .link file asks for a name the link does not carry; the new name takes effect at the next reboot",
			"current", rename.Current, "wanted", rename.Wanted)
	}
}

// renamedLinks pairs each written .link file with the live link its
// specification matches, by driver or by hardware address, and reports the
// pairs whose names differ. A device that is not present yet is not a
// rename: udev reads the new file when it appears.
func renamedLinks(
	changes []networkd.Change,
	specs []networkd.Spec,
	live []netif.LinkIdentity,
) []linkRename {
	byName := make(map[string]networkd.Spec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	var renames []linkRename
	for _, change := range changes {
		if change.Kind != networkd.FileLink || change.Removed {
			continue
		}
		spec, known := byName[change.Interface]
		if !known {
			continue
		}
		for _, link := range live {
			if !matches(spec.Match, link) || link.Name == spec.Name {
				continue
			}
			renames = append(renames, linkRename{Current: link.Name, Wanted: spec.Name})
		}
	}
	return renames
}

// matches reports whether the network manager would match link with the
// given device match: by the driver bound to it, or by its hardware address
// compared without regard to case.
func matches(match networkd.Match, link netif.LinkIdentity) bool {
	if match.Driver != "" {
		return link.Driver == match.Driver
	}
	if match.HardwareAddress != "" {
		return link.HardwareAddress == strings.ToLower(match.HardwareAddress)
	}
	return false
}
