package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/wanstate"
)

func TestNPTv6HairpinAliasReconcilesPrefixes(t *testing.T) {
	t.Parallel()
	entries := make(map[string]bool)
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		user, password, ok := request.BasicAuth()
		if !ok || user != "key" || password != "secret" {
			t.Error("missing OPNsense API authentication")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasSuffix(request.URL.Path, "/mwan_npt_hairpin_sources") {
			t.Errorf("unexpected alias path %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(request.URL.Path, "/list/") && request.Method == http.MethodGet:
			rows := make([]map[string]string, 0, len(entries))
			for address := range entries {
				rows = append(rows, map[string]string{"ip": address})
			}
			if err := json.NewEncoder(writer).Encode(map[string]any{"rows": rows}); err != nil {
				t.Error(err)
			}
		case request.Method == http.MethodPost:
			if err := request.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			address := request.PostForm.Get("address")
			if strings.Contains(request.URL.Path, "/add/") {
				entries[address] = true
				operations = append(operations, "add "+address)
			} else if strings.Contains(request.URL.Path, "/delete/") {
				delete(entries, address)
				operations = append(operations, "delete "+address)
			} else {
				t.Errorf("unexpected POST path %s", request.URL.Path)
			}
			if err := json.NewEncoder(writer).Encode(aliasResult{Status: "done"}); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	cfg := config.OPNsenseSection{URL: server.URL, APIKey: "key", APISecret: "secret", NPTv6HairpinAlias: "mwan_npt_hairpin_sources"}
	translations := map[string]wanstate.MemberTranslation{
		"att":     {V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6), ExternalPrefix: netip.MustParsePrefix("2001:db8:100::/60")}},
		"webpass": {V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6), ExternalPrefix: netip.MustParsePrefix("2001:db8:200::/60")}},
	}
	check := func(want ...string) {
		t.Helper()
		if err := reconcileNPTv6HairpinAlias(context.Background(), server.Client(), cfg, translations); err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(entries))
		for address := range entries {
			got = append(got, address)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("OPNsense alias entries = %v, want %v", got, want)
		}
	}
	check("2001:db8:100::/60", "2001:db8:200::/60")
	translations["att"] = wanstate.MemberTranslation{V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6), ExternalPrefix: netip.MustParsePrefix("2001:db8:300::/60")}}
	check("2001:db8:200::/60", "2001:db8:300::/60")
	translations["att"] = wanstate.MemberTranslation{V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6)}}
	check("2001:db8:200::/60")
	delete(entries, "2001:db8:200::/60")
	check("2001:db8:200::/60")
	addCount := 0
	for _, operation := range operations {
		if operation == "add 2001:db8:200::/60" {
			addCount++
		}
	}
	if addCount != 2 {
		t.Fatalf("router restart restored prefix %d times, want two total additions: %v", addCount, operations)
	}
}
