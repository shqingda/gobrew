package installer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

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
