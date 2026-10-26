package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/state"
)

func TestOwnedLinkRecoveryAndForeignConflict(t *testing.T) {
	prefix := t.TempDir()
	before, after := "/old/keg/tool", "/new/keg/tool"
	change := state.LinkChange{Path: "bin/tool", Before: &before, After: &after}
	if err := os.Mkdir(filepath.Join(prefix, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(before, filepath.Join(prefix, "bin/tool")); err != nil {
		t.Fatal(err)
	}
	if err := changeLink(prefix, change, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(prefix, "bin/tool")); got != after {
		t.Fatal(got)
	}
	if err := changeLink(prefix, change, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(prefix, "bin/tool")); got != before {
		t.Fatal(got)
	}
	if err := os.Remove(filepath.Join(prefix, "bin/tool")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin/tool"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := changeLink(prefix, change, false); err == nil {
		t.Fatal("overwrote foreign file")
	}
	b, _ := os.ReadFile(filepath.Join(prefix, "bin/tool"))
	if string(b) != "mine" {
		t.Fatal("lost user data")
	}
}

func TestPrivateDirectoryAliasesCannotRedirectWrites(t *testing.T) {
	for _, kind := range []string{"staging", "backups", "trash"} {
		t.Run(kind, func(t *testing.T) {
			prefix, tree, outside := t.TempDir(), t.TempDir(), t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(tree, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tree, "bin", "demo"), []byte("original"), 0755); err != nil {
				t.Fatal(err)
			}
			inst := Installer{Prefix: prefix}
			prepared := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "demo", Version: "1"}}, Tree: tree}}}
			if kind != "staging" {
				if _, err := inst.Apply(context.Background(), prepared, nil); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			prepared.Plan.StateGeneration = before.Generation
			private := filepath.Join(prefix, ".gobrew", kind)
			if err := os.Remove(private); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, private); err != nil {
				t.Fatal(err)
			}
			if kind == "trash" {
				err = inst.MutateRecords(context.Background(), func(snap *model.Snapshot) error { delete(snap.Records, "demo"); return nil })
			} else {
				_, err = inst.Apply(context.Background(), prepared, nil)
			}
			if err == nil {
				t.Fatal("accepted a symlinked private directory")
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("wrote outside the installation prefix")
			}
			after, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if after.Generation != before.Generation {
				t.Fatal("unsafe operation changed state")
			}
			if kind != "staging" {
				data, err := os.ReadFile(filepath.Join(prefix, "bin", "demo"))
				if err != nil || string(data) != "original" {
					t.Fatalf("changed installed package: %q %v", data, err)
				}
			}
		})
	}
}

