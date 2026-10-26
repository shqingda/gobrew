package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
