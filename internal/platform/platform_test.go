package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCloneTreeIsIndependent(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	if err := os.Mkdir(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "tool"), []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tool", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := CloneTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "tool"), []byte("changed"), 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(src, "tool"))
	if err != nil || string(b) != "original" {
		t.Fatalf("cache mutated: %q %v", b, err)
	}
	link, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil || link != "tool" {
		t.Fatalf("link = %q, %v", link, err)
	}
}

func TestLockCancellationAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release, err := AcquireLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := AcquireLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, ok, err := TryLock(path)
	if err != nil || !ok {
		t.Fatalf("lock not released: %v", err)
	}
	defer release()
}

func TestSyncTreeConfinesTraversalAndPropagatesFailures(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "data"), []byte("durable content"), 0444); err != nil {
		t.Fatal(err)
	}
	// A FIFO would be an error if followed. External link targets are not part
	// of a prepared tree's durability boundary, including dangling targets.
	if err := unix.Mkfifo(filepath.Join(outside, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := SyncTree(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirs([]string{root, filepath.Join(root, "nested"), root}); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirs([]string{filepath.Join(root, "external")}); err == nil {
		t.Fatal("batch sync followed a directory symlink")
	}
	if err := SyncDirs([]string{filepath.Join(root, "nested", "data")}); err == nil {
		t.Fatal("batch directory sync accepted a regular file")
	}
	if err := SyncTree(context.Background(), filepath.Join(root, "external")); err == nil {
		t.Fatal("accepted a symlink as the tree root")
	}
	if err := SyncTree(context.Background(), outside); err == nil {
		t.Fatal("accepted an unsupported filesystem entry")
	}
	if err := SyncSharedTree(context.Background(), outside); err != nil {
		t.Fatalf("mutable shared directory rejected a process endpoint: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SyncTree(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	file, err := os.Open(filepath.Join(root, "nested", "data"))
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := SyncFile(file); err == nil {
		t.Fatal("lost file sync failure")
	}
}

func TestWriteFileAtomicReplacesWithoutLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "receipt.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || string(data) != "new" || info.Mode().Perm() != 0600 {
		t.Fatalf("content %q mode %v: %v %v", data, info.Mode(), err, statErr)
	}
	if err := WriteFileAtomic(filepath.Join(dir, "missing", "file"), []byte("x"), 0600); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("leftover entries %v: %v", entries, err)
	}
}

func TestCreateOwnedDirCreatesOnlyAMissingOwnedDirectory(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	parent := filepath.Join(t.TempDir(), "opt")
	prefix := filepath.Join(parent, "gobrew")
	if err := CreateOwnedDir(prefix, uid, gid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(prefix)
	if err != nil || int(info.Sys().(*syscall.Stat_t).Uid) != uid {
		t.Fatalf("prefix not created for the invoking user: %v", err)
	}
	// Repeating for the same user is a no-op so concurrent helpers converge.
	if err := CreateOwnedDir(prefix, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := CreateOwnedDir(prefix, uid+1, gid); err == nil {
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
	if err := CreateOwnedDir(failed, uid, foreign); err == nil {
		t.Fatal("chown to a foreign group unexpectedly succeeded")
	}
	if _, err := os.Lstat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed handover left %s: %v", failed, err)
	}
}
