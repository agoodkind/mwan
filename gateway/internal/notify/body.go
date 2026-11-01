package notify

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// dropKeys are slog attrs that already appear in the email subject or
// in the host-snapshot footer rendered by send-email, so they are
// stripped from the body to avoid duplication.
var dropKeys = map[string]struct{}{
	"level":        {},
	"module":       {},
	"time":         {},
	"caller":       {},
	"alert_kind":   {},
	"alert_key":    {},
	"resolved":     {},
	"transition":   {},
	"last_failure": {},
}

// buildKeys collapse into a single Build: line.
var buildKeys = map[string]struct{}{
	"build":   {},
	"commit":  {},
	"dirty":   {},
	"binhash": {},
}

// whereKeys collapse into a single Where: line in this order.
var whereKeys = []string{"iface", "role", "daemon", "phase"}

// BuildEmailBody renders a tight body for the alert email above the
// host-snapshot footer that send-email appends downstream. boundAttrs
// represents attrs already bound on the handler (e.g. via WithAttrs)
// and are merged with the record's per-call attrs; per-call attrs win
// on key collision.
func BuildEmailBody(r slog.Record, boundAttrs []slog.Attr) string {
	merged := mergeAttrs(boundAttrs, r)

	var sections []string
	sections = append(sections, r.Message)
	if observations := buildObservations(merged, recordResolved(r)); observations != "" {
		sections = append(sections, observations)
	}

	if what := buildWhat(merged); what != "" {
		sections = append(sections, what)
	}
	if where := buildWhere(merged); where != "" {
		sections = append(sections, where)
	}
	if traceBuild := buildTraceLine(merged); traceBuild != "" {
		sections = append(sections, traceBuild)
	}
	if extras := buildExtras(merged); extras != "" {
		sections = append(sections, extras)
	}
	r.Attrs(func(attr slog.Attr) bool {
		if attr.Key != "last_failure" || attr.Value.Kind() != slog.KindGroup {
			return true
		}
		previous := slog.NewRecord(time.Time{}, r.Level, "", 0)
		current := mergeAttrs(boundAttrs, r)
		for _, field := range attr.Value.Group() {
			if field.Key == "message" {
				previous.Message = field.Value.String()
				continue
			}
			if value, exists := current[field.Key]; exists && value == emailValue(field.Value) {
				continue
			}
			previous.AddAttrs(field)
		}
		sections = append(sections, "Last failed observation:\n"+BuildEmailBody(previous, nil))
		return true
	})

	return strings.Join(sections, "\n\n")
}

// mergeAttrs returns a single ordered map keyed by attr name. Per-record
// attrs override pre-bound handler attrs because the call site is more
// specific than the bound context.
func mergeAttrs(bound []slog.Attr, r slog.Record) map[string]string {
	out := make(map[string]string, r.NumAttrs()+len(bound))
	for _, a := range bound {
		if _, drop := dropKeys[a.Key]; drop {
			continue
		}
		out[a.Key] = emailValue(a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		if _, drop := dropKeys[a.Key]; drop {
			return true
		}
		out[a.Key] = emailValue(a.Value)
		return true
	})
	return out
}

func emailValue(value slog.Value) string {
	if value.Kind() == slog.KindTime {
		return value.Time().Format("2006-01-02 15:04:05 MST (UTC-07:00)")
	}
	if value.Kind() == slog.KindDuration {
		return formatDuration(value.Duration())
	}
	return value.String()
}

func formatDuration(duration time.Duration) string {
	if duration > 0 && duration < time.Second {
		return "less than 1 second"
	}
	remaining := duration.Round(time.Second)
	var parts []string
	for _, unit := range []struct {
		duration time.Duration
		label    string
	}{
		{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}, {time.Second, "second"},
	} {
		count := remaining / unit.duration
		remaining %= unit.duration
		if count == 0 {
			continue
		}
		label := unit.label
		if count != 1 {
			label += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, label))
	}
	if len(parts) == 0 {
		return "0 seconds"
	}
	return strings.Join(parts, ", ")
}

func recordResolved(record slog.Record) bool {
	resolved := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "resolved" {
			resolved = attr.Value.Bool()
		}
		return true
	})
	return resolved
}

