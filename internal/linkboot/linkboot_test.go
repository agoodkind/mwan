package linkboot_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/linkboot"
	"goodkind.io/mwan/internal/networkd"
)

func TestRenderedDriverMatchDoesNotConflict(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	owned := interfaceintent.Connection{
		Name:  "enowned0",
		Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind:  interfaceintent.KindPhysical,
			Match: interfaceintent.Match{HardwareAddress: "02:00:5e:00:53:77"},
		},
	}
	rendered := interfaceintent.Connection{
		ID: "enrendered0", Name: "enrendered0", Owner: interfaceintent.OwnerNetworkd,
		Link: &interfaceintent.Link{
			Kind:  interfaceintent.KindPhysical,
			Match: interfaceintent.Match{Driver: "virtio_net"},
		},
	}
	connections := []interfaceintent.Connection{owned, rendered}
	writeRendered := func(match string) {
		t.Helper()
		content := networkd.Marker + "\n[Match]\n" + match + "\n\n[Link]\nName=enrendered0\n"
		path := filepath.Join(directory, networkd.FilePrefix+"-enrendered0.link")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write rendered file: %v", err)
		}
	}
	writeRendered("Driver=virtio_net")
	if err := linkboot.ValidateDir(directory, connections); err != nil {
		t.Fatalf("ValidateDir rejected a rendered driver match: %v", err)
	}
	if _, err := linkboot.WriteDir(directory, connections); err != nil {
		t.Fatalf("WriteDir rejected a rendered driver match: %v", err)
	}
	writeRendered("MACAddress=" + owned.Link.Match.HardwareAddress)
	if err := linkboot.ValidateDir(directory, connections); err == nil ||
		!strings.Contains(err.Error(), "may match the same device") {
		t.Fatalf("rendered file with the owned hardware address returned %v", err)
	}
}

func TestWriteDirManagesOnlyOwnedPhysicalNames(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	connection := interfaceintent.Connection{
		Name:  "enowned0",
		Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind:  interfaceintent.KindPhysical,
			Match: interfaceintent.Match{HardwareAddress: "02:00:5e:00:53:77"},
		},
	}
	changed, err := linkboot.WriteDir(directory, []interfaceintent.Connection{connection})
	if err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed files = %v, want one", changed)
	}
	path := filepath.Join(directory, changed[0])
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if !strings.Contains(string(content), "PermanentMACAddress=02:00:5e:00:53:77\n") ||
		!strings.Contains(string(content), "NamePolicy=\nName=enowned0\n") {
		t.Fatalf("generated link naming = %q", content)
	}
	changed, err = linkboot.WriteDir(directory, []interfaceintent.Connection{connection})
	if err != nil || len(changed) != 0 {
		t.Fatalf("unchanged WriteDir = %v, %v", changed, err)
	}
	connection.Name = "enowned1"
	changed, err = linkboot.WriteDir(directory, []interfaceintent.Connection{connection})
	if err != nil || len(changed) != 1 || changed[0] != filepath.Base(path) {
		t.Fatalf("renamed WriteDir = %v, %v", changed, err)
	}
	if err := os.WriteFile(path, []byte("[Match]\nMACAddress=*\n"), 0o644); err != nil {
		t.Fatalf("replace generated file: %v", err)
	}
	if _, err := linkboot.WriteDir(directory, []interfaceintent.Connection{connection}); err == nil ||
		!strings.Contains(err.Error(), "not managed by mwan") {
		t.Fatalf("foreign file error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove foreign file: %v", err)
	}
	if _, err := linkboot.WriteDir(directory, []interfaceintent.Connection{connection}); err != nil {
		t.Fatalf("restore generated file: %v", err)
	}
	changed, err = linkboot.WriteDir(directory, nil)
	if err != nil || len(changed) != 1 {
		t.Fatalf("prune owned file = %v, %v", changed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned file after prune: %v", err)
	}
	foreignPath := filepath.Join(directory, "05-foreign.link")
	if err := os.WriteFile(foreignPath, []byte("[Match]\nDriver=virtio_net\n[Link]\nName=foreign0\n"), 0o644); err != nil {
		t.Fatalf("write competing file: %v", err)
	}
	if err := linkboot.ValidateDir(directory, []interfaceintent.Connection{connection}); err == nil ||
		!strings.Contains(err.Error(), "may match the same device") {
		t.Fatalf("competing link name error = %v", err)
	}
}
