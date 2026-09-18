// Package version reports the running binary's build identity. The identity
// itself is gklog's: goodkind.io/gklog/version carries the stamped Commit,
// Dirty, and BuildTime, written at link time by go-makefile through GKLOG_VPKG
// for local builds and by the release engine for released archives. This
// package adds the one thing the stamp cannot carry, a hash of the binary as
// it sits on disk, and presents both in the forms mwan logs and RPCs use.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	gklogversion "goodkind.io/gklog/version"
)

const unknown = "unknown"

// dirtyStamp is the value gklog's Dirty field carries. The pipeline writes a
// boolean word; the older "clean" and "dirty" spellings are accepted so a
// binary stamped by hand still reports correctly.
type dirtyStamp string

const (
	dirtyStampTrue  dirtyStamp = "true"
	dirtyStampFalse dirtyStamp = "false"
	dirtyStampDirty dirtyStamp = "dirty"
	dirtyStampClean dirtyStamp = "clean"
)

// BuildVersion returns a human-readable build fingerprint of the form:
//
//	<commit>[-dirty]+<binhash>
//
// where <binhash> is the first 12 hex characters of SHA-256 of the running
// binary. This ensures that even an uncommitted build gets a stable,
// log-searchable identifier that matches the file on disk.
func BuildVersion() string {
	var sb strings.Builder
	sb.WriteString(GitCommit())
	if GitDirty() == "dirty" {
		sb.WriteString("-dirty")
	}
	sb.WriteString("+")
	sb.WriteString(BinaryHash())
	return sb.String()
}

// BinaryHash returns the first 12 hex characters of SHA-256 of the running
// binary. Returns "unknown" on any error.
func BinaryHash() string {
	return binaryHashFrom("")
}

// binaryHashFrom hashes the file at path. If path is empty it falls back to
// os.Executable(). Not exported (used internally and by tests).
func binaryHashFrom(path string) string {
	if path == "" {
		var err error
		path, err = os.Executable()
		if err != nil {
			return unknown
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return unknown
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return unknown
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// BuildVersionString returns a full one-line summary for startup logs.
func BuildVersionString() string {
	return fmt.Sprintf("commit=%s dirty=%s binhash=%s", GitCommit(), GitDirty(), BinaryHash())
}

// GitCommit returns the stamped git commit, or "unknown".
func GitCommit() string {
	return stampedOrUnknown(gklogversion.Commit)
}

// GitDirty returns "clean", "dirty", or "unknown".
func GitDirty() string {
	switch dirtyStamp(gklogversion.Dirty) {
	case dirtyStampTrue, dirtyStampDirty:
		return "dirty"
	case dirtyStampFalse, dirtyStampClean:
		return "clean"
	default:
		return unknown
	}
}

// stampedOrUnknown normalizes an unstamped value: gklog's own default and an
// empty string both mean the field was never written.
func stampedOrUnknown(value string) string {
	if value == "" {
		return unknown
	}
	return value
}
