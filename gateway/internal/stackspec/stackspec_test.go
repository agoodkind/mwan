package stackspec_test

import (
	"errors"
	"testing"

	"goodkind.io/mwan/internal/stackspec"
)

func TestCheckBuiltAcceptsTheListedPackageAndRefusesAnotherVersion(t *testing.T) {
	t.Parallel()
	for _, pkg := range stackspec.Packages() {
		if err := stackspec.CheckBuilt(pkg.Name, pkg.Version, pkg.FileName("arm64"), "arm64"); err != nil {
			t.Errorf("listed package %s refused: %v", pkg.Name, err)
		}
	}

	first := stackspec.Packages()[0]
	err := stackspec.CheckBuilt(first.Name, "9.9.9", first.Name+"_9.9.9_amd64.deb", "amd64")
	if !errors.Is(err, stackspec.ErrMismatch) {
		t.Fatalf("version mismatch error = %v, want %v", err, stackspec.ErrMismatch)
	}
}
