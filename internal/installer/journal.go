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
	return errors.Join(platform.SyncDir(filepath.Dir(from)), platform.SyncDir(filepath.Dir(to)))
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
