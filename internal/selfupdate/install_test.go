package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

func fixture(t *testing.T) (Manager, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := Manager{BinDir: filepath.Join(dir, "bin")}
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("first executable"), 0755); err != nil {
		t.Fatal(err)
	}
	return m, source
}

func TestLifecycleAndRecovery(t *testing.T) {
	m, source := fixture(t)
	ctx := context.Background()
	first, err := m.Install(ctx, source, "v0.1.0")
	if err != nil || !first.Changed {
		t.Fatalf("%+v %v", first, err)
	}
	if link, err := os.Readlink(filepath.Join(m.BinDir, "gob")); err != nil || link != "gobrew" {
		t.Fatalf("alias %q: %v", link, err)
	}
	r, err := m.read()
	if err != nil {
		t.Fatal(err)
	}
	old := r.Active
	if err := os.WriteFile(source, []byte("second executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, source, "v0.1.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.install(ctx, source, "v0.1.2", old); !errors.Is(err, model.ErrStalePlan) {
		t.Fatalf("stale: %v", err)
	}
	r, err = m.read()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after a durable uninstall intent but before unlinking.
	r.Active = ""
	if err := m.write(r); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gob", "gobrew"} {
		if _, err := os.Lstat(filepath.Join(m.BinDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remaining %s: %v", name, err)
		}
	}
	if _, err := m.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, source, "v0.1.1"); err != nil {
		t.Fatal(err)
	}
}

func TestForeignAliasAndModifiedBinaryArePreserved(t *testing.T) {
	m, source := fixture(t)
	ctx := context.Background()
	if err := m.directories(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(m.BinDir, "gob")
	if err := os.WriteFile(alias, []byte("foreign"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, source, "v0.1.0"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("foreign: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, source, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	r, err := m.read()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(m.root(), "versions", r.Active)
	if err := os.WriteFile(binary, []byte("user change"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Uninstall(ctx); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("modified: %v", err)
	}
	if data, err := os.ReadFile(binary); err != nil || string(data) != "user change" {
		t.Fatalf("lost change: %q %v", data, err)
	}
}

func TestInterruptedPublicationAndForeignStore(t *testing.T) {
	m, source := fixture(t)
	ctx := context.Background()
	if _, err := m.Install(ctx, source, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	before, err := m.read()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new version"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, source, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	after, err := m.read()
	if err != nil {
		t.Fatal(err)
	}
	// The current link is still old after the new active intent was persisted.
	if err := os.Remove(filepath.Join(m.root(), "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("versions/"+before.Active, filepath.Join(m.root(), "current")); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcile(after); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(m.root(), "current"))
	if err != nil || target != "versions/"+after.Active {
		t.Fatalf("recovery: %s %v", target, err)
	}
	foreign, source2 := fixture(t)
	if err := foreign.directories(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign.root(), "keep"), []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Install(ctx, source2, "v0.1.0"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("foreign store: %v", err)
	}
}

func TestLinkChangesRecheckOwnership(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gob")
	if err := os.Symlink("another-tool", path); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"gobrew", ""} {
		if err := setLink(path, target, "gobrew"); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("foreign link: %v", err)
		}
	}
	if target, err := os.Readlink(path); err != nil || target != "another-tool" {
		t.Fatalf("foreign link changed: %s %v", target, err)
	}
}
