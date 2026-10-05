package provider_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

const (
	releaseTag     = "202610050808-b7-b5778cd"
	buildCommit    = "b5778cd"
	amd64MwanHash  = "44838b2f946a1fce70dcbb93ab0436341551339dc90f647f292c57e845094081"
	arm64MwanHash  = "e993c81897d06a45df3bdebace8b96c76bcfa38aa566181212f44dd29fb46ddb"
	amd64StackHash = "ceba3c5af02239e6f15d8b7dd05f12eaf9570acc8c545096ba0aff22a5c468cc"
	arm64StackHash = "547ee05774f5c9be428ef2cf8c8cdc8fc1fe06e61be6362f84e1b9bb2f681fd6"
)

// releaseServer serves checksums.txt for releaseTag and counts the requests.
func releaseServer(t *testing.T, checksums string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/"+releaseTag+"/checksums.txt" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write([]byte(checksums))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func fullChecksums() string {
	lines := []string{
		amd64MwanHash + "  mwan_linux_amd64.tar.gz",
		arm64MwanHash + "  mwan_linux_arm64.tar.gz",
		amd64StackHash + "  wanconfig-stack_linux_amd64.tar.gz",
		arm64StackHash + "  wanconfig-stack_linux_arm64.tar.gz",
	}
	return strings.Join(lines, "\n") + "\n"
}

func diagnosticText(diagnostics []*tfprotov6.Diagnostic) string {
	var builder strings.Builder
	for _, diagnostic := range diagnostics {
		builder.WriteString(diagnostic.Summary + ": " + diagnostic.Detail + "\n")
	}
	return builder.String()
}

func TestReleaseReadsArchiveAddressesAndHashes(t *testing.T) {
	t.Parallel()
	server, _ := releaseServer(t, fullChecksums())
	provider := newServer(t, buildCommit, server.URL)

	release, diagnostics := provider.readRelease(t, releaseTag)

	if len(diagnostics) > 0 {
		t.Fatalf("read failed: %s", diagnosticText(diagnostics))
	}
	if release.ArchiveMember != "mwan" {
		t.Errorf("archive member = %q, want mwan", release.ArchiveMember)
	}
	want := map[string]archives{
		"amd64": {
			MwanURL:     server.URL + "/" + releaseTag + "/mwan_linux_amd64.tar.gz",
			MwanSHA256:  amd64MwanHash,
			StackURL:    server.URL + "/" + releaseTag + "/wanconfig-stack_linux_amd64.tar.gz",
			StackSHA256: amd64StackHash,
		},
		"arm64": {
			MwanURL:     server.URL + "/" + releaseTag + "/mwan_linux_arm64.tar.gz",
			MwanSHA256:  arm64MwanHash,
			StackURL:    server.URL + "/" + releaseTag + "/wanconfig-stack_linux_arm64.tar.gz",
			StackSHA256: arm64StackHash,
		},
	}
	if len(release.Architectures) != len(want) {
		t.Fatalf("architectures = %v, want %v", release.Architectures, want)
	}
	for name, wantArchives := range want {
		if release.Architectures[name] != wantArchives {
			t.Errorf("architecture %s = %+v, want %+v", name, release.Architectures[name], wantArchives)
		}
	}
}

func TestReleaseRejectsAVersionThatDiffersFromTheBuild(t *testing.T) {
	t.Parallel()
	server, requests := releaseServer(t, fullChecksums())
	provider := newServer(t, "aaaaaaa", server.URL)

	_, diagnostics := provider.readRelease(t, releaseTag)

	if !strings.Contains(diagnosticText(diagnostics), "differs from the provider build") {
		t.Fatalf("diagnostics = %q, want a version mismatch", diagnosticText(diagnostics))
	}
	if requests.Load() != 0 {
		t.Errorf("the data source made %d requests before it rejected the version", requests.Load())
	}
}

func TestReleaseRejectsABuildWithoutAReleaseTag(t *testing.T) {
	t.Parallel()
	server, requests := releaseServer(t, fullChecksums())
	provider := newServer(t, "", server.URL)

	_, diagnostics := provider.readRelease(t, releaseTag)

	if !strings.Contains(diagnosticText(diagnostics), "no release version") {
		t.Fatalf("diagnostics = %q, want an unstamped build error", diagnosticText(diagnostics))
	}
	if requests.Load() != 0 {
		t.Errorf("the data source made %d requests", requests.Load())
	}
}

func TestReleaseFailsWhenChecksumsOmitAnArchive(t *testing.T) {
	t.Parallel()
	incomplete := amd64MwanHash + "  mwan_linux_amd64.tar.gz\n"
	server, _ := releaseServer(t, incomplete)
	provider := newServer(t, buildCommit, server.URL)

	_, diagnostics := provider.readRelease(t, releaseTag)

	if !strings.Contains(diagnosticText(diagnostics), "wanconfig-stack_linux_amd64.tar.gz") {
		t.Fatalf("diagnostics = %q, want the missing archive named", diagnosticText(diagnostics))
	}
}

func TestRoleWANListsFilesUnitsAndModules(t *testing.T) {
	t.Parallel()
	provider := newServer(t, buildCommit, "")

	role, diagnostics := provider.readRole(t, "wan")

	if len(diagnostics) > 0 {
		t.Fatalf("read failed: %s", diagnosticText(diagnostics))
	}
	paths := make(map[string]string, len(role.Files))
	for _, file := range role.Files {
		paths[file.Path] = file.Mode
	}
	for _, wantPath := range []string{
		"/etc/systemd/system/mwan-ifmgr@.service",
		"/etc/sysctl.d/99-quiet-console.conf",
		"/etc/sysrepo-nacm-anonymous.xml",
	} {
		if paths[wantPath] != "0644" {
			t.Errorf("file %s has mode %q, want 0644", wantPath, paths[wantPath])
		}
	}
	if len(role.EnableUnits) < 2 || role.EnableUnits[1] != "mwan-ifmgr@wan.service" {
		t.Errorf("enable units = %v, want the wan instance second", role.EnableUnits)
	}
	var natFeatures []string
	for _, module := range role.Modules {
		if strings.HasPrefix(module.File, "ietf-nat@") {
			natFeatures = module.Features
			if !strings.HasPrefix(module.Path, "/usr/local/share/wanconfig/yang/") {
				t.Errorf("module path = %q, want the wanconfig schema directory", module.Path)
			}
		}
	}
	if len(natFeatures) != 4 {
		t.Errorf("ietf-nat features = %v, want the four translation features", natFeatures)
	}
}

func TestRoleHostHasNoYangModules(t *testing.T) {
	t.Parallel()
	provider := newServer(t, buildCommit, "")

	role, diagnostics := provider.readRole(t, "host")

	if len(diagnostics) > 0 {
		t.Fatalf("read failed: %s", diagnosticText(diagnostics))
	}
	if len(role.Modules) != 0 {
		t.Errorf("host role lists %d YANG modules, want none", len(role.Modules))
	}
	if len(role.Files) != 1 || role.Files[0].Path != "/etc/systemd/system/mwan-ifmgr.service" {
		t.Errorf("host role files = %v, want the single interface manager unit", role.Files)
	}
}

func TestRoleRejectsAnUnknownRole(t *testing.T) {
	t.Parallel()
	provider := newServer(t, buildCommit, "")

	_, diagnostics := provider.readRole(t, "gateway")

	if len(diagnostics) == 0 {
		t.Fatal("an unknown role was accepted")
	}
}
