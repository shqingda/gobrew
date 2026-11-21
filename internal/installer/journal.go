package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/state"
)

// moveKeg renames one journaled keg move and returns the directories whose
// entries it may have changed. It persists nothing: moveKegs flushes the
// whole batch before any caller commits state.
func (i Installer) moveKeg(m state.Move, undo bool) ([]string, error) {
	from, to := m.From, m.To
	if undo {
		from, to = to, from
	}
	if _, err := i.kegPath(from); err != nil {
		return nil, err
	}
	if _, err := i.kegPath(to); err != nil {
		return nil, err
	}
	// Include moves that are already done: a prior interrupted batch may have
	// renamed them without reaching its barrier.
	dirs := []string{filepath.Dir(from), filepath.Dir(to)}
	if owns(to, m.Owner) && !owns(from, m.Owner) {
		return dirs, nil
	}
	if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
		if owns(to, m.Owner) {
			return dirs, nil
		}
		return nil, fmt.Errorf("%w: missing owned keg %s", model.ErrConflict, from)
	} else if err != nil {
		return nil, err
	}
	if !owns(from, m.Owner) {
		return nil, fmt.Errorf("%w: keg ownership changed at %s", model.ErrConflict, from)
	}
	if _, err := os.Lstat(to); err == nil {
		return nil, fmt.Errorf("%w: occupied destination %s", model.ErrConflict, to)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	rel, err := filepath.Rel(i.Prefix, filepath.Dir(to))
	if err != nil {
		return nil, err
	}
	_, created, err := state.MakeDirs(i.Prefix, rel, 0755)
	if err != nil {
		return nil, err
	}
	for _, dir := range created {
		dirs = append(dirs, filepath.Dir(dir))
	}
	return dirs, os.Rename(from, to)
}

// moveKegs applies a journaled batch of keg moves, in reverse when undoing,
// then persists every changed directory before one device barrier, as
// changeLinks does for links. The journal already records each move, so an
// interruption anywhere in the batch recovers as before.
func (i Installer) moveKegs(moves []state.Move, undo bool) error {
	var dirs []string
	for step := range len(moves) {
		n := step
		if undo {
			n = len(moves) - 1 - step
		}
		changed, err := i.moveKeg(moves[n], undo)
		if err != nil {
			return err
		}
		dirs = append(dirs, changed...)
	}
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		paths = append(paths, dir)
	}
	if len(paths) == 0 {
		return nil
	}
	if i.syncMoves != nil {
		return i.syncMoves(paths)
	}
	return platform.SyncDirs(paths)
}

func (i Installer) switchFiles(j *state.Journal) error {
	if err := i.moveKegs(j.Moves, false); err != nil {
		return err
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
	if err = i.moveKegs(j.Moves, true); err != nil {
		return err
	}
	if err = tx.Save(j.Before); err != nil {
		return err
	}
	return tx.WriteJournal(nil)
}

func (i Installer) Recover(ctx context.Context) error {
	return (state.Store{Prefix: i.Prefix}).WithLock(ctx, i.recover)
}
