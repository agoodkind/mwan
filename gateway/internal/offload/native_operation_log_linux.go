//go:build linux && cgo && nativeoffload

package offload

import "log/slog"

// LogValue keeps the exclusive owner's operation explicit in structured diagnostics.
func (operation nativeOperation) LogValue() slog.Value {
	switch operation {
	case nativeEnable:
		return slog.StringValue("enable")
	case nativeInstall:
		return slog.StringValue("install")
	case nativeQuery:
		return slog.StringValue("query")
	case nativeRemove:
		return slog.StringValue("remove")
	case nativeDisable:
		return slog.StringValue("disable")
	case nativeShutdown:
		return slog.StringValue("shutdown")
	case nativeRetirement:
		return slog.StringValue("retirement")
	case nativeResume:
		return slog.StringValue("resume")
	case nativeReobserve:
		return slog.StringValue("reobserve")
	case nativeVXLANOpen:
		return slog.StringValue("vxlan-open")
	case nativeVXLANInstall:
		return slog.StringValue("vxlan-install")
	case nativeVXLANQuery:
		return slog.StringValue("vxlan-query")
	case nativeVXLANRemove:
		return slog.StringValue("vxlan-remove")
	case nativeVXLANClose:
		return slog.StringValue("vxlan-close")
	default:
		return slog.IntValue(int(operation))
	}
}
