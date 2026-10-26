package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

// SyncFile flushes a file and asks the device to persist prior writes. On macOS
// fsync alone does not flush a drive's volatile write cache; F_FULLFSYNC does.
func SyncFile(file *os.File) error { return fullSync(file) }

// SyncDir persists namespace changes made directly inside a real directory.
// Call this after publishing a file or moving a directory into its parent.
func SyncDir(path string) error { return syncEntry(path, true, true) }

// SyncDirs persists a journaled batch of namespace changes. Every directory is
// fsynced before one device barrier per filesystem. Callers must include newly
// created ancestors and wait for success before committing the new state.
func SyncDirs(paths []string) (err error) {
	seen := make(map[string]bool, len(paths))
	ordered := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			ordered = append(ordered, path)
		}
	}
	// Children precede any supplied ancestors.
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	devices := map[uint64]*os.File{}
	defer func() {
		for _, file := range devices {
			err = errors.Join(err, file.Close())
		}
	}()
	for _, path := range ordered {
		fd, openErr := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if openErr != nil {
			return fmt.Errorf("open directory for sync %s: %w", path, openErr)
		}
		file := os.NewFile(uintptr(fd), path)
		var stat unix.Stat_t
		if statErr := unix.Fstat(fd, &stat); statErr != nil {
			return errors.Join(statErr, file.Close())
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errors.Join(fmt.Errorf("sync requires a real directory: %s", path), file.Close())
		}
		if syncErr := syncInode(fd); syncErr != nil {
			return errors.Join(syncErr, file.Close())
		}
		device := uint64(stat.Dev)
		if devices[device] == nil {
			devices[device] = file
		} else if closeErr := file.Close(); closeErr != nil {
			return closeErr
		}
	}
	for _, file := range devices {
		if syncErr := SyncFile(file); syncErr != nil {
			return syncErr
		}
	}
	return nil
}

// SyncTree makes an exclusively owned tree durable before publication. Regular
// files are synced first, then directories from children to parents. A final
// device flush orders all those writes before the caller's completion marker.
// Links are persisted through their parent directories and are never followed.
// This deliberately runs only for newly built or materialized trees, not cache
// hits. The caller must separately sync the parent after the publication rename.
func SyncTree(ctx context.Context, root string) error {
	return syncTree(ctx, root, true)
}

// SyncSharedTree persists ordinary data and directories in mutable prefix
// storage. Running applications may keep sockets or FIFOs there; these process
// endpoints have no durable file contents and are skipped without following
// links. Prepared artifacts must use the strict SyncTree entry point instead.
func SyncSharedTree(ctx context.Context, root string) error {
	return syncTree(ctx, root, false)
}

func syncTree(ctx context.Context, root string, strict bool) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("sync tree requires a real directory: %s", root)
	}
	var directories []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			directories = append(directories, path)
		case entry.Type().IsRegular():
			return syncEntry(path, false, false)
		case entry.Type()&os.ModeSymlink != 0:
			// Syncing the link target could touch files outside the tree.
		default:
			if strict {
				return fmt.Errorf("cannot sync unsupported entry: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := syncEntry(directories[i], true, i == 0); err != nil {
			return err
		}
	}
	return nil
}

func syncEntry(path string, directory, full bool) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open for sync %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil && (directory && !info.IsDir() || !directory && !info.Mode().IsRegular()) {
		err = fmt.Errorf("filesystem entry changed before sync: %s", path)
	}
	if err == nil {
		if full {
			err = SyncFile(file)
		} else {
			// os.File.Sync also performs F_FULLFSYNC on Darwin. Flush each
			// inode here, then issue a single device barrier at the tree root.
			err = syncInode(fd)
		}
	}
	return errors.Join(err, file.Close())
}

func syncInode(fd int) error {
	for {
		err := unix.Fsync(fd)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
