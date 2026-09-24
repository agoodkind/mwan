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

func syncNPTv6HairpinAlias(ctx context.Context, log *slog.Logger, cfg config.OPNsenseSection, state *wanstate.Store, interval time.Duration) {
	certificate, err := os.ReadFile(cfg.NPTv6HairpinCAFile)
	if err != nil {
		log.ErrorContext(ctx, "npt: read OPNsense certificate failed", "err", err)
		return
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(certificate)
	if block == nil {
		log.ErrorContext(ctx, "npt: OPNsense certificate configuration invalid", "err", fmt.Errorf("no PEM block found in certificate file"))
		return
	}
	if !roots.AppendCertsFromPEM(certificate) {
		log.ErrorContext(ctx, "npt: OPNsense certificate configuration invalid", "err", fmt.Errorf("certificate pool rejected PEM data"))
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
	ticker := time.NewTicker(interval)
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
				continue
			}
		}
	}
}

func reconcileNPTv6HairpinAlias(ctx context.Context, client *http.Client, cfg config.OPNsenseSection, translations map[string]wanstate.MemberTranslation) error {
	wanted := make(map[netip.Prefix]bool)
	for _, member := range translations {
		if member.V6.Mode == string(config.TranslationNPTv6) && member.V6.ExternalPrefix.IsValid() {
			wanted[member.V6.ExternalPrefix.Masked()] = true
		}
	}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/api/firewall/alias_util/"
	alias := url.PathEscape(cfg.NPTv6HairpinAlias)
	current, err := listAlias(ctx, client, cfg, endpoint+"list/"+alias)
	if err != nil {
		return err
	}
	present := make(map[netip.Prefix]bool, len(current.Rows))
	for _, row := range current.Rows {
		prefix, err := netip.ParsePrefix(row.IP)
		if err != nil {
			wrapped := fmt.Errorf("invalid alias entry %q: %w", row.IP, err)
			slog.WarnContext(ctx, "npt: OPNsense hairpin alias contains an invalid prefix", "err", wrapped)
			return wrapped
		}
		present[prefix.Masked()] = true
	}
	for _, prefix := range sortedPrefixes(wanted) {
		if present[prefix] {
			continue
		}
		if err := changeAlias(ctx, client, cfg, endpoint+"add/"+alias, prefix.String()); err != nil {
			return err
		}
	}
	for _, prefix := range sortedPrefixes(present) {
		if wanted[prefix] {
			continue
		}
		if err := changeAlias(ctx, client, cfg, endpoint+"delete/"+alias, prefix.String()); err != nil {
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

func listAlias(ctx context.Context, client *http.Client, cfg config.OPNsenseSection, endpoint string) (aliasTable, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		wrapped := fmt.Errorf("alias list request: %w", err)
		slog.WarnContext(ctx, "npt: create OPNsense alias list request failed", "err", wrapped)
		return aliasTable{}, wrapped
	}
	data, err := sendAliasRequest(client, cfg, request)
	if err != nil {
		return aliasTable{}, err
	}
	var table aliasTable
	if err := json.Unmarshal(data, &table); err != nil {
		wrapped := fmt.Errorf("decode alias list: %w", err)
		slog.WarnContext(ctx, "npt: decode OPNsense alias list failed", "err", wrapped)
		return aliasTable{}, wrapped
	}
	return table, nil
}

func changeAlias(ctx context.Context, client *http.Client, cfg config.OPNsenseSection, endpoint, address string) error {
	body := strings.NewReader(url.Values{"address": {address}}.Encode())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		wrapped := fmt.Errorf("alias change request: %w", err)
		slog.WarnContext(ctx, "npt: create OPNsense alias change request failed", "err", wrapped)
		return wrapped
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	data, err := sendAliasRequest(client, cfg, request)
	if err != nil {
		return err
	}
	var result aliasResult
	if err := json.Unmarshal(data, &result); err != nil {
		wrapped := fmt.Errorf("decode alias change result: %w", err)
		slog.WarnContext(ctx, "npt: decode OPNsense alias change result failed", "err", wrapped)
		return wrapped
	}
	if result.Status != "done" {
		err := fmt.Errorf("API status %q", result.Status)
		slog.WarnContext(ctx, "npt: OPNsense alias change rejected", "err", err)
		return err
	}
	return nil
}

func sendAliasRequest(client *http.Client, cfg config.OPNsenseSection, request *http.Request) (json.RawMessage, error) {
	request.SetBasicAuth(cfg.APIKey, cfg.APISecret)
	response, err := client.Do(request)
	if err != nil {
		wrapped := fmt.Errorf("alias %s: %w", request.Method, err)
		slog.WarnContext(request.Context(), "npt: OPNsense alias request failed", "method", request.Method, "err", wrapped)
		return nil, wrapped
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		err := fmt.Errorf("HTTP %d", response.StatusCode)
		slog.WarnContext(request.Context(), "npt: OPNsense alias request returned an error", "method", request.Method, "err", err)
		return nil, err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	var data json.RawMessage
	if err := decoder.Decode(&data); err != nil {
		wrapped := fmt.Errorf("decode alias response: %w", err)
		slog.WarnContext(request.Context(), "npt: decode OPNsense alias response failed", "method", request.Method, "err", wrapped)
		return nil, wrapped
	}
	return data, nil
}
