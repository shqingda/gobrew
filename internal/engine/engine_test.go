package engine

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Error("no-op contacted network")
	return nil, errors.New("network forbidden")
}

func ownedRecord(t *testing.T, prefix, name string) model.Record {
	t.Helper()
	keg := filepath.Join(prefix, "Cellar", name, "1")
	if err := os.MkdirAll(keg, 0755); err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 32) + ":" + name
	if err := os.WriteFile(filepath.Join(keg, ".gobrew-owner"), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	return model.Record{Formula: model.Formula{Name: name, Version: "1"}, Keg: keg, Owner: owner, Status: model.Ready}
}

func TestNoopRejectsUnhealthyDependencyClosure(t *testing.T) {
	for _, damage := range []string{"missing-keg", "missing-marker", "symlink-marker", "hardlink-marker", "not-ready", "missing-record"} {
		t.Run(damage, func(t *testing.T) {
			prefix := t.TempDir()
			store := state.Store{Prefix: prefix}
			if err := store.Initialize(); err != nil {
				t.Fatal(err)
			}
			app, lib, leaf := ownedRecord(t, prefix, "app"), ownedRecord(t, prefix, "lib"), ownedRecord(t, prefix, "leaf")
			app.Requested = true
			app.Dependencies = []model.RuntimeDependency{{Name: "lib", Version: "1"}}
			lib.Dependencies = []model.RuntimeDependency{{Name: "leaf", Version: "1"}}
			snap := model.Snapshot{Records: map[string]model.Record{"app": app, "lib": lib, "leaf": leaf}}
			switch damage {
			case "missing-keg":
				if err := os.RemoveAll(leaf.Keg); err != nil {
					t.Fatal(err)
				}
			case "missing-marker", "symlink-marker", "hardlink-marker":
				marker := filepath.Join(leaf.Keg, ".gobrew-owner")
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if damage != "missing-marker" {
					outside := filepath.Join(t.TempDir(), "marker")
					if err := os.WriteFile(outside, []byte(leaf.Owner), 0600); err != nil {
						t.Fatal(err)
					}
					link := os.Symlink
					if damage == "hardlink-marker" {
						link = os.Link
					}
					if err := link(outside, marker); err != nil {
						t.Fatal(err)
					}
				}
			case "not-ready":
				leaf.Status = model.NeedsPostInstall
				snap.Records["leaf"] = leaf
			case "missing-record":
				delete(snap.Records, "leaf")
			}
			if err := store.WithLock(context.Background(), func(tx *state.Session) error { return tx.Save(snap) }); err != nil {
				t.Fatal(err)
			}
			before, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(t.TempDir(), "cache")
			e := New(Config{Prefix: prefix, CacheDir: cache, HTTP: &http.Client{Transport: noNetwork{t}}})
			if _, err := e.Execute(context.Background(), model.Request{Action: "install", Names: []string{"app"}}, nil); !errors.Is(err, model.ErrNeedsRepair) {
				t.Fatalf("damaged dependency reported success: %v", err)
			}
			after, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if after.Generation != before.Generation {
				t.Fatal("health check wrote state")
			}
			if _, err := os.Stat(cache); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("health check created cache")
			}
		})
	}
}

func TestInstalledNoopNeedsNeitherCatalogNorNetwork(t *testing.T) {
	prefix := t.TempDir()
	store := state.Store{Prefix: prefix}
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	rec := ownedRecord(t, prefix, "demo")
	rec.Requested = true
	rec.Formula.Aliases = []string{"demo-alias"}
	err := store.WithLock(context.Background(), func(tx *state.Session) error {
		s, err := tx.Load()
		if err != nil {
			return err
		}
		s.Records["demo"] = rec
		return tx.Save(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Prefix: prefix, CacheDir: filepath.Join(t.TempDir(), "missing"), HTTP: &http.Client{Transport: noNetwork{t}}})
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"demo", "demo-alias", "homebrew/core/demo"} {
		p, err := e.Plan(context.Background(), model.Request{Action: "install", Names: []string{name}})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Packages) != 0 || len(p.Unchanged) != 1 || p.Unchanged[0] != "demo" {
			t.Fatal(p)
		}
	}
	after, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if before.Generation != after.Generation {
		t.Fatal("no-op wrote state")
	}
	if _, err = os.Stat(e.config.CacheDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created cache on no-op")
	}
}

