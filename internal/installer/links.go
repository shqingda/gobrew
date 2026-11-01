package installer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/state"
)

func (i Installer) links(tree, keg, name string, linked bool) (map[string]string, error) {
	links := map[string]string{filepath.Join("opt", name): keg}
	if !linked {
		return links, nil
	}
	for _, dir := range []string{"bin", "sbin", "lib", "include", "share", "Frameworks"} {
		root := filepath.Join(tree, dir)
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: top-level %s must be a directory", model.ErrUnsupported, dir)
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(tree, path)
			if err != nil {
				return err
			}
			links[rel] = filepath.Join(keg, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return links, nil
}

func linkChanges(before, after model.Snapshot) ([]state.LinkChange, error) {
	collect := func(s model.Snapshot) (map[string]string, error) {
		m := make(map[string]string)
		for name, r := range s.Records {
			for p, target := range r.Links {
				if prev, ok := m[p]; ok && prev != target {
					return nil, fmt.Errorf("%w: %s claims %s", model.ErrConflict, name, p)
				}
				m[p] = target
			}
		}
		return m, nil
	}
	old, err := collect(before)
	if err != nil {
		return nil, err
	}
	next, err := collect(after)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool)
	for k := range old {
		names[k] = true
	}
	for k := range next {
		names[k] = true
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	changes := make([]state.LinkChange, 0)
	for _, path := range keys {
		a, aok := old[path]
		b, bok := next[path]
		if aok && bok && a == b {
			continue
		}
		c := state.LinkChange{Path: path}
		if aok {
			c.Before = &a
		}
		if bok {
			c.After = &b
		}
		changes = append(changes, c)
	}
	return changes, nil
}

func readLink(path string) (*string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("%w: refusing to replace %s", model.ErrConflict, path)
	}
	s, err := os.Readlink(path)
	return &s, err
}
func same(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func checkLink(prefix string, c state.LinkChange) error {
	path, err := state.SecurePath(prefix, c.Path)
	if err != nil {
		return err
	}
	cur, err := readLink(path)
	if err != nil {
		return err
	}
	if !same(cur, c.Before) {
		return fmt.Errorf("%w: foreign or modified link %s", model.ErrConflict, path)
	}
	return nil
}

// changeLinks is called only after the whole batch is in the durable journal.
// Namespace updates may be interrupted individually; commit waits for all
// changed directories and a device barrier, rather than flushing each link.
func (i Installer) changeLinks(changes []state.LinkChange, undo bool) error {
	parents := make(map[string]bool)
	for step := range len(changes) {
		n := step
		if undo {
			n = len(changes) - 1 - step
		}
		change := changes[n]
		if err := setLink(i.Prefix, change, undo); err != nil {
			return err
		}
		parents[filepath.Dir(change.Path)] = true
	}
	paths := make([]string, 0, len(parents))
	for rel := range parents {
		path, err := state.SecureDir(i.Prefix, rel)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			// Recovery may encounter a not-yet-created link whose old state
			// and parent directory were both absent.
			continue
		} else if err != nil {
			return err
		}
		// Include already-correct links: a prior interrupted batch may have
		// changed them without reaching its barrier.
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	if i.syncDirectories != nil {
		return i.syncDirectories(paths)
	}
	return platform.SyncDirs(paths)
}

func setLink(prefix string, c state.LinkChange, undo bool) error {
	expected, target := c.Before, c.After
	if undo {
		expected, target = target, expected
	}
	path, err := state.SecurePath(prefix, c.Path)
	if err != nil {
		return err
	}
	cur, err := readLink(path)
	if err != nil {
		return err
	}
	if same(cur, target) {
		return nil
	}
	if !same(cur, expected) {
		return fmt.Errorf("%w: modified link %s", model.ErrConflict, path)
	}
	if target == nil {
		return os.Remove(path)
	}
	if err = (Installer{Prefix: prefix}).ensureParent(path); err != nil {
		return err
	}
	tmp := path + ".gobrew-" + newID()
	if err = os.Symlink(*target, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}
