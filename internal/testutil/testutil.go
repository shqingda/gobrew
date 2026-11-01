// Package testutil holds fixtures shared by tests of several packages. Only
// _test.go files import it.
package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

// OwnedKeg creates prefix/Cellar/name/version with an ownership marker, as an
// earlier gobrew transaction would leave it, and returns the keg and owner.
func OwnedKeg(t *testing.T, prefix, name, version string) (keg, owner string) {
	t.Helper()
	keg = filepath.Join(prefix, "Cellar", name, version)
	if err := os.MkdirAll(keg, 0755); err != nil {
		t.Fatal(err)
	}
	owner = strings.Repeat("a", 32) + ":" + name
	if err := os.WriteFile(filepath.Join(keg, model.OwnerMarker), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	return keg, owner
}
