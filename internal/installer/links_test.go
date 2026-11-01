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
	if err := (Installer{Prefix: prefix}).changeLinks([]state.LinkChange{change}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(prefix, "bin/tool")); got != after {
		t.Fatal(got)
	}
	if err := (Installer{Prefix: prefix}).changeLinks([]state.LinkChange{change}, true); err != nil {
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
	if err := (Installer{Prefix: prefix}).changeLinks([]state.LinkChange{change}, false); err == nil {
		t.Fatal("overwrote foreign file")
	}
	b, _ := os.ReadFile(filepath.Join(prefix, "bin/tool"))
	if string(b) != "mine" {
		t.Fatal("lost user data")
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
			p := onePackage(prefix, tree, model.Formula{Name: "demo", Version: "1"})
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
	p := onePackage(prefix, tree, f)
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
