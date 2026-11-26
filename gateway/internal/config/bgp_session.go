package config

import (
	"net/netip"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/internal/bgpsession"
)

// BGPSession is one configured external BGP session, and BackupFor maps each backup-mode export prefix to the connections that the prefix backs up.
type BGPSession struct {
	Session   bgpsession.Config
	BackupFor map[netip.Prefix][]connectionid.ID
}
