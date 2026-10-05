// Package release resolves the files of one mwan GitHub release. The asset
// names and the repository are the release contract of the mwan pipeline.
package release

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const (
	// Repository is the GitHub repository that publishes the mwan release.
	Repository = "agoodkind/mwan"
	// DefaultBaseURL is the GitHub download root of Repository. A release asset
	// is at <base>/<tag>/<asset>.
	DefaultBaseURL = "https://github.com/" + Repository + "/releases/download"
	// ChecksumsAsset lists the SHA-256 of every archive of the release.
	ChecksumsAsset = "checksums.txt"
	// ArchiveMember is the name of the binary inside the mwan archive.
	ArchiveMember = "mwan"

	// MwanAssetPattern is the name of the mwan archive for one architecture.
	MwanAssetPattern = "mwan_linux_%s.tar.gz"
	// StackAssetPattern is the name of the wanconfig stack archive for one
	// architecture.
	StackAssetPattern = "wanconfig-stack_linux_%s.tar.gz"

	// ArchitectureAMD64 is the amd64 architecture name in asset names.
	ArchitectureAMD64 = "amd64"
	// ArchitectureARM64 is the arm64 architecture name in asset names.
	ArchitectureARM64 = "arm64"

	sha256HexLength  = 64
	checksumFields   = 2
	maxChecksumsSize = 1 << 20
)

// Asset is one downloadable archive and its SHA-256 as lowercase hex.
type Asset struct {
	URL    string
	SHA256 string
}

// Architecture is the pair of archives the release ships for one architecture.
type Architecture struct {
	Mwan  Asset
	Stack Asset
}

// Architectures lists the architectures the release ships.
func Architectures() []string {
	return []string{ArchitectureAMD64, ArchitectureARM64}
}

// CommitOf returns the commit of a release tag. The release pipeline names a
// release <timestamp>-<run>-<short commit>, and stamps the same short commit
// into the binaries it builds.
func CommitOf(tag string) (string, error) {
	index := strings.LastIndex(tag, "-")
	if index < 0 || index == len(tag)-1 {
		return "", fmt.Errorf("release tag %q does not end in -<commit>", tag)
	}
	return tag[index+1:], nil
}

// Resolve downloads checksums.txt of the release and returns the archives of
// every architecture. A release that omits one of the archives fails.
func Resolve(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	version string,
) (map[string]Architecture, error) {
	checksumsURL, err := url.JoinPath(baseURL, version, ChecksumsAsset)
	if err != nil {
		slog.ErrorContext(ctx, "build of the checksums URL failed", "base", baseURL, "err", err)
		return nil, fmt.Errorf("build the URL of %s: %w", ChecksumsAsset, err)
	}
	checksums, err := fetchChecksums(ctx, client, checksumsURL)
	if err != nil {
		return nil, err
	}

	result := make(map[string]Architecture, len(Architectures()))
	for _, architecture := range Architectures() {
		mwan, err := resolveAsset(baseURL, version, fmt.Sprintf(MwanAssetPattern, architecture), checksums)
		if err != nil {
			return nil, err
		}
		stack, err := resolveAsset(baseURL, version, fmt.Sprintf(StackAssetPattern, architecture), checksums)
		if err != nil {
			return nil, err
		}
		result[architecture] = Architecture{Mwan: mwan, Stack: stack}
	}
	return result, nil
}

func resolveAsset(baseURL string, version string, name string, checksums map[string]string) (Asset, error) {
	hash, found := checksums[name]
	if !found {
		return Asset{}, fmt.Errorf("%s of release %s has no entry for %s", ChecksumsAsset, version, name)
	}
	assetURL, err := url.JoinPath(baseURL, version, name)
	if err != nil {
		slog.Error("build of the asset URL failed", "base", baseURL, "asset", name, "err", err)
		return Asset{}, fmt.Errorf("build the URL of %s: %w", name, err)
	}
	return Asset{URL: assetURL, SHA256: hash}, nil
}

func fetchChecksums(ctx context.Context, client *http.Client, checksumsURL string) (map[string]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		slog.ErrorContext(ctx, "build of the checksums request failed", "url", checksumsURL, "err", err)
		return nil, fmt.Errorf("build the request for %s: %w", checksumsURL, err)
	}
	response, err := client.Do(request)
	if err != nil {
		slog.ErrorContext(ctx, "download of the release checksums failed", "url", checksumsURL, "err", err)
		return nil, fmt.Errorf("download %s: %w", checksumsURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP status %d", checksumsURL, response.StatusCode)
	}
	return parseChecksums(io.LimitReader(response.Body, maxChecksumsSize))
}

// parseChecksums reads the sha256sum format: a hex hash, whitespace, and the
// file name, with an optional leading * on the name for binary mode.
func parseChecksums(reader io.Reader) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != checksumFields {
			return nil, fmt.Errorf("malformed checksum line %q", line)
		}
		hash := strings.ToLower(fields[0])
		if len(hash) != sha256HexLength || strings.Trim(hash, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("checksum line %q has no SHA-256 hash", line)
		}
		checksums[strings.TrimPrefix(fields[1], "*")] = hash
	}
	if err := scanner.Err(); err != nil {
		slog.Error("read of the checksums failed", "err", err)
		return nil, fmt.Errorf("read the checksums: %w", err)
	}
	return checksums, nil
}
