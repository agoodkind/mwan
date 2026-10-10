package watchdogconfig

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
	return NotifyCadence(c.Notify, c.IfMgr.Alerts)
}

// NotifyCadence uses fallback values only when the corresponding setting
// is empty. Invalid durations do not trigger fallback.
func NotifyCadence(section NotifySection, fallback IfMgrAlertsSection) notify.Config {
	var repeat time.Duration
	if section.RepeatEvery != "" {
		if d, err := time.ParseDuration(section.RepeatEvery); err == nil {
			repeat = d
		}
	} else if fallback.RepeatEvery != "" {
		if d, err := time.ParseDuration(fallback.RepeatEvery); err == nil {
			repeat = d
		}
	}
	source := section.PerKind
	if len(source) == 0 {
		source = fallback.PerModule
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
