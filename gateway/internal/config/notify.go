package config

import (
	"time"

	"goodkind.io/mwan/internal/notify"
)

// NotifyConfig uses IfMgr.Alerts.RepeatEvery when Notify.RepeatEvery is
// empty. NotifyConfig uses IfMgr.Alerts.PerModule when Notify.PerKind is
// empty. Invalid durations do not trigger fallback.
func (c *Config) NotifyConfig() notify.Config {
	if c == nil {
		return notify.Config{RepeatEvery: 0, PerKind: nil}
	}
	var repeat time.Duration
	if c.Notify.RepeatEvery != "" {
		if d, err := time.ParseDuration(c.Notify.RepeatEvery); err == nil {
			repeat = d
		}
	} else if c.IfMgr.Alerts.RepeatEvery != "" {
		if d, err := time.ParseDuration(c.IfMgr.Alerts.RepeatEvery); err == nil {
			repeat = d
		}
	}
	source := c.Notify.PerKind
	if len(source) == 0 {
		source = c.IfMgr.Alerts.PerModule
	}
	if len(source) == 0 {
		return notify.Config{RepeatEvery: repeat, PerKind: nil}
	}
	perKind := make(map[string]time.Duration, len(source))
	for kind, raw := range source {
		if raw == "" {
			continue
		}
		if d, err := time.ParseDuration(raw); err == nil {
			perKind[kind] = d
		}
	}
	return notify.Config{RepeatEvery: repeat, PerKind: perKind}
}
