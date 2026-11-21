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

func same(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// checkLinks confirms that every link is still in its journaled old state.
func checkLinks(prefix string, changes []state.LinkChange) (err error) {
	dirs := newLinkDirs(prefix)
	defer func() { err = errors.Join(err, dirs.close()) }()
	for _, c := range changes {
		cur, path, err := dirs.read(c.Path)
		if err != nil {
			return err
		}
		if !same(cur, c.Before) {
			return fmt.Errorf("%w: foreign or modified link %s", model.ErrConflict, path)
		}
	}
	return nil
}

// changeLinks is called only after the whole batch is in the durable journal.
// Namespace updates may be interrupted individually; commit waits for all
// changed directories and a device barrier, rather than flushing each link.
func (i Installer) changeLinks(changes []state.LinkChange, undo bool) (err error) {
	dirs := newLinkDirs(i.Prefix)
	defer func() { err = errors.Join(err, dirs.close()) }()
	parents := make(map[string]bool)
	for step := range len(changes) {
		n := step
		if undo {
			n = len(changes) - 1 - step
		}
		change := changes[n]
		created, err := dirs.set(change, undo)
		if err != nil {
			return err
		}
		parents[filepath.Dir(change.Path)] = true
		// A new parent directory's own entry joins the same barrier.
		for _, dir := range created {
			rel, err := filepath.Rel(i.Prefix, filepath.Dir(dir))
			if err != nil {
				return err
			}
			parents[rel] = true
		}
	}
	paths := make([]string, 0, len(parents))
	for rel := range parents {
		path := i.Prefix
		if rel != "." {
			var err error
			if path, err = state.SecureDir(i.Prefix, rel); err != nil {
				return err
			}
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

// linkDirs holds a link batch's parent directories. Each is opened once,
// after SecureDir has checked that it and every ancestor are real
// directories. Links are then read and replaced by name through that handle,
// so a parent swapped for a symlink later in the batch cannot redirect them.
type linkDirs struct {
	prefix  string
	open    map[string]*os.Root
	missing map[string]bool
}

func newLinkDirs(prefix string) *linkDirs {
	return &linkDirs{prefix: prefix, open: make(map[string]*os.Root), missing: make(map[string]bool)}
}

// root returns the opened directory rel, or nil when it does not exist.
func (d *linkDirs) root(rel string) (*os.Root, error) {
	if r, ok := d.open[rel]; ok {
		return r, nil
	}
	if d.missing[rel] {
		return nil, nil
	}
	path := d.prefix
	if rel != "." {
		var err error
		if path, err = state.SecureDir(d.prefix, rel); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		d.missing[rel] = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("unsafe directory %s", path)
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	// The handle must be the directory that was checked, not a replacement.
	if opened, err := r.Stat("."); err != nil || !os.SameFile(info, opened) {
		return nil, errors.Join(fmt.Errorf("directory changed while opening: %s", path), err, r.Close())
	}
	d.open[rel] = r
	return r, nil
}

func (d *linkDirs) close() error {
	var err error
	for _, r := range d.open {
		err = errors.Join(err, r.Close())
	}
	return err
}

// set changes one journaled link. It returns the parent directories it
// created; changeLinks persists them with the batch, before its barrier.
func (d *linkDirs) set(c state.LinkChange, undo bool) ([]string, error) {
	expected, target := c.Before, c.After
	if undo {
		expected, target = target, expected
	}
	cur, path, err := d.read(c.Path)
	if err != nil {
		return nil, err
	}
	dir, name := filepath.Dir(c.Path), filepath.Base(c.Path)
	r := d.open[dir]
	if same(cur, target) {
		return nil, nil
	}
	if !same(cur, expected) {
		return nil, fmt.Errorf("%w: modified link %s", model.ErrConflict, path)
	}
	if target == nil {
		return nil, r.Remove(name)
	}
	var created []string
	if r == nil {
		if _, created, err = state.MakeDirs(d.prefix, dir, 0755); err != nil {
			return nil, err
		}
		// MakeDirs may have created ancestors that other links also use.
		clear(d.missing)
		if r, err = d.root(dir); err != nil {
			return created, err
		}
		if r == nil {
			return created, fmt.Errorf("directory vanished after creation: %s", filepath.Dir(path))
		}
	}
	if cur == nil {
		// Nothing is there, so creating the link is already atomic; it
		// fails rather than replace an entry that appeared meanwhile.
		if err = r.Symlink(*target, name); errors.Is(err, os.ErrExist) {
			err = fmt.Errorf("%w: modified link %s", model.ErrConflict, path)
		}
		return created, err
	}
	tmp := name + ".gobrew-" + newID()
	if err = r.Symlink(*target, tmp); err != nil {
		return created, err
	}
	if err = r.Rename(tmp, name); err != nil {
		return created, errors.Join(err, r.Remove(tmp))
	}
	return created, nil
}

// read returns the target of the link at rel, or nil when nothing is there,
// and the link's full path. Anything but a symlink is a conflict.
func (d *linkDirs) read(rel string) (*string, string, error) {
	if !filepath.IsLocal(rel) {
		return nil, "", fmt.Errorf("unsafe path %q", rel)
	}
	path := filepath.Join(d.prefix, rel)
	r, err := d.root(filepath.Dir(rel))
	if err != nil || r == nil {
		return nil, path, err
	}
	name := filepath.Base(rel)
	info, err := r.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil, path, fmt.Errorf("%w: refusing to replace %s", model.ErrConflict, path)
	}
	s, err := r.Readlink(name)
	return &s, path, err
}
