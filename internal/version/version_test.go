package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	gklogversion "goodkind.io/gklog/version"
)

func TestBuildVersion_NonEmpty(t *testing.T) {
	t.Parallel()
	v := BuildVersion()
	if strings.TrimSpace(v) == "" {
		t.Fatal("BuildVersion() empty")
	}
}

func TestBuildVersion_DirtyFlag(t *testing.T) {
	// gklog stamps Dirty as a boolean word; confirm the -dirty suffix appears
	// for it and for the older spelling a hand build might use.
	orig := gklogversion.Dirty
	defer func() { gklogversion.Dirty = orig }()
	for _, stamp := range []string{"true", "dirty"} {
		gklogversion.Dirty = stamp
		v := BuildVersion()
		if !strings.Contains(v, "-dirty") {
			t.Fatalf("BuildVersion() with Dirty=%q: want -dirty in %q", stamp, v)
		}
	}
}

func TestGitDirty_MapsGklogStamps(t *testing.T) {
	orig := gklogversion.Dirty
	defer func() { gklogversion.Dirty = orig }()
	cases := map[string]string{
		"true": "dirty", "dirty": "dirty",
		"false": "clean", "clean": "clean",
		"": "unknown", "unknown": "unknown", "maybe": "unknown",
	}
	for stamp, want := range cases {
		gklogversion.Dirty = stamp
		if got := GitDirty(); got != want {
			t.Fatalf("GitDirty() with Dirty=%q = %q, want %q", stamp, got, want)
		}
	}
}

func TestGitCommit_ReadsGklogStamp(t *testing.T) {
	orig := gklogversion.Commit
	defer func() { gklogversion.Commit = orig }()
	gklogversion.Commit = "03cf29ac"
	if got := GitCommit(); got != "03cf29ac" {
		t.Fatalf("GitCommit() = %q, want the gklog stamp", got)
	}
	gklogversion.Commit = ""
	if got := GitCommit(); got != "unknown" {
		t.Fatalf("GitCommit() with empty stamp = %q, want unknown", got)
	}
}

func TestBinaryHash_Form(t *testing.T) {
	t.Parallel()
	h := BinaryHash()
	if h == "unknown" {
		return
	}
	if len(h) != 12 {
		t.Fatalf("BinaryHash len=%d want 12 or unknown", len(h))
	}
	for _, r := range h {
		if !unicode.Is(unicode.Hex_Digit, r) {
			t.Fatalf("non-hex in BinaryHash: %q", h)
		}
	}
}

func TestBinaryHashFrom_KnownFile(t *testing.T) {
	t.Parallel()
	// Write a small known file and hash it.
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := binaryHashFrom(p)
	if len(h) != 12 {
		t.Fatalf("got %q want 12-char hex", h)
	}
}

func TestBinaryHashFrom_MissingFile(t *testing.T) {
	t.Parallel()
	h := binaryHashFrom(filepath.Join(t.TempDir(), "nope"))
	if h != "unknown" {
		t.Fatalf("got %q want unknown", h)
	}
}

func TestBinaryHashFrom_Directory(t *testing.T) {
	t.Parallel()
	// Passing a directory as path: os.Open succeeds but io.Copy on a directory
	// returns an error on Linux (is a directory), returns "unknown".
	// On some platforms this might succeed; we just check it doesn't panic.
	_ = binaryHashFrom(t.TempDir())
}

func TestBuildVersionString_ContainsMarkers(t *testing.T) {
	t.Parallel()
	s := BuildVersionString()
	if !strings.Contains(s, "commit=") {
		t.Fatalf("missing commit=: %q", s)
	}
	if !strings.Contains(s, "binhash=") {
		t.Fatalf("missing binhash=: %q", s)
	}
}
