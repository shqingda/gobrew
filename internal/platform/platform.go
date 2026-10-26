// Package platform contains the small operating-system boundary of the engine.
package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"golang.org/x/sys/unix"
)

func Detect() (model.Platform, error) {
	p := model.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if p.OS != "darwin" || p.Arch != "arm64" {
		return p, fmt.Errorf("%w: macOS ARM64 is required", model.ErrUnsupported)
	}
	version, err := systemVersion()
	if err != nil {
		return p, err
	}
	p.Major, err = strconv.Atoi(strings.Split(version, ".")[0])
	if err != nil {
		return p, fmt.Errorf("parse macOS version %q: %w", version, err)
	}
	if p.Major < 15 {
		return p, fmt.Errorf("%w: macOS 15 or newer is required", model.ErrUnsupported)
	}
	return p, nil
}

// AcquireLock waits cooperatively for an advisory process lock. Lock files are
// permanent: removing one would let another process lock a different inode.
func AcquireLock(ctx context.Context, path string) (func() error, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, acquired, err := TryLock(path)
		if err != nil {
			return nil, err
		}
		if acquired {
			return release, nil
		}
		timer := time.NewTimer(40 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// TryLock is used by cache eviction to skip entries held by active operations.
func TryLock(path string) (func() error, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, false, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, false, err
	}
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() { closeErr = errors.Join(unix.Flock(fd, unix.LOCK_UN), unix.Close(fd)) })
		return closeErr
	}, true, nil
}

// CloneTree materializes a new, independently writable tree. It never uses
// hardlinks, which would allow post-install work to mutate the shared cache.
// Its contents and containing directory are durable on successful return.
func CloneTree(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("destination already exists: %s", dst)
		}
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := cloneTree(src, dst); err == nil {
		if err := SyncTree(ctx, dst); err != nil {
			return err
		}
		return SyncDir(filepath.Dir(dst))
	}
	// A failed clone can leave a partial destination on some filesystems.
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	err = filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.Mkdir(target, info.Mode().Perm()|0700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, in)
			return errors.Join(copyErr, out.Close())
		default:
			return fmt.Errorf("unsupported filesystem entry: %s", path)
		}
	})
	if err != nil {
		return err
	}
	if err := SyncTree(ctx, dst); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(dst))
}
