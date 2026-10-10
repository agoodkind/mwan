package bgp

import "goodkind.io/mwan/internal/bgpsession"

type (
	// SessionConfig is the complete configuration of one external BGP session.
	SessionConfig = bgpsession.Config
	// ImportRule accepts learned IPv6 prefixes inside one prefix.
	ImportRule = bgpsession.ImportRule
	// ExportRule configures a session to originate one IPv6 prefix.
	ExportRule = bgpsession.ExportRule
	// ExportMode selects the condition under which a session originates an export prefix.
	ExportMode = bgpsession.ExportMode
	// Community is one standard BGP community in ASN:value form.
	Community = bgpsession.Community
	// LargeCommunity is one large BGP community in global-administrator:local-data-1:local-data-2 form.
	LargeCommunity = bgpsession.LargeCommunity
)

const (
	// ExportAlways originates the prefix whenever the session is eligible.
	ExportAlways = bgpsession.ExportAlways
	// ExportBackup originates the prefix only while the session is eligible
	// and the backup path is active.
	ExportBackup = bgpsession.ExportBackup
)
