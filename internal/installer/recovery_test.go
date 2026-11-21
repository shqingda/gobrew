package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/state"
)

// demoTree is a prepared tree with one executable whose content names it.
func demoTree(t *testing.T, content string) string {
	t.Helper()
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "bin", "demo"), []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return tree
}

func installedDemo(t *testing.T) (Installer, state.Store, model.Record) {
	t.Helper()
	prefix := t.TempDir()
	store := state.Store{Prefix: prefix}
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	inst := Installer{Prefix: prefix}
	if _, err := inst.Apply(context.Background(), onePackage(prefix, demoTree(t, "v1"), model.Formula{Name: "demo", Version: "1"}), nil); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return inst, store, snap.Records["demo"]
}

// assertRestored checks that recovery left exactly the original install.
func assertRestored(t *testing.T, inst Installer, store state.Store, original model.Record) {
	t.Helper()
	snap, err := store.Load()
	if err != nil || snap.RecoveryRequired {
		t.Fatalf("after recovery: %+v, %v", snap, err)
	}
	got := snap.Records["demo"]
	if len(snap.Records) != 1 || got.Keg != original.Keg || got.Owner != original.Owner || got.Formula.Version != "1" {
		t.Fatalf("record = %+v, want %+v", got, original)
	}
	if !owns(original.Keg, original.Owner) {
		t.Fatal("the original keg lost its ownership marker")
	}
	if b, err := os.ReadFile(filepath.Join(original.Keg, "bin", "demo")); err != nil || string(b) != "v1" {
		t.Fatalf("original keg content = %q, %v", b, err)
	}
	if target, err := os.Readlink(filepath.Join(inst.Prefix, "bin", "demo")); err != nil || target != filepath.Join(original.Keg, "bin", "demo") {
		t.Fatalf("bin/demo -> %q, %v", target, err)
	}
}

