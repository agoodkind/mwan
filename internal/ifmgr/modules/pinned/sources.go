package pinned

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
)

// maxFeedBytes bounds the feed body this module reads. The published list is a
// few kilobytes; the bound is what keeps a redirected or replaced URL from
// handing the daemon an unbounded stream.
const maxFeedBytes = 4 << 20

// resolveAll resolves every configured host name in one family and returns one
// host prefix per answer. A name that fails to resolve is logged and skipped,
// so one dead name does not cost the refresh every other name's addresses.
func (m *Module) resolveAll(
	ctx context.Context, log *slog.Logger, fam family, names []string,
) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		addrs, err := m.resolver.LookupNetIP(ctx, fam.network, name)
		if err != nil {
			log.WarnContext(ctx, "pinned: resolve failed; skipping the name",
				"name", name, "family", fam.name, "err", err)
			continue
		}
		for _, addr := range addrs {
			prefix, ok := hostPrefix(addr, fam)
			if !ok {
				continue
			}
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

// fetchFeed reads the published prefix list and splits it by family. A feed
// that cannot be fetched or read contributes nothing and is logged: the pin
// then holds the seeds and the resolved names until the next refresh, which is
// the same degradation the shell refresher had.
func (m *Module) fetchFeed(
	ctx context.Context, log *slog.Logger,
) (prefixesV4, prefixesV6 []netip.Prefix) {
	if m.cfg.FeedURL == "" {
		return nil, nil
	}
	body, ok := m.readFeed(ctx, log)
	if !ok {
		return nil, nil
	}
	prefixes, skipped := parseFeed(body)
	if skipped > 0 {
		log.WarnContext(ctx, "pinned: feed carried lines that are not prefixes",
			"url", m.cfg.FeedURL, "skipped", skipped)
	}
	for _, prefix := range prefixes {
		if familyV4.holds(prefix.Addr()) {
			prefixesV4 = append(prefixesV4, prefix)
			continue
		}
		prefixesV6 = append(prefixesV6, prefix)
	}
	log.DebugContext(ctx, "pinned: feed parsed",
		"url", m.cfg.FeedURL, "v4", len(prefixesV4), "v6", len(prefixesV6))
	return prefixesV4, prefixesV6
}

// readFeed fetches the feed body and reports false where it could not, having
// said in the journal which step failed and why. The refresh continues without
// the feed in that case, so the failure is a logged degradation rather than an
// error the caller has to render a second time.
func (m *Module) readFeed(ctx context.Context, log *slog.Logger) ([]byte, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.FeedURL, nil)
	if err != nil {
		log.WarnContext(ctx, "pinned: feed url is unusable; keeping the other sources",
			"url", m.cfg.FeedURL, "err", err)
		return nil, false
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		log.WarnContext(ctx, "pinned: feed fetch failed; keeping the other sources",
			"url", m.cfg.FeedURL, "err", err)
		return nil, false
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		log.WarnContext(ctx, "pinned: feed answered with an error status; keeping the other sources",
			"url", m.cfg.FeedURL, "status", response.Status)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes))
	if err != nil {
		log.WarnContext(ctx, "pinned: reading the feed body failed; keeping the other sources",
			"url", m.cfg.FeedURL, "err", err)
		return nil, false
	}
	return body, true
}

// parseFeed reads the body as one prefix per line and returns how many lines
// held something else, counting a line too long to scan as one of them. A bare
// address counts as its own single-address prefix, blank lines and lines
// opening with a comment marker are not counted as failures, and nothing about
// the URL decides how the body is read.
func parseFeed(body []byte) ([]netip.Prefix, int) {
	prefixes := make([]netip.Prefix, 0, 64)
	skipped := 0
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix, ok := parseFeedEntry(line)
		if !ok {
			skipped++
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	if err := scanner.Err(); err != nil {
		skipped++
	}
	return prefixes, skipped
}

func parseFeedEntry(line string) (netip.Prefix, bool) {
	if prefix, err := netip.ParsePrefix(line); err == nil {
		return unmapPrefix(prefix).Masked(), true
	}
	addr, err := netip.ParseAddr(line)
	if err != nil {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), true
}