func TestNoopRecoversInterruptedLinkSwitch(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	rec := ownedRecord(t, prefix, "demo")
	rec.Requested = true
	oldTarget, newTarget := filepath.Join(rec.Keg, "bin", "demo"), filepath.Join(prefix, "Cellar", "demo", "2", "bin", "demo")
	rec.Links = map[string]string{"bin/demo": oldTarget}
	link := filepath.Join(prefix, "bin", "demo")
	if err := os.Symlink(newTarget, link); err != nil {
		t.Fatal(err)
	}
	if err := s.WithLock(context.Background(), func(tx *state.Session) error {
		if err := tx.Save(model.Snapshot{Records: map[string]model.Record{"demo": rec}}); err != nil {
			return err
		}
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		return tx.WriteJournal(&state.Journal{Phase: "switching", Before: snap, After: snap, Links: []state.LinkChange{{Path: "bin/demo", Before: &oldTarget, After: &newTarget}}})
	}); err != nil {
		t.Fatal(err)
	}
	e := New(Config{Prefix: prefix, CacheDir: filepath.Join(t.TempDir(), "missing"), HTTP: &http.Client{Transport: noNetwork{t}}})
	req := model.Request{Action: "install", Names: []string{"demo"}}
	before, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Plan(context.Background(), req); !errors.Is(err, model.ErrNeedsRepair) || !errors.Is(err, model.ErrRecoveryRequired) {
		t.Fatalf("plan ignored pending transaction: %v", err)
	}
	afterPlan, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if before.Generation != afterPlan.Generation {
		t.Fatal("planning recovered state implicitly")
	}
	if got, err := os.Readlink(link); err != nil || got != newTarget {
		t.Fatalf("planning changed links: %q %v", got, err)
	}
	result, err := e.Execute(context.Background(), req, nil)
	if err != nil || len(result.Unchanged) != 1 {
		t.Fatalf("recovered no-op: %+v %v", result, err)
	}
	if got, err := os.Readlink(link); err != nil || got != oldTarget {
		t.Fatalf("pending transaction bypassed recovery: %q %v", got, err)
	}
	after, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation <= before.Generation {
		t.Fatal("recovery did not save rollback")
	}
	if after.RecoveryRequired {
		t.Fatal("recovery left a pending transaction")
	}
	if _, err := e.Execute(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	noop, err := s.Load()
	if err != nil || noop.Generation != after.Generation {
		t.Fatal("normal no-op wrote state", err)
	}
	if _, err := os.Stat(e.config.CacheDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery created cache")
	}
}

func TestDoctorReportsRecoveryAndOwnershipWithoutMutation(t *testing.T) {
	for _, damage := range []string{"pending-journal", "missing-marker"} {
		t.Run(damage, func(t *testing.T) {
			prefix := t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			rec := ownedRecord(t, prefix, "demo")
			if err := s.WithLock(context.Background(), func(tx *state.Session) error {
				snap := model.Snapshot{Records: map[string]model.Record{"demo": rec}}
				if err := tx.Save(snap); err != nil {
					return err
				}
				if damage == "pending-journal" {
					return tx.WriteJournal(&state.Journal{Phase: "switching", Before: snap, After: snap})
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if damage == "missing-marker" {
				if err := os.Remove(filepath.Join(rec.Keg, ".gobrew-owner")); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			e := New(Config{Prefix: prefix, CacheDir: filepath.Join(t.TempDir(), "missing"), HTTP: &http.Client{Transport: noNetwork{t}}})
			findings, err := e.Doctor(context.Background(), false)
			if err != nil || len(findings) == 0 {
				t.Fatalf("doctor reported healthy damaged state: %+v %v", findings, err)
			}
			after, err := s.Load()
			if err != nil || after.Generation != before.Generation || after.RecoveryRequired != before.RecoveryRequired {
				t.Fatal("read-only doctor mutated state", err)
			}
		})
	}
}

func TestUninstallProtectsDependentsAndAutoremoveRoots(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	err := s.WithLock(context.Background(), func(tx *state.Session) error {
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		snap.Records["app"] = model.Record{Requested: true, Dependencies: []model.RuntimeDependency{{Name: "lib"}}}
		snap.Records["lib"] = model.Record{}
		snap.Records["orphan"] = model.Record{}
		return tx.Save(snap)
	})
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Prefix: prefix})
	if _, err = e.Uninstall(context.Background(), []string{"lib"}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("removed required library: %v", err)
	}
	names, err := e.Autoremove(context.Background(), true)
	if err != nil || len(names) != 1 || names[0] != "orphan" {
		t.Fatalf("%v %v", names, err)
	}
}

func TestExplicitInstallPromotesDependencyLocally(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	rec := ownedRecord(t, prefix, "demo")
	if err := s.WithLock(context.Background(), func(tx *state.Session) error {
		snap, _ := tx.Load()
		snap.Records["demo"] = rec
		return tx.Save(snap)
	}); err != nil {
		t.Fatal(err)
	}
	e := New(Config{Prefix: prefix, CacheDir: filepath.Join(t.TempDir(), "missing"), HTTP: &http.Client{Transport: noNetwork{t}}})
	req := model.Request{Action: "install", Names: []string{"demo"}}
	if _, err := e.Execute(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Installed()
	if err != nil || !snap.Records["demo"].Requested {
		t.Fatalf("not promoted: %+v %v", snap, err)
	}
	if _, err = e.Execute(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	next, err := e.Installed()
	if err != nil || next.Generation != snap.Generation {
		t.Fatal("already-requested install wrote state")
	}
}
