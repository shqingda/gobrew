package installer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
	"github.com/shqingda/gobrew/internal/testutil"
)

func TestPrunePrefixRemovesOnlyItsOwnLeftovers(t *testing.T) {
	prefix := t.TempDir()
	store := state.Store{Prefix: prefix}
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	keg, owner := testutil.OwnedKeg(t, prefix, "tool", "1")
	missing := filepath.Join(prefix, "Cellar", "gone", "1", "bin", "gone")
	path := func(rel string) string { return filepath.Join(prefix, rel) }
	for _, dir := range []string{"bin", "share/empty/nested", "share/kept", "share/stale", "etc/empty", "var/empty"} {
		if err := os.MkdirAll(path(dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path("share/kept/user.txt"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"bin/stray":        missing,                   // Removed: gobrew's shape, unclaimed.
		"share/stale/link": missing,                   // Removed, and its directory after it.
		"bin/claimed":      missing,                   // Kept: a record claims it, so doctor reports it.
		"bin/foreign":      "/nonexistent/other/tool", // Kept: broken, but not gobrew's.
		"bin/alive":        keg,                       // Kept: resolves.
		"etc/stray":        missing,                   // Kept: configuration is never pruned.
	}
	for rel, target := range links {
		if err := os.Symlink(target, path(rel)); err != nil {
			t.Fatal(err)
		}
	}
	record := model.Record{Formula: model.Formula{Name: "tool", Version: "1"}, Keg: keg, Owner: owner, Status: model.Ready, Links: map[string]string{"bin/claimed": missing}}
	if err := store.WithLock(context.Background(), func(tx *state.Session) error {
		return tx.Save(model.Snapshot{Records: map[string]model.Record{"tool": record}})
	}); err != nil {
		t.Fatal(err)
	}
	want := []model.Removed{
		{Path: path("bin/stray"), Kind: model.BrokenLink},
		{Path: path("share/stale/link"), Kind: model.BrokenLink},
		{Path: path("share/stale"), Kind: model.EmptyDirectory},
		{Path: path("share/empty/nested"), Kind: model.EmptyDirectory},
		{Path: path("share/empty"), Kind: model.EmptyDirectory},
	}
	i := Installer{Prefix: prefix}

	preview, err := i.PrunePrefix(context.Background(), true)
	if err != nil || !slices.Equal(preview, want) {
		t.Fatalf("dry run = %+v, %v; want %+v", preview, err, want)
	}
	for _, removed := range want {
		if _, err := os.Lstat(removed.Path); err != nil {
			t.Fatalf("dry run removed %s", removed.Path)
		}
	}

	pruned, err := i.PrunePrefix(context.Background(), false)
	if err != nil || !slices.Equal(pruned, want) {
		t.Fatalf("prune = %+v, %v; want %+v", pruned, err, want)
	}
	for _, removed := range want {
		if _, err := os.Lstat(removed.Path); !os.IsNotExist(err) {
			t.Fatalf("kept %s: %v", removed.Path, err)
		}
	}
	for _, rel := range []string{"bin", "share", "share/kept/user.txt", "bin/claimed", "bin/foreign", "bin/alive", "etc/stray", "etc/empty", "var/empty"} {
		if _, err := os.Lstat(path(rel)); err != nil {
			t.Fatalf("pruned %s: %v", rel, err)
		}
	}
	if again, err := i.PrunePrefix(context.Background(), false); err != nil || len(again) != 0 {
		t.Fatalf("second prune = %+v, %v", again, err)
	}
}