func TestRecoveryRejectsPathsOutsidePrefix(t *testing.T) {
	prefix, outside := t.TempDir(), t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 32) + ":demo"
	if err := os.WriteFile(filepath.Join(outside, ownerFile), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.WithLock(context.Background(), func(tx *state.Session) error {
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		return tx.WriteJournal(&state.Journal{Phase: "switching", Before: snap, After: snap, Moves: []state.Move{{From: filepath.Join(prefix, "Cellar", "demo", "1"), To: outside, Owner: owner}}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := (Installer{Prefix: prefix}).Recover(context.Background()); err == nil {
		t.Fatal("recovery accepted an external path")
	}
	if _, err := os.Stat(filepath.Join(outside, ownerFile)); err != nil {
		t.Fatal("recovery moved an external directory")
	}
}

func TestApplyRechecksExistingDependencyOwnership(t *testing.T) {
	prefix, tree := t.TempDir(), t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(prefix, "Cellar", "lib", "1")
	if err := os.MkdirAll(lib, 0755); err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 32) + ":lib"
	if err := os.WriteFile(filepath.Join(lib, ownerFile), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.WithLock(context.Background(), func(tx *state.Session) error {
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		snap.Records["lib"] = model.Record{Formula: model.Formula{Name: "lib", Version: "1"}, Keg: lib, Owner: owner, Status: model.Ready}
		return tx.Save(snap)
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	prepared := model.Prepared{Plan: model.Plan{Prefix: prefix, StateGeneration: snap.Generation}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "app", Version: "1"}}, Tree: tree, RuntimeDependencies: []model.RuntimeDependency{{Name: "lib", Version: "1"}}}}}
	if err := os.RemoveAll(lib); err != nil {
		t.Fatal(err)
	}
	if _, err := (Installer{Prefix: prefix}).Apply(context.Background(), prepared, nil); !errors.Is(err, model.ErrNeedsRepair) {
		t.Fatalf("activated app with a missing dependency: %v", err)
	}
	after, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != snap.Generation {
		t.Fatal("failed activation changed state")
	}
}

func TestPostInstallRejectsMissingTransitiveDependency(t *testing.T) {
	prefix, tree := t.TempDir(), t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	inst := Installer{Prefix: prefix}
	prepared := model.Prepared{Plan: model.Plan{Prefix: prefix}}
	for _, f := range []model.Formula{{Name: "leaf", Version: "1"}, {Name: "lib", Version: "1", Dependencies: []string{"leaf"}}, {Name: "app", Version: "1", Dependencies: []string{"lib"}}} {
		prepared.Packages = append(prepared.Packages, model.PreparedPackage{Package: model.Package{Formula: f}, Tree: tree})
	}
	if _, err := inst.Apply(context.Background(), prepared, nil); err != nil {
		t.Fatal(err)
	}
	before, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(before.Records["leaf"].Keg); err != nil {
		t.Fatal(err)
	}
	if err := inst.PostInstall(context.Background(), []string{"app"}, nil); !errors.Is(err, model.ErrNeedsRepair) {
		t.Fatalf("post-install accepted missing transitive dependency: %v", err)
	}
	after, err := s.Load()
	if err != nil || after.Generation != before.Generation {
		t.Fatal("failed preflight changed state", err)
	}
}

func TestApplyAndRecoveryBoundaries(t *testing.T) {
	for _, phase := range []string{"before-move", "after-move", "partial-links", "after-links", "postinstall"} {
		t.Run(phase, func(t *testing.T) {
			prefix := t.TempDir()
			store := state.Store{Prefix: prefix}
			if err := store.Initialize(); err != nil {
				t.Fatal(err)
			}
			src := t.TempDir()
			if err := os.Mkdir(filepath.Join(src, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "bin", "demo"), []byte("test"), 0755); err != nil {
				t.Fatal(err)
			}
			inst := Installer{Prefix: prefix}
			p := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "demo", Version: "1"}, Requested: true}, Tree: src}}}
			err := store.WithLock(context.Background(), func(tx *state.Session) error {
				snap, err := tx.Load()
				if err != nil {
					return err
				}
				j, err := inst.stage(context.Background(), p, snap)
				if err != nil {
					return err
				}
				if err = tx.WriteJournal(j); err != nil {
					return err
				}
				if phase != "before-move" {
					for _, m := range j.Moves {
						if err = inst.moveKeg(m, false); err != nil {
							return err
						}
					}
				}
				if phase == "partial-links" {
					// A process can stop between link renames and the batch barrier.
					if err = setLink(prefix, j.Links[0], false); err != nil {
						return err
					}
				} else if phase == "after-links" || phase == "postinstall" {
					if err = inst.changeLinks(j.Links, false); err != nil {
						return err
					}
				}
				if phase == "postinstall" {
					j.Phase = "postinstall"
					if err = tx.WriteJournal(j); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = inst.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			snap, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if phase == "postinstall" {
				if snap.Records["demo"].Status != model.NeedsPostInstall {
					t.Fatal(snap)
				}
			} else {
				if len(snap.Records) != 0 {
					t.Fatal(snap)
				}
				if _, err = os.Lstat(filepath.Join(prefix, "bin", "demo")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("link survived rollback: %v", err)
				}
			}
		})
	}
}

func TestLinkBatchBarrierAndFailureRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			prefix, tree := t.TempDir(), t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(tree, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			for n := range 20 {
				if err := os.WriteFile(filepath.Join(tree, "bin", fmt.Sprintf("tool-%d", n)), []byte("tool"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			failure := errors.New("injected directory sync failure")
			calls := 0
			inst := Installer{Prefix: prefix, syncDirectories: func(paths []string) error {
				calls++
				if len(paths) != 2 {
					t.Fatalf("expected bin+opt once for entire batch, got %v", paths)
				}
				if calls == 1 {
					for n := range 20 {
						if _, err := os.Readlink(filepath.Join(prefix, "bin", fmt.Sprintf("tool-%d", n))); err != nil {
							t.Fatal("barrier ran before all links changed", err)
						}
					}
					if fail {
						return failure
					}
				}
				return platform.SyncDirs(paths)
			}}
			p := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "demo", Version: "1"}}, Tree: tree}}}
			_, err := inst.Apply(context.Background(), p, nil)
			if fail != errors.Is(err, failure) || !fail && err != nil {
				t.Fatalf("apply error: %v", err)
			}
			snap, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if snap.RecoveryRequired {
				t.Fatal("completed rollback/commit left journal")
			}
			if fail {
				if calls != 2 || len(snap.Records) != 0 {
					t.Fatalf("sync failure published state or skipped recovery: calls=%d records=%+v", calls, snap.Records)
				}
				for n := range 20 {
					if _, err := os.Lstat(filepath.Join(prefix, "bin", fmt.Sprintf("tool-%d", n))); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("link survived rollback", err)
					}
				}
			} else if calls != 1 || snap.Records["demo"].Status != model.Ready {
				t.Fatalf("batch did not commit once: calls=%d records=%+v", calls, snap.Records)
			}
		})
	}
}

