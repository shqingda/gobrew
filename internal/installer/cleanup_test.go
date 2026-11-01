package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

func TestCleanupRejectsAliasedOwnershipMarker(t *testing.T) {
	for _, kind := range []string{"Cellar", "backups"} {
		t.Run(kind, func(t *testing.T) {
			prefix := t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			keg := filepath.Join(prefix, "Cellar", "demo", "1")
			if kind == "backups" {
				keg = filepath.Join(prefix, ".gobrew", "backups", "id", "demo")
			}
			if err := os.MkdirAll(keg, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "marker")
			if err := os.WriteFile(marker, []byte(strings.Repeat("a", 32)+":demo"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(marker, filepath.Join(keg, ownerFile)); err != nil {
				t.Fatal(err)
			}
			if _, err := (Installer{Prefix: prefix}).Cleanup(context.Background()); err == nil {
				t.Fatal("accepted aliased ownership")
			}
			if _, err := os.Lstat(keg); err != nil {
				t.Fatal("removed foreign directory", err)
			}
		})
	}
}

func TestCleanupWithLimitsDryRunAndPinsLikeBrew(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	keg := func(name, version string) string {
		t.Helper()
		path := filepath.Join(prefix, "Cellar", name, version)
		if err := os.MkdirAll(filepath.Join(path, "bin"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "bin", name), []byte("12345"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ownerFile), []byte(strings.Repeat("a", 32)+":"+name), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	records := map[string]model.Record{}
	for _, name := range []string{"uv", "jq", "node"} {
		current := keg(name, "2")
		records[name] = model.Record{Formula: model.Formula{Name: name, Version: "2"}, Keg: current, Owner: strings.Repeat("a", 32) + ":" + name, Status: model.Ready, Pinned: name == "node"}
	}
	oldUV, oldJQ, oldNode := keg("uv", "1"), keg("jq", "1"), keg("node", "1")
	if err := s.WithLock(context.Background(), func(tx *state.Session) error { return tx.Save(model.Snapshot{Records: records}) }); err != nil {
		t.Fatal(err)
	}
	in := Installer{Prefix: prefix}
	removed, err := in.CleanupWith(context.Background(), CleanupOptions{Formulae: []string{"uv"}, DryRun: true})
	if err != nil || len(removed) != 1 || removed[0].Path != oldUV || removed[0].Files != 2 {
		t.Fatalf("dry run = %+v, %v", removed, err)
	}
	if _, err := os.Stat(oldUV); err != nil {
		t.Fatal("dry run removed a keg")
	}
	if removed, err = in.CleanupWith(context.Background(), CleanupOptions{Formulae: []string{"uv"}}); err != nil || len(removed) != 1 {
		t.Fatalf("formula cleanup = %+v, %v", removed, err)
	}
	if _, err := os.Stat(oldJQ); err != nil {
		t.Fatal("formula cleanup touched another formula")
	}
	if removed, err = in.CleanupWith(context.Background(), CleanupOptions{Skip: []string{"jq"}}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{oldUV: false, oldJQ: true, oldNode: true, records["uv"].Keg: true} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Fatalf("%s exists = %v, want %v (removed %+v)", path, err == nil, want, removed)
		}
	}
}
