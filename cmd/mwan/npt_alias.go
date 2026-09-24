package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/wanstate"
)

const hairpinAliasInterval = 15 * time.Second

type aliasTable struct {
	Rows []struct {
		IP string `json:"ip"`
	} `json:"rows"`
}

type aliasResult struct {
	Status string `json:"status"`
}

func syncNPTv6HairpinAlias(ctx context.Context, log *slog.Logger, cfg config.OPNsenseSection, state *wanstate.Store) {
	certificate, err := os.ReadFile(cfg.NPTv6HairpinCAFile)
	if err != nil {
		log.ErrorContext(ctx, "npt: read OPNsense certificate failed", "err", err)
		return
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(certificate)
	if block == nil || !roots.AppendCertsFromPEM(certificate) {
		log.ErrorContext(ctx, "npt: OPNsense certificate configuration invalid", "err", fmt.Errorf("certificate is missing"))
		return
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		log.ErrorContext(ctx, "npt: parse OPNsense certificate failed", "err", err)
		return
	}
	if len(parsed.DNSNames) == 0 {
		log.ErrorContext(ctx, "npt: OPNsense certificate has no DNS name", "err", fmt.Errorf("certificate has no DNS SAN"))
		return
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		err := fmt.Errorf("default HTTP transport is %T", http.DefaultTransport)
		log.ErrorContext(ctx, "npt: unsupported HTTP transport", "err", err)
		return
	}
	transport := defaultTransport.Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: parsed.DNSNames[0]}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	ticker := time.NewTicker(hairpinAliasInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			translations := state.Snapshot().Translation
			if len(translations) == 0 {
				continue
			}
			if err := reconcileNPTv6HairpinAlias(ctx, client, cfg, translations); err != nil {
				log.WarnContext(ctx, "npt: synchronize OPNsense hairpin alias failed", "alias", cfg.NPTv6HairpinAlias, "err", err)
			}
		}
	}
}

func reconcileNPTv6HairpinAlias(ctx context.Context, client *http.Client, cfg config.OPNsenseSection, translations map[string]wanstate.MemberTranslation) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "npt: reconcile hairpin alias failed", "err", resultErr)
		}
	}()
	wanted := make(map[netip.Prefix]bool)
	for _, member := range translations {
		if member.V6.Mode == string(config.TranslationNPTv6) && member.V6.ExternalPrefix.IsValid() {
			wanted[member.V6.ExternalPrefix.Masked()] = true
		}
	}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/api/firewall/alias_util/"
	alias := url.PathEscape(cfg.NPTv6HairpinAlias)
	var current aliasTable
	if err := aliasRequest(ctx, client, cfg, http.MethodGet, endpoint+"list/"+alias, "", &current); err != nil {
		return err
	}
	present := make(map[netip.Prefix]bool, len(current.Rows))
	for _, row := range current.Rows {
		prefix, err := netip.ParsePrefix(row.IP)
		if err != nil {
			return fmt.Errorf("invalid alias entry %q: %w", row.IP, err)
		}
		present[prefix.Masked()] = true
	}
	for _, prefix := range sortedPrefixes(wanted) {
		if present[prefix] {
			continue
		}
		if err := aliasRequest(ctx, client, cfg, http.MethodPost, endpoint+"add/"+alias, prefix.String(), nil); err != nil {
			return err
		}
	}
	for _, prefix := range sortedPrefixes(present) {
		if wanted[prefix] {
			continue
		}
		if err := aliasRequest(ctx, client, cfg, http.MethodPost, endpoint+"delete/"+alias, prefix.String(), nil); err != nil {
			return err
		}
	}
	return nil
}

func sortedPrefixes(prefixes map[netip.Prefix]bool) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(prefixes))
	for prefix := range prefixes {
		result = append(result, prefix)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func aliasRequest(ctx context.Context, client *http.Client, cfg config.OPNsenseSection, method, endpoint, address string, output *aliasTable) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "npt: OPNsense alias request failed", "method", method, "err", resultErr)
		}
	}()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(url.Values{"address": {address}}.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("alias request: %w", err)
	}
	request.SetBasicAuth(cfg.APIKey, cfg.APISecret)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("alias %s: %w", method, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if output != nil {
		if err := decoder.Decode(output); err != nil {
			return fmt.Errorf("decode alias list: %w", err)
		}
		return nil
	}
	var result aliasResult
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode alias result: %w", err)
	}
	if result.Status != "done" {
		return fmt.Errorf("API status %q", result.Status)
	}
	return nil
}
