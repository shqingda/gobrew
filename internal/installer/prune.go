package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/state"
)

// PrunePrefix ports brew's prefix pruning within gobrew's ownership rules. A
// broken link goes only if it points into this Cellar and no record claims it:
// claimed links are doctor findings and other links are foreign. Empty
// subdirectories of the link directories go as well; etc and var never do.
// Each removal is idempotent and changes no state, so an interrupted run needs
// no journal and the next run finishes the work.
func (i Installer) PrunePrefix(ctx context.Context, dryRun bool) ([]model.Removed, error) {
	removed := []model.Removed{}
	err := (state.Store{Prefix: i.Prefix}).WithLock(ctx, func(tx *state.Session) error {
		if !dryRun {
			if err := i.recover(tx); err != nil {
				return err
			}
		}
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		if snap.RecoveryRequired {
			return model.ErrRecoveryRequired
		}
		claimed := map[string]bool{}
		for _, rec := range snap.Records {
			for path := range rec.Links {
				claimed[filepath.Join(i.Prefix, path)] = true
			}
		}
		cellar, err := state.SecureDir(i.Prefix, "Cellar")
		if err != nil {
			return err
		}
		var parents []string
		remove := func(path, kind string) error {
			if !dryRun {
				err := os.Remove(path)
				// Something appeared since the scan; it is simply not empty.
				if kind == model.EmptyDirectory && errors.Is(err, syscall.ENOTEMPTY) {
					return nil
				}
				if err != nil {
					return err
				}
				parents = append(parents, filepath.Dir(path))
			}
			removed = append(removed, model.Removed{Path: path, Kind: kind})
			return nil
		}
		for _, dir := range []string{"bin", "sbin", "lib", "include", "share", "Frameworks", "opt"} {
			if err = i.pruneDir(ctx, dir, cellar, claimed, remove); err != nil {
				return err
			}
		}
		// A parent may itself have been pruned afterwards.
		parents = slices.DeleteFunc(parents, func(path string) bool {
			_, err := os.Lstat(path)
			return errors.Is(err, os.ErrNotExist)
		})
		return platform.SyncDirs(parents)
	})
	return removed, err
}

func (i Installer) pruneDir(ctx context.Context, dir, cellar string, claimed map[string]bool, remove func(path, kind string) error) error {
	root, err := state.SecureDir(i.Prefix, dir)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	gone := map[string]bool{}
	var dirs []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root {
				dirs = append(dirs, path)
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink == 0 || claimed[path] {
			return nil
		}
		stray, err := brokenCellarLink(path, cellar)
		if err != nil || !stray {
			return err
		}
		gone[path] = true
		return remove(path, model.BrokenLink)
	})
	if err != nil {
		return err
	}
	// Walk order puts parents first, so this empties directories bottom up.
	for _, path := range slices.Backward(dirs) {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(entries, func(entry fs.DirEntry) bool { return !gone[filepath.Join(path, entry.Name())] }) {
			continue
		}
		gone[path] = true
		if err = remove(path, model.EmptyDirectory); err != nil {
			return err
		}
	}
	return nil
}

func brokenCellarLink(path, cellar string) (bool, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	if rel, err := filepath.Rel(cellar, target); err != nil || !filepath.IsLocal(rel) {
		return false, nil
	}
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("check link %s: %w", path, err)
	}
	return false, nil
}
