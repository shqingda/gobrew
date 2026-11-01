package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/state"
)

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
		if err := i.stagePackage(ctx, j, staging, before, prepared); err != nil {
			return nil, err
		}
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

// stagePackage clones one prepared tree into staging, marks gobrew's
// ownership and records the journal moves and record that publish it.
func (i Installer) stagePackage(ctx context.Context, j *state.Journal, staging string, before model.Snapshot, prepared model.PreparedPackage) error {
	pkg := prepared.Package
	f := pkg.Formula
	if !model.ValidPathComponent(f.Name) || !model.ValidPathComponent(f.PkgVersion()) {
		return errors.New("invalid package path")
	}
	keg, err := state.SecurePath(i.Prefix, filepath.Join("Cellar", f.Name, f.PkgVersion()))
	if err != nil {
		return err
	}
	dest := filepath.Join(staging, f.Name)
	if err = platform.CloneTree(ctx, prepared.Tree, dest); err != nil {
		return err
	}
	owner := j.ID + ":" + f.Name
	marker, err := os.OpenFile(filepath.Join(dest, ownerFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(marker, owner)
	err = errors.Join(writeErr, marker.Sync(), marker.Close(), platform.SyncDir(dest))
	if err != nil {
		return err
	}
	rec := model.Record{Formula: f, Bottle: pkg.Bottle, Keg: keg, Owner: owner, Dependencies: slices.Clone(prepared.RuntimeDependencies), Requested: pkg.Requested, Pinned: pkg.Pin, Status: model.NeedsPostInstall, Linked: !f.KegOnly, InstalledAt: time.Now().UTC()}
	if old, ok := before.Records[f.Name]; ok {
		rec.Requested = rec.Requested || old.Requested
		rec.Pinned = old.Pinned || pkg.Pin
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
		return err
	}
	if err := i.backupExistingKeg(j, before, keg, f.Name); err != nil {
		return err
	}
	j.Moves = append(j.Moves, state.Move{From: dest, To: keg, Owner: owner})
	j.After.Records[f.Name] = rec
	j.Packages = append(j.Packages, f.Name)
	return nil
}

// backupExistingKeg journals moving an existing keg aside. Only a keg that
// gobrew provably created may be replaced.
func (i Installer) backupExistingKeg(j *state.Journal, before model.Snapshot, keg, name string) error {
	if _, err := os.Lstat(keg); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	existing := ""
	if old, ok := before.Records[name]; ok && old.Keg == keg && owns(keg, old.Owner) {
		existing = old.Owner
	} else if leftover, err := readOwner(keg); err == nil && validOwner(leftover, name) && owns(keg, leftover) && !kegInUse(before, keg) {
		// An earlier version switch, such as an explicit downgrade, left
		// this keg behind. Its marker proves gobrew created it, exactly
		// as cleanup requires before reclaiming it.
		existing = leftover
	}
	if existing == "" {
		return fmt.Errorf("%w: existing unowned keg %s", model.ErrConflict, keg)
	}
	backup := filepath.Join(i.Prefix, ".gobrew", "backups", j.ID, name)
	if _, err := i.kegPath(backup); err != nil {
		return err
	}
	j.Moves = append(j.Moves, state.Move{From: keg, To: backup, Owner: existing})
	return nil
}
