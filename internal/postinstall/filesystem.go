package postinstall

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

func (r *runner) write(rel string, data []byte, mode os.FileMode) error {
	if err := r.root.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(rel), ".gobrew-"+rand.Text())
	file, err := r.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer r.root.Remove(tmp)
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return r.root.Rename(tmp, rel)
}

func (r *runner) symlink(s step) error {
	target, err := r.path(s.Target)
	if err != nil {
		return err
	}
	var sources []string
	if s.Source.Base == "relative" {
		source := r.expand(s.Source.Path)
		if filepath.IsAbs(source) || !within(r.env.Prefix, filepath.Join(filepath.Dir(target), source)) {
			return errors.New("relative symlink escapes prefix")
		}
		sources = []string{source}
	} else {
		source, err := r.path(s.Source)
		if err != nil {
			return err
		}
		sources = []string{source}
		if s.SourceGlob {
			sources, err = r.glob(source)
			if err != nil {
				return err
			}
		}
	}
	directory := false
	if s.SourceGlob {
		info, err := os.Stat(target)
		directory = len(sources) > 1 || (err == nil && info.IsDir())
	}
	for _, source := range sources {
		dest := target
		if directory {
			dest = filepath.Join(target, filepath.Base(source))
		}
		rel, err := r.relative(dest, false)
		if err != nil {
			return err
		}
		if err := r.root.MkdirAll(filepath.Dir(rel), 0755); err != nil {
			return err
		}
		if s.Force {
			if err := r.root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := r.root.Symlink(source, rel); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) transfer(ctx context.Context, s step) error {
	source, err := r.path(s.Source)
	if err != nil {
		return err
	}
	target, err := r.path(s.Target)
	if err != nil {
		return err
	}
	if s.SourceGlob {
		matches, err := r.glob(source)
		if err != nil {
			return err
		}
		if len(matches) != 1 {
			return fmt.Errorf("source glob must match exactly one path")
		}
		source = matches[0]
	}
	if s.Type == "move_children" || s.Type == "move_contents" || s.Type == "link_children" {
		rel, err := r.relative(source, true)
		if err != nil {
			return err
		}
		dir, err := r.root.Open(rel)
		if err != nil {
			return err
		}
		children, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return err
		}
		for _, child := range children {
			from := filepath.Join(source, child.Name())
			if from == target {
				continue
			}
			to := filepath.Join(target, r.expand(s.Prefix)+child.Name()+r.expand(s.Suffix))
			next := s
			next.Source = &pathSpec{Path: from}
			next.Target = &pathSpec{Path: to}
			if s.Type == "link_children" {
				next.Type = "symlink"
				if err := r.symlink(next); err != nil {
					return err
				}
			} else {
				next.Type = "move"
				if err := r.transfer(ctx, next); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if s.Type == "link_dir" {
		return r.linkDirectory(ctx, source, target)
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, filepath.Base(source))
	}
	// Moving a symlink moves the link itself, not the configuration or data it
	// references. Copy keeps its existing dereference semantics.
	src, err := r.relative(source, s.Type != "move")
	if err != nil {
		return err
	}
	dst, err := r.relative(target, false)
	if err != nil {
		return err
	}
	if src == dst {
		return nil
	}
	// Validate the source before touching an existing destination. In
	// particular, retrying a completed move must not delete its result when
	// the original source has already been consumed.
	info, err := r.root.Lstat(src)
	if err != nil {
		return err
	}
	if s.Type != "move" {
		if info.IsDir() && !s.Recursive {
			return errors.New("copying directory requires recursive")
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("unsupported copy source")
		}
	}
	if err := r.root.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	overwrite := s.Overwrite == nil || *s.Overwrite || s.Force
	if _, err := r.root.Lstat(dst); err == nil {
		if !overwrite {
			return os.ErrExist
		}
		if err := r.root.RemoveAll(dst); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.Type == "move" {
		return r.root.Rename(src, dst)
	}
	return r.copy(ctx, src, dst)
}

func (r *runner) copy(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := r.root.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := r.root.Readlink(src)
		if err != nil {
			return err
		}
		return r.root.Symlink(target, dst)
	}
	if info.IsDir() {
		if err := r.root.MkdirAll(dst, info.Mode().Perm()|0700); err != nil {
			return err
		}
		file, err := r.root.Open(src)
		if err != nil {
			return err
		}
		entries, err := file.ReadDir(-1)
		file.Close()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := r.copy(ctx, filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("unsupported copy source")
	}
	in, err := r.root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := r.root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

func (r *runner) linkDirectory(ctx context.Context, source, target string) error {
	src, err := r.relative(source, true)
	if err != nil {
		return err
	}
	return filepath.WalkDir(filepath.Join(r.env.Prefix, src), func(p string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel(filepath.Join(r.env.Prefix, src), p)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		dst, err := r.relative(dest, false)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return r.root.MkdirAll(dst, 0755)
		}
		return r.symlink(step{Source: &pathSpec{Path: p}, Target: &pathSpec{Path: dest}, Force: true})
	})
}

func (r *runner) permissions(s step) error {
	for _, spec := range s.Paths {
		pattern, err := r.path(&spec)
		if err != nil {
			return err
		}
		paths, err := r.glob(pattern)
		if err != nil {
			return err
		}
		for _, p := range paths {
			change := func(p string) error {
				rel, err := r.relative(p, true)
				if err != nil {
					return err
				}
				info, err := r.root.Stat(rel)
				if err != nil {
					return err
				}
				mode := info.Mode().Perm()
				switch s.Permissions {
				case "u+w":
					mode |= 0200
				case "u-w":
					mode &^= 0200
				case "a+r":
					mode |= 0444
				case "a+rx":
					mode |= 0555
				default:
					n, err := strconv.ParseUint(s.Permissions, 8, 12)
					if err != nil {
						return err
					}
					mode = os.FileMode(n)
				}
				return r.root.Chmod(rel, mode)
			}
			if s.NonRecursive {
				if err := change(p); err != nil {
					return err
				}
			} else if err := filepath.WalkDir(p, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return nil
				}
				return change(path)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