func TestPostinstallRecoveryPreservesPromotionsAndCompletedStatus(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			prefix, tree := t.TempDir(), t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			inst := Installer{Prefix: prefix}
			lib := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "lib", Version: "1"}}, Tree: tree}}}
			if _, err := inst.Apply(context.Background(), lib, nil); err != nil {
				t.Fatal(err)
			}
			err := s.WithLock(context.Background(), func(tx *state.Session) error {
				snap, err := tx.Load()
				if err != nil {
					return err
				}
				app := model.Prepared{Plan: model.Plan{Prefix: prefix, Promote: []string{"lib"}}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "app", Version: "1"}}, Tree: tree}}}
				j, err := inst.stage(context.Background(), app, snap)
				if err != nil {
					return err
				}
				if err = tx.WriteJournal(j); err != nil {
					return err
				}
				if err = inst.switchFiles(j); err != nil {
					return err
				}
				j.Phase = "postinstall"
				if err = tx.WriteJournal(j); err != nil {
					return err
				}
				if completed {
					snap = cloneSnapshot(j.After)
					rec := snap.Records["app"]
					rec.Status, rec.PostInstallComplete = model.Ready, true
					snap.Records["app"] = rec
					return tx.Save(snap)
				}
				return nil // Simulate a crash before publishing j.After.
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = inst.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			snap, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if rec := snap.Records["lib"]; !rec.Requested || rec.Status != model.Ready || !rec.PostInstallComplete {
				t.Fatalf("lost promotion or completed state: %+v", rec)
			}
			want := model.NeedsPostInstall
			if completed {
				want = model.Ready
			}
			if rec := snap.Records["app"]; rec.Status != want || rec.PostInstallComplete != completed {
				t.Fatalf("lost package recovery state: %+v", rec)
			}
		})
	}
}

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

func TestInstallPreservesConfigurationAndRejectsStalePlan(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	for _, d := range []string{"bin", "etc"} {
		if err := os.Mkdir(filepath.Join(tree, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tree, "bin", "demo"), []byte("hello"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "etc", "demo.conf"), []byte("default"), 0644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(prefix, "etc", "demo.conf")
	if err := os.WriteFile(config, []byte("user"), 0644); err != nil {
		t.Fatal(err)
	}
	p := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "demo", Version: "1"}}, Tree: tree}}}
	inst := Installer{Prefix: prefix}
	result, err := inst.Apply(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Succeeded) != 1 {
		t.Fatal(result)
	}
	b, err := os.ReadFile(config)
	if err != nil || string(b) != "user" {
		t.Fatalf("config overwritten: %s %v", b, err)
	}
	if _, err = inst.Apply(context.Background(), p, nil); !errors.Is(err, model.ErrStalePlan) {
		t.Fatalf("want stale plan, got %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	copy(p, "partial")
	return len("partial"), errors.New("input failed")
}

func TestFailedConfigCopyLeavesNoDestination(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "config")
	if err := copyConfig(failingReader{}, dst, 0644); err == nil {
		t.Fatal("expected read error")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial configuration published")
	}
}

func TestReservedMarkerCannotFollowBottleSymlink(t *testing.T) {
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "mine")
	if err := os.WriteFile(outside, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(tree, ownerFile)); err != nil {
		t.Fatal(err)
	}
	inst := Installer{Prefix: prefix}
	p := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "demo", Version: "1"}}, Tree: tree}}}
	if _, err := inst.Apply(context.Background(), p, nil); err == nil {
		t.Fatal("accepted reserved marker")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "original" {
		t.Fatal("modified outside file")
	}
}

func TestRepairLinksDoesNotRepeatSuccessfulPostinstall(t *testing.T) {
	for _, barrierFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(barrierFailure), func(t *testing.T) { checkRepairLinks(t, barrierFailure) })
	}
}

