package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

func TestInitReportsMissingPrivilegeWithoutSideEffects(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write any parent")
	}
	parent := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
	e := New(Config{Prefix: filepath.Join(parent, "prefix")})
	if err := e.Init(); !errors.Is(err, model.ErrPrivilegeRequired) {
		t.Fatalf("Init = %v, want ErrPrivilegeRequired", err)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("failed Init left %v", entries)
	}
}

func TestInitCompletesOnlyPristineLayouts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(prefix string) error
		adopt bool
	}{
		{"empty", func(string) error { return nil }, true},
		{"interrupted-layout", func(p string) error {
			for _, dir := range []string{".gobrew/staging", "bin", "Cellar"} {
				if err := os.MkdirAll(filepath.Join(p, dir), 0755); err != nil {
					return err
				}
			}
			return os.WriteFile(filepath.Join(p, ".gobrew/writer.lock"), nil, 0600)
		}, true},
		{"foreign-file", func(p string) error { return os.WriteFile(filepath.Join(p, "notes.txt"), nil, 0600) }, false},
		{"populated-bin", func(p string) error {
			if err := os.Mkdir(filepath.Join(p, "bin"), 0755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(p, "bin", "tool"), nil, 0755)
		}, false},
		{"symlinked-layout", func(p string) error { return os.Symlink(os.TempDir(), filepath.Join(p, "lib")) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "prefix")
			if err := os.Mkdir(prefix, 0755); err != nil {
				t.Fatal(err)
			}
			if err := tc.setup(prefix); err != nil {
				t.Fatal(err)
			}
			e := New(Config{Prefix: prefix})
			err := e.Init()
			if tc.adopt != (err == nil) {
				t.Fatalf("Init = %v, adopt = %v", err, tc.adopt)
			}
			ok, statErr := e.Initialized()
			if statErr != nil || ok != tc.adopt {
				t.Fatalf("Initialized = %v, %v", ok, statErr)
			}
		})
	}
}

func TestConcurrentInitAgreesOnOnePrefix(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { errs[i] = New(Config{Prefix: prefix}).Init() })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Prefix: prefix}).Installed(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivilegedHalfCreatesOnlyAMissingOwnedPrefix(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	parent := filepath.Join(t.TempDir(), "opt")
	prefix := filepath.Join(parent, "gobrew")
	if err := createOwnedPrefix(prefix, uid, gid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(prefix)
	if err != nil || int(info.Sys().(*syscall.Stat_t).Uid) != uid {
		t.Fatalf("prefix not created for the invoking user: %v", err)
	}
	// Repeating for the same user is a no-op so concurrent helpers converge.
	if err := createOwnedPrefix(prefix, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := createOwnedPrefix(prefix, uid+1, gid); err == nil {
		t.Fatal("helper accepted a prefix owned by someone else")
	}
	if os.Geteuid() == 0 {
		return
	}
	// A non-root chown to a foreign group fails like a failed handover; the
	// half-created prefix must not stay behind owned by the wrong account.
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	foreign := 1
	for slices.Contains(groups, foreign) || foreign == gid {
		foreign++
	}
	failed := filepath.Join(parent, "failed")
	if err := createOwnedPrefix(failed, uid, foreign); err == nil {
		t.Fatal("chown to a foreign group unexpectedly succeeded")
	}
	if _, err := os.Lstat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed handover left %s: %v", failed, err)
	}
}
