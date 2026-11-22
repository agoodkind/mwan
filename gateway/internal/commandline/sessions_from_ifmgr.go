package commandline

import (
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/bgpsession"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/bgpsessions"
)

// All BGP sessions share the router ID from [bgp].
func withSessionRouterID(
	settings bgpsessions.Config,
	routerIDText string,
	logger *slog.Logger,
) (bgpsessions.Config, error) {
	if len(settings.Sessions) == 0 {
		return settings, nil
	}

	routerID, err := netip.ParseAddr(routerIDText)
	if err == nil {
		err = bgpsession.ValidateRouterID(routerID)
	}

	if err != nil {
		logger.Warn("ifmgr: BGP session router ID rejected", "router_id", routerIDText, "err", err)

		return settings, fmt.Errorf(
			"invalid [bgp] router_id %q for external bgp sessions: %w",
			routerIDText,
			err,
		)
	}

	sessions := make([]bgpsessions.Session, 0, len(settings.Sessions))
	for _, session := range settings.Sessions {
		session.Config.RouterID = routerID
		sessions = append(sessions, session)
	}

	settings.Sessions = sessions

	return settings, nil
}

func bgpSessionsFromConfig(cfg *config.Config, name string) []config.BGPSession {
	if cfg == nil {
		return nil
	}

	return cfg.IfMgr.WAN[name].BGPSessions
}