// TestInterruptedUpgradeAndReinstallRestoreTheOldKeg stops a version switch
// at each journal boundary, as a crash would, and recovers in a new session.
// Reinstall reuses the keg path, so it also backs the old keg up.
func TestInterruptedUpgradeAndReinstallRestoreTheOldKeg(t *testing.T) {
	for _, version := range []string{"2", "1"} {
		for _, stop := range []string{"before-move", "after-backup", "after-move", "partial-links", "after-links"} {
			t.Run("v"+version+"/"+stop, func(t *testing.T) {
				inst, store, original := installedDemo(t)
				next := onePackage(inst.Prefix, demoTree(t, "v"+version+"-new"), model.Formula{Name: "demo", Version: version})
				err := store.WithLock(context.Background(), func(tx *state.Session) error {
					snap, err := tx.Load()
					if err != nil {
						return err
					}
					j, err := inst.stage(context.Background(), next, snap, nil)
					if err != nil {
						return err
					}
					if want := map[string]int{"2": 1, "1": 2}[version]; len(j.Moves) != want {
						t.Fatalf("moves = %+v", j.Moves)
					}
					if err = tx.WriteJournal(j); err != nil {
						return err
					}
					moves := map[string]int{"before-move": 0, "after-backup": 1}[stop]
					if stop != "before-move" && stop != "after-backup" {
						moves = len(j.Moves)
					}
					for _, m := range j.Moves[:min(moves, len(j.Moves))] {
						if _, err = inst.moveKeg(m, false); err != nil {
							return err
						}
					}
					// A reinstall keeps the keg path, so its links do not change.
					if version == "1" && len(j.Links) != 0 {
						t.Fatalf("reinstall changes links: %+v", j.Links)
					}
					switch {
					case len(j.Links) == 0:
					case stop == "partial-links":
						return setLink(inst.Prefix, j.Links[0], false)
					case stop == "after-links":
						return inst.changeLinks(j.Links, false)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if snap, err := store.Load(); err != nil || !snap.RecoveryRequired {
					t.Fatalf("the interruption was not journaled: %v", err)
				}
				if err := inst.Recover(context.Background()); err != nil {
					t.Fatal(err)
				}
				assertRestored(t, inst, store, original)
			})
		}
	}
}

// TestInterruptedUninstallRestoresTheKegFromTrash stops an uninstall after
// its keg moved to the trash, before or after its links were removed.
func TestInterruptedUninstallRestoresTheKegFromTrash(t *testing.T) {
	for _, stop := range []string{"after-move", "after-links"} {
		t.Run(stop, func(t *testing.T) {
			inst, store, original := installedDemo(t)
			err := store.WithLock(context.Background(), func(tx *state.Session) error {
				before, err := tx.Load()
				if err != nil {
					return err
				}
				after := cloneSnapshot(before)
				delete(after.Records, "demo")
				links, err := linkChanges(before, after)
				if err != nil {
					return err
				}
				id := newID()
				trash := filepath.Join(inst.Prefix, ".gobrew", "trash", id, "demo")
				j := &state.Journal{ID: id, Phase: "switching", Before: before, After: after, Links: links,
					Moves: []state.Move{{From: original.Keg, To: trash, Owner: original.Owner}}}
				if err = tx.WriteJournal(j); err != nil {
					return err
				}
				if _, err = inst.moveKeg(j.Moves[0], false); err != nil {
					return err
				}
				if stop == "after-links" {
					return inst.changeLinks(j.Links, false)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(original.Keg); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the keg was not moved: %v", err)
			}
			if err := inst.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertRestored(t, inst, store, original)
		})
	}
}

// TestUnknownJournalPhaseIsNeverGuessed keeps a journal that this version
// does not understand, and the files it describes, for repair.
func TestUnknownJournalPhaseIsNeverGuessed(t *testing.T) {
	inst, store, original := installedDemo(t)
	err := store.WithLock(context.Background(), func(tx *state.Session) error {
		before, err := tx.Load()
		if err != nil {
			return err
		}
		return tx.WriteJournal(&state.Journal{ID: newID(), Phase: "from-a-newer-gobrew", Before: before, After: model.Snapshot{Records: map[string]model.Record{}}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Recover(context.Background()); !errors.Is(err, model.ErrNeedsRepair) {
		t.Fatalf("recover = %v, want needs repair", err)
	}
	snap, err := store.Load()
	if err != nil || !snap.RecoveryRequired || snap.Records["demo"].Owner != original.Owner {
		t.Fatalf("after refusing: %+v, %v", snap, err)
	}
	if !owns(original.Keg, original.Owner) {
		t.Fatal("refusing recovery changed the keg")
	}
}

// TestInterruptedPendingLinksBeforeReadyCommitRollBack stops after a
// package's pending links changed but before Ready and the cleared journal
// committed together, as a crash would.
func TestInterruptedPendingLinksBeforeReadyCommitRollBack(t *testing.T) {
	inst, store, original := installedDemo(t)
	extra := filepath.Join(original.Keg, "bin", "extra")
	if err := os.WriteFile(extra, []byte("generated"), 0755); err != nil {
		t.Fatal(err)
	}
	err := store.WithLock(context.Background(), func(tx *state.Session) error {
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		rec := snap.Records["demo"]
		rec.Status, rec.PostInstallComplete = model.NeedsPostInstall, true
		snap.Records["demo"] = rec
		if err = tx.Save(snap); err != nil {
			return err
		}
		target := extra
		pending := []state.LinkChange{{Path: filepath.Join("bin", "extra"), After: &target}}
		j := &state.Journal{ID: newID(), Phase: "postinstall", Before: snap, After: cloneSnapshot(snap), Packages: []string{"demo"}, PendingLinks: pending, LinkPackage: "demo"}
		if err = tx.WriteJournal(j); err != nil {
			return err
		}
		return inst.changeLinks(pending, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(inst.Prefix, "bin", "extra")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending link survived recovery", err)
	}
	snap, err := store.Load()
	if err != nil || snap.RecoveryRequired {
		t.Fatalf("after recovery: %+v, %v", snap, err)
	}
	if got := snap.Records["demo"]; got.Status != model.NeedsPostInstall || !got.PostInstallComplete || got.Owner != original.Owner {
		t.Fatalf("record = %+v", got)
	}
}

// TestKegMoveBatchBarrierAndFailureRollback checks that a reinstall's two
// keg moves (backup, then publish) share one barrier after both renames, and
// that a failed barrier restores the old keg.
func TestKegMoveBatchBarrierAndFailureRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			inst, store, original := installedDemo(t)
			failure := errors.New("injected keg move barrier failure")
			calls := 0
			inst.syncMoves = func(paths []string) error {
				calls++
				if calls == 1 {
					if owner := readOwnerOrEmpty(original.Keg); owner == "" || owner == original.Owner {
						t.Fatal("barrier ran before every keg moved")
					}
					for _, dir := range []string{filepath.Join(inst.Prefix, "Cellar", "demo"), filepath.Join(inst.Prefix, ".gobrew", "backups")} {
						if !slices.ContainsFunc(paths, func(p string) bool { return p == dir || filepath.Dir(p) == dir }) {
							t.Fatalf("batch omits %s: %v", dir, paths)
						}
					}
					if fail {
						return failure
					}
				}
				return platform.SyncDirs(paths)
			}
			next := onePackage(inst.Prefix, demoTree(t, "v1-new"), model.Formula{Name: "demo", Version: "1"})
			current, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			next.Plan.StateGeneration = current.Generation
			_, err = inst.Apply(context.Background(), next, nil)
			if fail != errors.Is(err, failure) || !fail && err != nil {
				t.Fatalf("apply error: %v", err)
			}
			if fail {
				if calls != 2 {
					t.Fatalf("recovery did not flush its undo batch: calls=%d", calls)
				}
				assertRestored(t, inst, store, original)
				return
			}
			snap, err := store.Load()
			if got := snap.Records["demo"]; err != nil || calls != 1 || got.Owner == original.Owner || got.Status != model.Ready {
				t.Fatalf("calls=%d snap=%+v err=%v", calls, snap, err)
			}
		})
	}
}

func readOwnerOrEmpty(keg string) string {
	owner, _ := readOwner(keg)
	return owner
}
