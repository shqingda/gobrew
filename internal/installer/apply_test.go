package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
	"github.com/shqingda/gobrew/internal/testutil"
)

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
			prepared := onePackage(prefix, tree, model.Formula{Name: "demo", Version: "1"})
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
	lib, owner := testutil.OwnedKeg(t, prefix, "lib", "1")
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
	p := onePackage(prefix, tree, model.Formula{Name: "demo", Version: "1"})
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
	p := onePackage(prefix, tree, model.Formula{Name: "demo", Version: "1"})
	if _, err := inst.Apply(context.Background(), p, nil); err == nil {
		t.Fatal("accepted reserved marker")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "original" {
		t.Fatal("modified outside file")
	}
}

// An explicit downgrade leaves the newer keg behind. Installing that version
// again may reuse the path only when gobrew's marker proves ownership.
func TestLeftoverOwnedKegIsReplacedButForeignKegIsNot(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		prefix := t.TempDir()
		if err := (state.Store{Prefix: prefix}).Initialize(); err != nil {
			t.Fatal(err)
		}
		keg := filepath.Join(prefix, "Cellar", "demo", "2")
		if err := os.MkdirAll(keg, 0755); err != nil {
			t.Fatal(err)
		}
		marker := strings.Repeat("b", 32) + ":demo"
		if foreign {
			marker = "someone else"
		}
		if err := os.WriteFile(filepath.Join(keg, ownerFile), []byte(marker), 0600); err != nil {
			t.Fatal(err)
		}
		tree := t.TempDir()
		if err := os.WriteFile(filepath.Join(tree, "new"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		p := onePackage(prefix, tree, model.Formula{Name: "demo", Version: "2"})
		_, err := (Installer{Prefix: prefix}).Apply(context.Background(), p, nil)
		if foreign {
			if !errors.Is(err, model.ErrConflict) {
				t.Fatalf("foreign keg replaced: %v", err)
			}
			if data, _ := os.ReadFile(filepath.Join(keg, ownerFile)); string(data) != "someone else" {
				t.Fatal("foreign keg modified")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(keg, "new")); err != nil {
			t.Fatalf("leftover keg not replaced: %v", err)
		}
	}
}
