// Package installer owns the mutable prefix. All filesystem commits are
// journaled; post-install commands have an explicit, non-rollbackable boundary.
package installer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/postinstall"
	"github.com/shqingda/gobrew/internal/state"
)

const ownerFile = ".gobrew-owner"

type Installer struct {
	Prefix string
	// The filesystem barrier is replaceable in package-local fault tests.
	syncDirectories func([]string) error
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func cloneSnapshot(s model.Snapshot) model.Snapshot {
	r := model.Snapshot{Generation: s.Generation, Records: make(map[string]model.Record, len(s.Records))}
	for k, v := range s.Records {
		r.Records[k] = v
	}
	return r
}

func (i Installer) Apply(ctx context.Context, p model.Prepared, events chan<- model.Event) (model.Result, error) {
	result := model.Result{Unchanged: p.Plan.Unchanged}
	if p.Plan.Prefix != i.Prefix {
		return result, errors.New("prepared prefix does not match installer")
	}
	err := (state.Store{Prefix: i.Prefix}).WithLock(ctx, func(tx *state.Session) error {
		if err := i.recover(tx); err != nil {
			return err
		}
		before, err := tx.Load()
		if err != nil {
			return err
		}
		if before.Generation != p.Plan.StateGeneration {
			return model.ErrStalePlan
		}
		if err := i.ValidateState(p, before); err != nil {
			return err
		}
		if len(p.Packages) == 0 {
			if len(p.Plan.Promote) > 0 {
				for _, name := range p.Plan.Promote {
					rec, ok := before.Records[name]
					if !ok {
						return model.ErrStalePlan
					}
					rec.Requested = true
					before.Records[name] = rec
				}
				return tx.Save(before)
			}
			return nil
		}
		j, err := i.stage(ctx, p, before)
		if err != nil {
			return err
		}
		if err = tx.WriteJournal(j); err != nil {
			return err
		}
		if err = i.switchFiles(j); err != nil {
			return errors.Join(err, i.recover(tx))
		}
		// From this point, config and external commands may have persistent effects.
		j.Phase = "postinstall"
		if err = tx.WriteJournal(j); err != nil {
			return err
		}
		if err = tx.Save(j.After); err != nil {
			return err
		}
		current, err := tx.Load()
		if err != nil {
			return err
		}
		for n, name := range j.Packages {
			if err = i.finishPackage(ctx, tx, &current, j, name, false, events); err != nil {
				result.NeedsRepair = append(result.NeedsRepair, j.Packages[n:]...)
				return fmt.Errorf("%w: %s: %w", model.ErrNeedsRepair, name, err)
			}
			result.Succeeded = append(result.Succeeded, name)
		}
		return tx.WriteJournal(nil)
	})
	return result, err
}

func (i Installer) stage(ctx context.Context, p model.Prepared, before model.Snapshot) (*state.Journal, error) {
	id := newID()
	j := &state.Journal{ID: id, Phase: "switching", Before: before, After: cloneSnapshot(before)}
	for _, name := range p.Plan.Promote {
		rec, ok := j.After.Records[name]
		if !ok {
			return nil, model.ErrStalePlan
		}
		rec.Requested = true
		j.After.Records[name] = rec
	}
	stagingRel := filepath.Join(".gobrew", "staging", id)
	staging, err := state.EnsureDir(i.Prefix, stagingRel, 0755)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			if checked, err := state.SecureDir(i.Prefix, stagingRel); err == nil {
				_ = os.RemoveAll(checked)
			}
		}
	}()
	for _, prepared := range p.Packages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pkg := prepared.Package
		f := pkg.Formula
		if !validPart(f.Name) || !validPart(f.PkgVersion()) {
			return nil, errors.New("invalid package path")
		}
		keg, err := state.SecurePath(i.Prefix, filepath.Join("Cellar", f.Name, f.PkgVersion()))
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(staging, f.Name)
		if err = platform.CloneTree(ctx, prepared.Tree, dest); err != nil {
			return nil, err
		}
		owner := id + ":" + f.Name
		marker, err := os.OpenFile(filepath.Join(dest, ownerFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := io.WriteString(marker, owner)
		err = errors.Join(writeErr, marker.Sync(), marker.Close(), state.SyncDir(dest))
		if err != nil {
			return nil, err
		}
		rec := model.Record{Formula: f, Bottle: pkg.Bottle, Keg: keg, Owner: owner, Dependencies: slices.Clone(prepared.RuntimeDependencies), Requested: pkg.Requested, Status: model.NeedsPostInstall, Linked: !f.KegOnly, InstalledAt: time.Now().UTC()}
		if old, ok := before.Records[f.Name]; ok {
			rec.Requested = rec.Requested || old.Requested
			rec.Pinned = old.Pinned
			rec.Linked = old.Linked
		}
		for n := range rec.Dependencies {
			rec.Dependencies[n].Name = strings.TrimPrefix(rec.Dependencies[n].Name, "homebrew/core/")
		}
		if len(rec.Dependencies) == 0 {
			for _, dep := range f.Dependencies {
				rec.Dependencies = append(rec.Dependencies, model.RuntimeDependency{Name: dep})
			}
		}
		rec.Links, err = i.links(dest, keg, f.Name, rec.Linked)
		if err != nil {
			return nil, err
		}
		if _, err = os.Lstat(keg); err == nil {
			old, ok := before.Records[f.Name]
			if !ok || old.Keg != keg || !owns(keg, old.Owner) {
				return nil, fmt.Errorf("%w: existing unowned keg %s", model.ErrConflict, keg)
			}
			backup := filepath.Join(i.Prefix, ".gobrew", "backups", id, f.Name)
			if _, err := i.kegPath(backup); err != nil {
				return nil, err
			}
			j.Moves = append(j.Moves, state.Move{From: keg, To: backup, Owner: old.Owner})
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		j.Moves = append(j.Moves, state.Move{From: dest, To: keg, Owner: owner})
		j.After.Records[f.Name] = rec
		j.Packages = append(j.Packages, f.Name)
	}
	j.Links, err = linkChanges(before, j.After)
	if err != nil {
		return nil, err
	}
	for _, c := range j.Links {
		if err = checkLink(i.Prefix, c); err != nil {
			return nil, err
		}
	}
	success = true
	return j, nil
}

func validPart(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}
func owns(path, owner string) bool {
	parts := strings.SplitN(owner, ":", 2)
	if len(parts) != 2 || !validOwner(owner, parts[1]) {
		return false
	}
	actual, err := readOwner(path)
	return err == nil && actual == owner
}

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

func changeLink(prefix string, c state.LinkChange, undo bool) error {
	return (Installer{Prefix: prefix}).changeLinks([]state.LinkChange{c}, undo)
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

func (i Installer) moveKeg(m state.Move, undo bool) error {
	from, to := m.From, m.To
	if undo {
		from, to = to, from
	}
	if _, err := i.kegPath(from); err != nil {
		return err
	}
	if _, err := i.kegPath(to); err != nil {
		return err
	}
	if owns(to, m.Owner) && !owns(from, m.Owner) {
		return nil
	}
	if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
		if owns(to, m.Owner) {
			return nil
		}
		return fmt.Errorf("%w: missing owned keg %s", model.ErrConflict, from)
	} else if err != nil {
		return err
	}
	if !owns(from, m.Owner) {
		return fmt.Errorf("%w: keg ownership changed at %s", model.ErrConflict, from)
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%w: occupied destination %s", model.ErrConflict, to)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := i.ensureParent(to); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return errors.Join(state.SyncDir(filepath.Dir(from)), state.SyncDir(filepath.Dir(to)))
}

func (i Installer) switchFiles(j *state.Journal) error {
	for _, m := range j.Moves {
		if err := i.moveKeg(m, false); err != nil {
			return err
		}
	}
	return i.changeLinks(j.Links, false)
}

func (i Installer) recover(tx *state.Session) error {
	j, err := tx.Journal()
	if err != nil || j == nil {
		return err
	}
	if j.Phase == "postinstall" {
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		if len(j.PendingLinks) > 0 && snap.Records[j.LinkPackage].Status != model.Ready {
			if err = i.changeLinks(j.PendingLinks, true); err != nil {
				return err
			}
		}
		// The postinstall journal can be durable before the new snapshot. Restore
		// requested-root promotions without replacing later completion state.
		for name, intended := range j.After.Records {
			if existing, ok := snap.Records[name]; ok && existing.Owner == intended.Owner && intended.Requested {
				existing.Requested = true
				snap.Records[name] = existing
			}
		}
		for _, name := range j.Packages {
			intended := j.After.Records[name]
			if existing, ok := snap.Records[name]; ok && existing.Owner == intended.Owner {
				intended = existing
			}
			if intended.Status != model.Ready {
				intended.Status = model.NeedsPostInstall
				intended.PostInstallError = "operation interrupted; inspect and run gobrew postinstall explicitly"
			}
			snap.Records[name] = intended
		}
		if err = tx.Save(snap); err != nil {
			return err
		}
		return tx.WriteJournal(nil)
	}
	if j.Phase != "switching" {
		return fmt.Errorf("%w: unknown recovery phase %s", model.ErrNeedsRepair, j.Phase)
	}
	if err = i.changeLinks(j.Links, true); err != nil {
		return err
	}
	for n := len(j.Moves) - 1; n >= 0; n-- {
		if err = i.moveKeg(j.Moves[n], true); err != nil {
			return err
		}
	}
	if err = tx.Save(j.Before); err != nil {
		return err
	}
	return tx.WriteJournal(nil)
}

func (i Installer) Recover(ctx context.Context) error {
	return (state.Store{Prefix: i.Prefix}).WithLock(ctx, i.recover)
}

// MutateRecords uses the same ownership journal for uninstall, pin and linking.
func (i Installer) MutateRecords(ctx context.Context, fn func(*model.Snapshot) error) error {
	return (state.Store{Prefix: i.Prefix}).WithLock(ctx, func(tx *state.Session) error {
		if err := i.recover(tx); err != nil {
			return err
		}
		before, err := tx.Load()
		if err != nil {
			return err
		}
		after := cloneSnapshot(before)
		if err = fn(&after); err != nil {
			return err
		}
		changes, err := linkChanges(before, after)
		if err != nil {
			return err
		}
		for _, c := range changes {
			if err = checkLink(i.Prefix, c); err != nil {
				return err
			}
		}
		j := &state.Journal{ID: newID(), Phase: "switching", Before: before, After: after, Links: changes}
		for name, rec := range before.Records {
			if _, retained := after.Records[name]; retained {
				continue
			}
			if !owns(rec.Keg, rec.Owner) {
				return fmt.Errorf("%w: changed keg %s", model.ErrConflict, rec.Keg)
			}
			if _, err := i.kegPath(rec.Keg); err != nil {
				return err
			}
			trash := filepath.Join(i.Prefix, ".gobrew", "trash", j.ID, name)
			if _, err := i.kegPath(trash); err != nil {
				return err
			}
			j.Moves = append(j.Moves, state.Move{From: rec.Keg, To: trash, Owner: rec.Owner})
		}
		if err = tx.WriteJournal(j); err != nil {
			return err
		}
		if err = i.switchFiles(j); err != nil {
			return errors.Join(err, i.recover(tx))
		}
		if err = tx.Save(after); err != nil {
			return err
		}
		if err = tx.WriteJournal(nil); err != nil {
			return err
		}
		for _, m := range j.Moves {
			if err = i.removeOwned(m.To, m.Owner); err != nil {
				return err
			}
		}
		return nil
	})
}

func (i Installer) SetLinked(ctx context.Context, names []string, linked, allowKegOnly bool) error {
	return i.MutateRecords(ctx, func(s *model.Snapshot) error {
		for _, name := range names {
			rec, ok := s.Records[name]
			if !ok {
				return fmt.Errorf("%w: %s", model.ErrNotFound, name)
			}
			if linked && rec.Formula.KegOnly && !allowKegOnly {
				return fmt.Errorf("%w: %s is keg-only; use link --force to expose it", model.ErrUnsupported, name)
			}
			if !owns(rec.Keg, rec.Owner) {
				return fmt.Errorf("%w: %s", model.ErrNeedsRepair, name)
			}
			if _, err := i.kegPath(rec.Keg); err != nil {
				return err
			}
			links, err := i.links(rec.Keg, rec.Keg, name, linked)
			if err != nil {
				return err
			}
			rec.Linked = linked
			rec.Links = links
			s.Records[name] = rec
		}
		return nil
	})
}

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
		rec.PostInstallError = "initialization interrupted; inspect before retrying"
		snap.Records[name] = rec
		if err := tx.Save(*snap); err != nil {
			return err
		}
		model.Emit(events, model.Event{Phase: "postinstall", Package: name})
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := i.mergeShared(rec.Keg); err != nil {
			return fail(err)
		}
		if err := postinstall.Run(ctx, rec.Formula.Steps, postinstall.Env{Prefix: i.Prefix, Keg: rec.Keg, Name: name, Version: rec.Formula.Version, Output: io.Discard}); err != nil {
			return fail(err)
		}
		if len(rec.Formula.Steps) > 0 {
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
				return state.SyncDir(filepath.Dir(dst))
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
	err = errors.Join(copyErr, tmp.Sync(), tmp.Close())
	if err != nil {
		return err
	}
	if err = os.Link(tmp.Name(), dst); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return err
	}
	return state.SyncDir(filepath.Dir(dst))
}
