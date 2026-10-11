// Package configstate computes configuration fingerprints for the agent
// RPC and gateway commands.
package configstate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// State uses LastDeployEpoch 0 for missing or unparsable deploy files.
type State struct {
	ConfigHash      string `json:"config_hash"`
	ConfigManifest  string `json:"config_manifest"`
	LastDeployEpoch int64  `json:"last_deploy_epoch"`
}

// Read hashes file paths and contents, skipping unreadable files.
// An empty root uses absolute gateway paths.
func Read(root string, deployFile []byte) State {
	composite := sha256.New()
	var manifest strings.Builder
	for _, p := range criticalPaths(root) {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		fileSum := hex.EncodeToString(sum[:])
		manifest.WriteString(fileSum)
		manifest.WriteString("  ")
		manifest.WriteString(p)
		manifest.WriteByte('\n')
		_, _ = composite.Write([]byte(p))
		_, _ = composite.Write([]byte{0})
		_, _ = composite.Write(data)
	}
	deployEpoch, _ := strconv.ParseInt(strings.TrimSpace(string(deployFile)), 10, 64)

	return State{
		ConfigHash:      hex.EncodeToString(composite.Sum(nil)),
		ConfigManifest:  manifest.String(),
		LastDeployEpoch: deployEpoch,
	}
}

// Expanding globs on every read includes newly created configuration files.
func criticalPaths(root string) []string {
	var paths []string
	add := func(p string) { paths = append(paths, p) }
	add(filepath.Join(root, "/etc/mwan/mwan.env"))
	add(filepath.Join(root, "/etc/mwan/config.toml"))
	add(filepath.Join(root, "/etc/mwan/network.json"))
	add(filepath.Join(root, "/etc/iproute2/rt_tables"))
	add(filepath.Join(root, "/etc/sysctl.d/99-mwan.conf"))
	add(filepath.Join(root, "/etc/wpa_supplicant/wpa_supplicant.conf"))
	for _, g := range []string{
		"/etc/systemd/network/*",
		"/etc/networkd-dispatcher/routable.d/*",
	} {
		matches, err := filepath.Glob(filepath.Join(root, g))
		if err != nil {
			continue
		}
		for _, m := range matches {
			st, err := os.Stat(m)
			if err != nil || st.IsDir() {
				continue
			}
			add(m)
		}
	}
	sort.Strings(paths)
	return paths
}
