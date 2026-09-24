package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/wanstate"
)

func TestNPTv6HairpinAliasReconcilesPrefixes(t *testing.T) {
	entries := make(map[string]bool)
	var operations []string
	var mutex sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
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
	certificateFile := filepath.Join(t.TempDir(), "opnsense.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(certificateFile, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.OPNsenseSection{URL: server.URL, APIKey: "key", APISecret: "secret", NPTv6HairpinAlias: "mwan_npt_hairpin_sources", NPTv6HairpinCAFile: certificateFile}
	state := wanstate.New()
	translations := map[string]wanstate.MemberTranslation{
		"att":     {V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6), ExternalPrefix: netip.MustParsePrefix("2001:db8:100::/60")}},
		"webpass": {V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6), ExternalPrefix: netip.MustParsePrefix("2001:db8:200::/60")}},
	}
	state.SetTranslation(translations)
	ctx, cancel := context.WithCancel(context.Background())
	var aliasSync sync.WaitGroup
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("test HTTPS transport is not *http.Transport")
	}
	startNPTv6HairpinAlias(ctx, logger, cfg, state, &aliasSync, transport, 10*time.Millisecond)
	defer func() {
		cancel()
		aliasSync.Wait()
	}()
	check := func(want ...string) {
		t.Helper()
		slices.Sort(want)
		deadline := time.After(2 * time.Second)
		for {
			mutex.Lock()
			got := make([]string, 0, len(entries))
			for address := range entries {
				got = append(got, address)
			}
			mutex.Unlock()
			slices.Sort(got)
			if slices.Equal(got, want) {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("OPNsense alias entries = %v, want %v", got, want)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	check("2001:db8:100::/60", "2001:db8:200::/60")
	translations["att"] = wanstate.MemberTranslation{V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6), ExternalPrefix: netip.MustParsePrefix("2001:db8:300::/60")}}
	state.SetTranslation(translations)
	check("2001:db8:200::/60", "2001:db8:300::/60")
	translations["att"] = wanstate.MemberTranslation{V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNPTv6)}}
	state.SetTranslation(translations)
	check("2001:db8:200::/60")
	mutex.Lock()
	delete(entries, "2001:db8:200::/60")
	mutex.Unlock()
	check("2001:db8:200::/60")
	mutex.Lock()
	addCount := 0
	for _, operation := range operations {
		if operation == "add 2001:db8:200::/60" {
			addCount++
		}
	}
	operationSnapshot := slices.Clone(operations)
	mutex.Unlock()
	if addCount != 2 {
		t.Fatalf("add 2001:db8:200::/60 operation count = %d, want 2; operations: %v", addCount, operationSnapshot)
	}
}
