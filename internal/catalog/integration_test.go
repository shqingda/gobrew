//go:build integration

package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

// Run explicitly with: go test -tags=integration ./internal/catalog
// This verifies the live upstream wire format without changing any installation.
func TestOfficialCatalogRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := Client{Dir: t.TempDir(), Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 15}}
	snapshot, err := c.Update(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jq, err := snapshot.Lookup("jq")
	if err != nil {
		t.Fatal(err)
	}
	if jq.UnsupportedReason != "" || len(jq.Bottles) == 0 {
		t.Fatalf("jq unexpectedly unsupported: %+v", jq)
	}
	loaded, err := c.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != snapshot.Generation || len(loaded.Formulae) != len(snapshot.Formulae) {
		t.Fatal("round trip changed catalog")
	}
	t.Logf("verified and indexed %d official formulae", len(snapshot.Formulae))
}