func buildObservations(attrs map[string]string, resolved bool) string {
	observationLabel := "Current observation"
	if resolved {
		observationLabel = "Recovery observed"
	}
	var lines []string
	for _, item := range []struct{ key, label string }{
		{"first_failure_observed", "First failure observed"},
		{"last_failure_observed", "Latest failure observed"},
		{"observed_at", observationLabel},
		{"incident_duration", "Observed duration"},
	} {
		if value, ok := attrs[item.key]; ok {
			lines = append(lines, item.label+": "+value)
			delete(attrs, item.key)
		}
	}
	return strings.Join(lines, "\n")
}

// Command error output appears separately from the main error detail.
func buildWhat(attrs map[string]string) string {
	errVal, ok := attrs["err"]
	if !ok {
		return ""
	}
	delete(attrs, "err")
	main, stderrPart := splitStderr(errVal)
	if stderrPart == "" {
		return "Error: " + main
	}
	return "Error: " + main + "\nCommand error output: " + stderrPart
}

// splitStderr extracts a stderr="..." (or unquoted stderr=...) fragment
// from an error string. Returns (mainPart, stderrPart) where stderrPart
// is empty if no stderr fragment is present. mainPart has the trailing
// stderr=... slice removed and any trailing parenthesis or whitespace
// trimmed.
func splitStderr(err string) (string, string) {
	before, after, ok := strings.Cut(err, "stderr=")
	if !ok {
		return err, ""
	}
	main := strings.TrimRight(strings.TrimSpace(before), "(,: ")
	rest := strings.TrimSpace(after)
	rest = strings.TrimSuffix(rest, ")")
	if strings.HasPrefix(rest, "\"") {
		closing := strings.LastIndex(rest, "\"")
		if closing > 0 {
			return main, strings.TrimSpace(rest[1:closing])
		}
	}
	return main, rest
}

// Operational context precedes build and diagnostic metadata.
func buildWhere(attrs map[string]string) string {
	var parts []string
	for _, k := range whereKeys {
		v, ok := attrs[k]
		if !ok {
			continue
		}
		parts = append(parts, titleKey(k)+": "+v)
		delete(attrs, k)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// buildTraceLine renders Trace: <id>    Build: <commit> (dirty). Either
// half is optional and the line collapses to a single field if only one
// is present. Returns empty if neither is present.
func buildTraceLine(attrs map[string]string) string {
	trace, hasTrace := attrs["trace"]
	if hasTrace {
		delete(attrs, "trace")
	}
	build := buildSummary(attrs)
	switch {
	case hasTrace && build != "":
		return "Trace:   " + trace + "    Build: " + build
	case hasTrace:
		return "Trace:   " + trace
	case build != "":
		return "Build:   " + build
	default:
		return ""
	}
}

// buildSummary collapses (build, commit, dirty, binhash) into a compact
// string and removes those keys from attrs. The expected mwan attrs are
// commit (short SHA) and dirty ("clean"|"dirty"); build is sometimes
// used as an alias for commit. binhash is appended in plain form if
// present.
func buildSummary(attrs map[string]string) string {
	commit := firstNonEmpty(attrs, "commit", "build")
	dirty := attrs["dirty"]
	binhash := attrs["binhash"]
	for k := range buildKeys {
		delete(attrs, k)
	}
	if commit == "" && dirty == "" && binhash == "" {
		return ""
	}
	var b strings.Builder
	if commit != "" {
		b.WriteString(commit)
	}
	if dirty != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("(" + dirty + ")")
	}
	if binhash != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("binhash=" + binhash)
	}
	return b.String()
}

// firstNonEmpty returns the value for the first key in keys present in
// attrs, or "" if none are.
func firstNonEmpty(attrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := attrs[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

// buildExtras renders any remaining attrs as Key: value lines, sorted
// by key. The buildKeys, whereKeys, dropKeys, and trace/err keys have
// all been removed from attrs by earlier steps, so this only emits
// genuinely extra context.
func buildExtras(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, titleKey(k)+": "+attrs[k])
	}
	return strings.Join(lines, "\n")
}

// Reader labels use spaces while logs retain the original field keys.
func titleKey(k string) string {
	if k == "" {
		return k
	}
	if k == "iface" {
		return "Interface"
	}
	k = strings.ReplaceAll(k, "_", " ")
	return strings.ToUpper(k[:1]) + k[1:]
}
