package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
			return nil, fmt.Errorf("%w: No such keg: %s", model.ErrNotFound, filepath.Join(i.Prefix, "Cellar", name))
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

// RestoreLinks recreates links that installed records claim but that are
// missing from the prefix, through the same ownership journal as linking.
// A changed or foreign file at a claimed path is left alone for the user.
func (i Installer) RestoreLinks(ctx context.Context) ([]string, error) {
	var restored []string
	err := (state.Store{Prefix: i.Prefix}).WithLock(ctx, func(tx *state.Session) error {
		if err := i.recover(tx); err != nil {
			return err
		}
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		var changes []state.LinkChange
		for _, rec := range snap.Records {
			if !owns(rec.Keg, rec.Owner) {
				continue
			}
			for rel, target := range rec.Links {
				path, err := state.SecurePath(i.Prefix, rel)
				if err != nil {
					return err
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					continue
				}
				changes = append(changes, state.LinkChange{Path: rel, After: &target})
			}
		}
		if len(changes) == 0 {
			return nil
		}
		slices.SortFunc(changes, func(a, b state.LinkChange) int { return strings.Compare(a.Path, b.Path) })
		// The records already claim these links, so the snapshot is unchanged;
		// undoing an interrupted restore only removes what it created.
		j := &state.Journal{ID: newID(), Phase: "switching", Before: snap, After: snap, Links: changes}
		if err = tx.WriteJournal(j); err != nil {
			return err
		}
		if err = i.switchFiles(j); err != nil {
			return errors.Join(err, i.recover(tx))
		}
		if err = tx.WriteJournal(nil); err != nil {
			return err
		}
		for _, c := range changes {
			restored = append(restored, filepath.Join(i.Prefix, c.Path))
		}
		return nil
	})
	return restored, err
}
