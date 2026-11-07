package installer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

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

// SetLinked links or unlinks formulae through the ownership journal.
func (i Installer) SetLinked(ctx context.Context, names []string, linked, allowKegOnly bool) ([]model.LinkResult, error) {
	var results []model.LinkResult
	err := i.MutateRecords(ctx, func(s *model.Snapshot) (err error) {
		results, err = i.changeLinked(s, names, linked, allowKegOnly)
		return err
	})
	return results, err
}

// PreviewLinked reports what SetLinked would change, including the conflicts
// that would stop it, without taking the writer lock or touching the prefix.
// Like brew link --dry-run, it lists keg-only formulae without --force.
func (i Installer) PreviewLinked(names []string, linked bool) ([]model.LinkResult, error) {
	before, err := (state.Store{Prefix: i.Prefix}).Load()
	if err != nil {
		return nil, err
	}
	if before.RecoveryRequired {
		return nil, model.ErrRecoveryRequired
	}
	after := cloneSnapshot(before)
	results, err := i.changeLinked(&after, names, linked, true)
	if err != nil {
		return nil, err
	}
	changes, err := linkChanges(before, after)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		if err = checkLink(i.Prefix, c); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (i Installer) changeLinked(s *model.Snapshot, names []string, linked, allowKegOnly bool) ([]model.LinkResult, error) {
	results := make([]model.LinkResult, 0, len(names))
	for _, name := range names {
		rec, ok := s.Records[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s", model.ErrNotFound, name)
		}
		result := model.LinkResult{Name: name, Keg: rec.Keg, Paths: []string{}, AlreadyLinked: linked && rec.Linked}
		result.KegOnly = linked && rec.Formula.KegOnly && rec.Formula.KegOnlyReason != model.KegOnlyVersioned
		result.NeedsForce = result.KegOnly && !allowKegOnly
		if result.AlreadyLinked || result.NeedsForce {
			results = append(results, result)
			continue
		}
		if !owns(rec.Keg, rec.Owner) {
			return nil, fmt.Errorf("%w: %s", model.ErrNeedsRepair, name)
		}
		if _, err := i.kegPath(rec.Keg); err != nil {
			return nil, err
		}
		links, err := i.links(rec.Keg, rec.Keg, name, linked)
		if err != nil {
			return nil, err
		}
		added, removed := links, rec.Links
		if !linked {
			added, removed = rec.Links, links
		}
		for path := range added {
			if _, kept := removed[path]; !kept {
				result.Paths = append(result.Paths, filepath.Join(i.Prefix, path))
			}
		}
		slices.Sort(result.Paths)
		rec.Linked = linked
		rec.Links = links
		s.Records[name] = rec
		results = append(results, result)
	}
	return results, nil
}
