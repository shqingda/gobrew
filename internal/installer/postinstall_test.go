package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

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

func TestPostinstallRecoveryPreservesPromotionsAndCompletedStatus(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			prefix, tree := t.TempDir(), t.TempDir()
			s := state.Store{Prefix: prefix}
			if err := s.Initialize(); err != nil {
				t.Fatal(err)
			}
			inst := Installer{Prefix: prefix}
			lib := onePackage(prefix, tree, model.Formula{Name: "lib", Version: "1"})
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
	p := onePackage(prefix, tree, f)
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
			p := onePackage(prefix, tree, model.Formula{Name: "demo", Version: "1"})
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
