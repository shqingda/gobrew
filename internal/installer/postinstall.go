package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/postinstall"
	"github.com/shqingda/gobrew/internal/state"
)

func (i Installer) PostInstall(ctx context.Context, names []string, events chan<- model.Event) error {
	return (state.Store{Prefix: i.Prefix}).WithLock(ctx, func(tx *state.Session) error {
		if err := i.recover(tx); err != nil {
			return err
		}
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		var required []string
		for _, name := range names {
			rec, ok := snap.Records[name]
			if !ok {
				return fmt.Errorf("%w: %s", model.ErrNotFound, name)
			}
			if !owns(rec.Keg, rec.Owner) {
				return fmt.Errorf("%w: invalid keg %s", model.ErrNeedsRepair, name)
			}
			if _, err := i.kegPath(rec.Keg); err != nil {
				return err
			}
			if len(rec.Dependencies) == 0 {
				required = append(required, rec.Formula.Dependencies...)
			}
			for _, dep := range rec.Dependencies {
				required = append(required, dep.Name)
			}
		}
		if err := i.ValidateState(model.Prepared{Plan: model.Plan{Unchanged: required}}, snap); err != nil {
			return err
		}
		j := &state.Journal{ID: newID(), Phase: "postinstall", Before: snap, After: cloneSnapshot(snap), Packages: names}
		if err = tx.WriteJournal(j); err != nil {
			return err
		}
		for _, name := range names {
			rerun := snap.Records[name].Status == model.Ready
			if err = i.finishPackage(ctx, tx, &snap, j, name, rerun, events); err != nil {
				return fmt.Errorf("%w: %s: %w", model.ErrNeedsRepair, name, err)
			}
		}
		return tx.WriteJournal(nil)
	})
}

// finishPackage persists successful external initialization before publishing
// links it may have created. Repair can then retry linking without rerunning it.
func (i Installer) finishPackage(ctx context.Context, tx *state.Session, snap *model.Snapshot, j *state.Journal, name string, rerun bool, events chan<- model.Event) error {
	rec := snap.Records[name]
	if rerun {
		rec.PostInstallComplete = false
	}
	rec.Status = model.NeedsPostInstall
	fail := func(cause error) error {
		rec.PostInstallError = cause.Error()
		snap.Records[name] = rec
		return errors.Join(cause, tx.Save(*snap))
	}
	if !rec.PostInstallComplete {
		steps := rec.Formula.Steps
		if slices.Contains(j.SkipPostInstall, name) {
			steps = nil
		}
		rec.PostInstallError = "initialization interrupted; inspect before retrying"
		snap.Records[name] = rec
		if err := tx.Save(*snap); err != nil {
			return err
		}
		if len(steps) > 0 {
			model.Emit(events, model.Event{Phase: "postinstall", Package: name})
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := i.mergeShared(rec.Keg); err != nil {
			return fail(err)
		}
		if err := postinstall.Run(ctx, steps, postinstall.Env{Prefix: i.Prefix, Keg: rec.Keg, Name: name, Version: rec.Formula.Version, Output: &outputEvents{events: events, name: name}}); err != nil {
			return fail(err)
		}
		if len(steps) > 0 {
			if err := i.syncPostInstall(ctx, rec.Keg); err != nil {
				return fail(err)
			}
		}
		if !owns(rec.Keg, rec.Owner) {
			return fail(fmt.Errorf("%w: post-install changed keg ownership", model.ErrNeedsRepair))
		}
		rec.PostInstallComplete = true
		snap.Records[name] = rec
		if err := tx.Save(*snap); err != nil {
			return err
		}
	}
	links, err := i.links(rec.Keg, rec.Keg, name, rec.Linked)
	if err != nil {
		return fail(err)
	}
	next := cloneSnapshot(*snap)
	updated := rec
	updated.Links = links
	next.Records[name] = updated
	changes, err := linkChanges(*snap, next)
	if err != nil {
		return fail(err)
	}
	for _, c := range changes {
		if err = checkLink(i.Prefix, c); err != nil {
			return fail(err)
		}
	}
	j.PendingLinks = changes
	j.LinkPackage = name
	if err = tx.WriteJournal(j); err != nil {
		return err
	}
	if err = i.changeLinks(changes, false); err != nil {
		return fail(errors.Join(err, i.recover(tx)))
	}
	updated.Status = model.Ready
	updated.PostInstallError = ""
	next.Records[name] = updated
	if err = tx.Save(next); err != nil {
		return err
	}
	*snap = next
	j.PendingLinks = nil
	j.LinkPackage = ""
	if err = tx.WriteJournal(j); err != nil {
		return err
	}
	model.Emit(events, model.Event{Phase: "installed", Package: name})
	return nil
}

func (i Installer) mergeShared(keg string) error {
	for _, dir := range []string{"etc", "var"} {
		src := filepath.Join(keg, dir)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(keg, path)
			if err != nil {
				return err
			}
			dst, err := state.SecurePath(i.Prefix, rel)
			if err != nil {
				return err
			}
			if d.IsDir() {
				_, err := state.EnsureDir(i.Prefix, rel, 0755)
				return err
			}
			if _, err := os.Lstat(dst); err == nil {
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				resolved := target
				if !filepath.IsAbs(target) {
					resolved = filepath.Join(filepath.Dir(dst), target)
				}
				r, err := filepath.Rel(i.Prefix, resolved)
				if err != nil || !filepath.IsLocal(r) {
					return fmt.Errorf("unsafe shared symlink %s", rel)
				}
				if err := os.Symlink(target, dst); err != nil {
					return err
				}
				return platform.SyncDir(filepath.Dir(dst))
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported shared file %s", rel)
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			return copyConfig(in, dst, info.Mode().Perm())
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// copyConfig publishes a fully synced regular file without replacing an
// existing configuration. A failed copy never leaves a partial destination.
func copyConfig(src io.Reader, dst string, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".gobrew-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	_, copyErr := io.Copy(tmp, src)
	err = errors.Join(copyErr, platform.SyncFile(tmp), tmp.Close())
	if err != nil {
		return err
	}
	if err = os.Link(tmp.Name(), dst); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return err
	}
	return platform.SyncDir(filepath.Dir(dst))
}

// outputEvents forwards post-install warnings and command output as "output"
// events, one per line. Like all progress, delivery is best effort and a
// caller without an event channel discards it.
type outputEvents struct {
	events  chan<- model.Event
	name    string
	pending []byte
}

func (o *outputEvents) Write(b []byte) (int, error) {
	if o.events == nil {
		return len(b), nil
	}
	o.pending = append(o.pending, b...)
	for {
		line, rest, found := bytes.Cut(o.pending, []byte("\n"))
		if !found {
			break
		}
		if len(line) > 0 {
			model.Emit(o.events, model.Event{Phase: "output", Package: o.name, Message: string(line)})
		}
		o.pending = rest
	}
	if len(o.pending) > 4096 { // Bound a newline-free stream.
		model.Emit(o.events, model.Event{Phase: "output", Package: o.name, Message: string(o.pending)})
		o.pending = nil
	}
	return len(b), nil
}
