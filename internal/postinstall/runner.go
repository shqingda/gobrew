package postinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type runner struct {
	env    Env
	root   *os.Root
	guards map[string]bool
}

func Run(ctx context.Context, steps []json.RawMessage, env Env) error {
	if err := Validate(steps); err != nil {
		return err
	}
	if env.Output == nil {
		env.Output = io.Discard
	}
	prefix, err := filepath.EvalSymlinks(env.Prefix)
	if err != nil {
		return err
	}
	env.Prefix = prefix
	env.Keg, err = filepath.EvalSymlinks(env.Keg)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(env.Keg) || !within(prefix, env.Keg) {
		return fmt.Errorf("post-install keg is outside prefix")
	}
	root, err := os.OpenRoot(prefix)
	if err != nil {
		return err
	}
	defer root.Close()
	r := runner{env: env, root: root, guards: map[string]bool{}}
	for i, raw := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		s, _ := decode(raw)
		match, err := r.matchGuards(s.Guards)
		if err != nil {
			return fmt.Errorf("post-install step %d guard: %w", i+1, err)
		}
		if !match {
			continue
		}
		if err := r.run(ctx, s); err != nil {
			return fmt.Errorf("post-install step %d (%s): %w", i+1, s.Type, err)
		}
	}
	return nil
}

func (r *runner) run(ctx context.Context, s step) error {
	switch s.Type {
	case "warn":
		_, err := fmt.Fprintln(r.env.Output, r.expand(s.Message))
		return err
	case "run":
		return r.command(ctx, s)
	case "mkdir", "mkdir_p", "touch", "write", "inreplace":
		p, err := r.path(s.Path)
		if err != nil {
			return err
		}
		rel, err := r.relative(p, true)
		if err != nil {
			return err
		}
		switch s.Type {
		case "mkdir":
			return r.root.Mkdir(rel, 0755)
		case "mkdir_p":
			return r.root.MkdirAll(rel, 0755)
		case "touch":
			if err := r.root.MkdirAll(filepath.Dir(rel), 0755); err != nil {
				return err
			}
			file, err := r.root.OpenFile(rel, os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			now := time.Now()
			return r.root.Chtimes(rel, now, now)
		case "write":
			if s.Overwrite == nil || !*s.Overwrite {
				if _, err := r.root.Stat(rel); err == nil {
					return nil
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			return r.write(rel, []byte(r.expand(s.Content)), 0644)
		case "inreplace":
			data, err := r.root.ReadFile(rel)
			if err != nil {
				return err
			}
			before := r.expand(s.Before)
			if !strings.Contains(string(data), before) && !s.SkipAudit {
				return fmt.Errorf("inreplace pattern not found")
			}
			n := -1
			if s.FirstOnly {
				n = 1
			}
			info, err := r.root.Stat(rel)
			if err != nil {
				return err
			}
			return r.write(rel, []byte(strings.Replace(string(data), before, r.expand(s.After), n)), info.Mode().Perm())
		}
	case "remove":
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
				rel, err := r.relative(p, false)
				if err != nil {
					return err
				}
				if rel == "." {
					return errors.New("cannot remove prefix root")
				}
				if s.Recursive {
					err = r.root.RemoveAll(rel)
				} else {
					err = r.root.Remove(rel)
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
		return nil
	case "symlink":
		return r.symlink(s)
	case "copy", "move", "move_children", "move_contents", "link_dir", "link_children":
		return r.transfer(ctx, s)
	case "set_permissions":
		return r.permissions(s)
	case "change_dylib_id":
		p, err := r.path(s.Source)
		if err != nil {
			return err
		}
		rel, err := r.relative(p, true)
		if err != nil {
			return err
		}
		abs := filepath.Join(r.env.Prefix, rel)
		if err := r.execute(ctx, "/usr/bin/install_name_tool", []string{"-id", r.expand(s.ID), abs}, nil); err != nil {
			return err
		}
		return r.execute(ctx, "/usr/bin/codesign", []string{"--force", "--sign", "-", "--preserve-metadata=entitlements,requirements,flags,runtime", abs}, nil)
	case "compile_gsettings_schemas", "gio_querymodules", "gdk_pixbuf_query_loaders", "gtk_update_icon_cache", "update_mime_database", "update_desktop_database":
		tools := map[string][2]string{"compile_gsettings_schemas": {"glib", "glib-compile-schemas"}, "gio_querymodules": {"glib", "gio-querymodules"}, "gdk_pixbuf_query_loaders": {"gdk-pixbuf", "gdk-pixbuf-query-loaders"}, "gtk_update_icon_cache": {"gtk+3", "gtk3-update-icon-cache"}, "update_mime_database": {"shared-mime-info", "update-mime-database"}, "update_desktop_database": {"desktop-file-utils", "update-desktop-database"}}
		tool := tools[s.Type]
		var args []string
		if s.Type == "gdk_pixbuf_query_loaders" {
			args = []string{"--update-cache"}
		} else {
			p, err := r.path(s.Path)
			if err != nil {
				return err
			}
			args = []string{p}
		}
		if s.Type == "gtk_update_icon_cache" {
			if _, err := os.Stat(filepath.Join(r.env.Prefix, "opt", "gtk4")); err == nil {
				tool = [2]string{"gtk4", "gtk4-update-icon-cache"}
			}
			args = append([]string{"-q", "-t", "-f"}, args...)
		}
		return r.execute(ctx, filepath.Join(r.env.Prefix, "opt", tool[0], "bin", tool[1]), args, nil)
	}
	return fmt.Errorf("unsupported operation %s", s.Type)
}
