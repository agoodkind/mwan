package notify

import (
	"log/slog"
	"sort"
	"strings"
)

// dropKeys are slog attrs that already appear in the email subject or
// in the host-snapshot footer rendered by send-email, so they are
// stripped from the body to avoid duplication.
var dropKeys = map[string]struct{}{
	"level":  {},
	"module": {},
	"time":   {},
	"caller": {},
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
		out[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		if _, drop := dropKeys[a.Key]; drop {
			return true
		}
		out[a.Key] = a.Value.String()
		return true
	})
	return out
}

// buildWhat renders the What: block from the err attr. If err contains
// a stderr=... substring, the stderr is split onto its own line under
// the main message line for readability.
func buildWhat(attrs map[string]string) string {
	errVal, ok := attrs["err"]
	if !ok {
		return ""
	}
	delete(attrs, "err")
	main, stderrPart := splitStderr(errVal)
	if stderrPart == "" {
		return "What:    " + main
	}
	return "What:    " + main + "\n         stderr: " + stderrPart
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

// buildWhere renders the Where: line. Skips any of (iface, role,
// daemon, phase) that are not in attrs.
func buildWhere(attrs map[string]string) string {
	var parts []string
	for _, k := range whereKeys {
		v, ok := attrs[k]
		if !ok {
			continue
		}
		parts = append(parts, k+"="+v)
		delete(attrs, k)
	}
	if len(parts) == 0 {
		return ""
	}
	return "Where:   " + strings.Join(parts, ", ")
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

// titleKey upper-cases the first rune so extra-attr labels read like
// the other section headers. Multi-word keys with underscores are left
// as-is because they tend to be machine identifiers (e.g.
// "remote_addr") and the underscore reads better than space-separated
// capitalization.
func titleKey(k string) string {
	if k == "" {
		return k
	}
	return strings.ToUpper(k[:1]) + k[1:]
}