func checkRepairLinks(t *testing.T, barrierFailure bool) {
	t.Helper()
	prefix := t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(prefix, "var", "count")
	generated := filepath.Join(prefix, "Cellar", "demo", "1", "bin", "generated")
	script := "#!/bin/sh\nprintf x >> '" + count + "'\nprintf generated > '" + generated + "'\n"
	if err := os.WriteFile(filepath.Join(tree, "bin", "counter"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(prefix, "bin", "generated")
	if !barrierFailure {
		if err := os.WriteFile(foreign, []byte("mine"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	f := model.Formula{Name: "demo", Version: "1", Steps: []json.RawMessage{json.RawMessage(`{"type":"run","command":{"base":"bin","path":"counter"},"args":[]}`)}}
	p := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: f}, Tree: tree}}}
	inst := Installer{Prefix: prefix}
	if barrierFailure {
		calls := 0
		inst.syncDirectories = func(paths []string) error {
			calls++
			if calls == 2 {
				return errors.New("injected pending-link barrier failure")
			}
			return platform.SyncDirs(paths)
		}
	}
	if _, err := inst.Apply(context.Background(), p, nil); !errors.Is(err, model.ErrNeedsRepair) {
		t.Fatalf("expected link publication failure, got %v", err)
	}
	snap, err := s.Load()
	if err != nil || !snap.Records["demo"].PostInstallComplete {
		t.Fatalf("completion not persisted: %+v %v", snap, err)
	}
	if !barrierFailure {
		if err = os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
	} else if _, err := os.Lstat(foreign); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed pending-link batch was not rolled back", err)
	}
	if err = inst.PostInstall(context.Background(), []string{"demo"}, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(count); string(data) != "x" {
		t.Fatalf("hook repeated: %q", data)
	}
	if _, err = os.Readlink(foreign); err != nil {
		t.Fatalf("generated command not linked: %v", err)
	}
}

func TestPostInstallSyncFailureDoesNotPublishCompletion(t *testing.T) {
	prefix, tree := t.TempDir(), t.TempDir()
	s := state.Store{Prefix: prefix}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tree, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(prefix, "Cellar", "demo", "1", "pipe")
	script := "#!/bin/sh\n/usr/bin/mkfifo '" + pipe + "'\n"
	if err := os.WriteFile(filepath.Join(tree, "bin", "make-pipe"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	f := model.Formula{Name: "demo", Version: "1", Steps: []json.RawMessage{json.RawMessage(`{"type":"run","command":{"base":"bin","path":"make-pipe"},"args":[]}`)}}
	p := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: f}, Tree: tree}}}
	if _, err := (Installer{Prefix: prefix}).Apply(context.Background(), p, nil); !errors.Is(err, model.ErrNeedsRepair) {
		t.Fatalf("unsynced post-install reported success: %v", err)
	}
	info, err := os.Lstat(pipe)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("hook did not create sync failure fixture", err)
	}
	snap, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	rec := snap.Records["demo"]
	if rec.PostInstallComplete || rec.Status != model.NeedsPostInstall {
		t.Fatalf("published unsynced completion: %+v", rec)
	}
}

func TestPostInstallCancellationPreservesCauseAndRepairState(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprint(repair), func(t *testing.T) {
			prefix, tree := t.TempDir(), t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			inst := Installer{Prefix: prefix}
			p := model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: model.Formula{Name: "demo", Version: "1"}}, Tree: tree}}}
			steps := []json.RawMessage{json.RawMessage(`{"type":"run","command":{"path":"/bin/sleep"},"args":["5"]}`)}
			if repair {
				if _, err := inst.Apply(context.Background(), p, nil); err != nil {
					t.Fatal(err)
				}
				if err := s.WithLock(context.Background(), func(tx *state.Session) error {
					snap, err := tx.Load()
					if err != nil {
						return err
					}
					rec := snap.Records["demo"]
					rec.Formula.Steps = steps
					snap.Records["demo"] = rec
					return tx.Save(snap)
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				p.Packages[0].Package.Formula.Steps = steps
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events, done := make(chan model.Event, 8), make(chan error, 1)
			go func() {
				if repair {
					done <- inst.PostInstall(ctx, []string{"demo"}, events)
				} else {
					_, err := inst.Apply(ctx, p, events)
					done <- err
				}
			}()
			var err error
		wait:
			for {
				select {
				case event := <-events:
					if event.Phase == "postinstall" {
						cancel()
					}
				case err = <-done:
					break wait
				}
			}
			if !errors.Is(err, context.Canceled) || !errors.Is(err, model.ErrNeedsRepair) {
				t.Fatalf("lost cancellation or repair cause: %v", err)
			}
			snap, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if rec := snap.Records["demo"]; rec.Status != model.NeedsPostInstall || rec.PostInstallComplete {
				t.Fatalf("canceled initialization marked complete: %+v", rec)
			}
		})
	}
}
