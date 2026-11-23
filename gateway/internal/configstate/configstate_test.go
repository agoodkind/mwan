package configstate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/mwan/internal/configstate"
)

func TestReadHashesConfigurationAndManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "etc/mwan/network.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	data := []byte("{}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	state := configstate.Read(root)
	fileHash := sha256.Sum256(data)
	expectedManifest := hex.EncodeToString(fileHash[:]) + "  " + path + "\n"
	if state.ConfigManifest != expectedManifest {
		t.Fatalf("manifest=%q, want %q", state.ConfigManifest, expectedManifest)
	}

	composite := sha256.New()
	_, _ = composite.Write([]byte(path))
	_, _ = composite.Write([]byte{0})
	_, _ = composite.Write(data)
	expectedHash := hex.EncodeToString(composite.Sum(nil))
	if state.ConfigHash != expectedHash {
		t.Fatalf("hash=%q, want %q", state.ConfigHash, expectedHash)
	}

	if err := os.WriteFile(path, []byte("{\"changed\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	updated := configstate.Read(root)
	if updated.ConfigHash == state.ConfigHash || updated.ConfigManifest == state.ConfigManifest {
		t.Fatalf(
			"configuration edit did not change hash and manifest: before=%+v after=%+v",
			state,
			updated,
		)
	}
}
