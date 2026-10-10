package config

import (
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/watchdogconfig"
)

// NotifyConfig uses IfMgr.Alerts.RepeatEvery when Notify.RepeatEvery is
// empty. NotifyConfig uses IfMgr.Alerts.PerModule when Notify.PerKind is
// empty. Invalid durations do not trigger fallback.
func (c *Config) NotifyConfig() notify.Config {
	if c == nil {
		return notify.Config{RepeatEvery: 0, PerKind: nil}
	}
	return watchdogconfig.NotifyCadence(c.Notify, c.IfMgr.Alerts)
}
