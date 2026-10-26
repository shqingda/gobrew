package artifact

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/platform"
)

func TestCleanupRemovesOnlyUnleasedIdentifiableTemporaryArtifacts(t *testing.T) {
	cache := t.TempDir()
	m := New(Config{CacheDir: cache, BlobBudget: -1, TreeBudget: -1})
	orphan, active := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var orphanPaths, activePaths, retained []string
	var releases []func() error
	for _, tier := range []struct{ dir, temporary, lock string }{
		{"blobs", ".download-", "blob-"}, {"trees", ".prepare-", "tree-"},
	} {
		dir := filepath.Join(cache, tier.dir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		create := func(name string) string {
			path := filepath.Join(dir, name)
			if tier.dir == "trees" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "partial"), []byte("interrupted"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("interrupted"), 0600); err != nil {
				t.Fatal(err)
			}
			return path
		}
		orphanPaths = append(orphanPaths, create(tier.temporary+orphan+"-1234567890"))
		activePaths = append(activePaths, create(tier.temporary+active+"-1234567890"))
		for _, name := range []string{tier.temporary + "1234567890", tier.temporary + orphan, tier.temporary + orphan + "-", tier.temporary + orphan + "-unrelated", tier.temporary + orphan + "-9999999999", tier.temporary + orphan + "-0123", tier.temporary + strings.Repeat("g", 64) + "-123"} {
			retained = append(retained, create(name))
		}
		link := filepath.Join(dir, tier.temporary+orphan+"-42")
		if err := os.Symlink("unowned", link); err != nil {
			t.Fatal(err)
		}
		retained = append(retained, link)
		release, err := platform.AcquireLock(context.Background(), filepath.Join(cache, "locks", tier.lock+active))
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
		defer release()
	}
	removed, err := m.Cleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != len(orphanPaths) {
		t.Fatalf("removed=%v, want only %v", removed, orphanPaths)
	}
	for _, path := range orphanPaths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan retained: %s (%v)", path, err)
		}
	}
	for _, path := range append(activePaths, retained...) {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected entry removed: %s (%v)", path, err)
		}
	}
	for _, release := range releases {
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}
	removed, err = m.Cleanup(context.Background())
	if err != nil || len(removed) != len(activePaths) {
		t.Fatalf("released leftovers not reclaimed: %v %v", removed, err)
	}
}
