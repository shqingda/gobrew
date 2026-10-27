package installer

import (
	"context"
	"debug/macho"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/state"
)

// CleanupOptions narrows Cleanup the way brew cleanup's arguments do.
type CleanupOptions struct {
	// Formulae limits keg removal to these racks and skips the private
	// staging roots; empty means every formula.
	Formulae []string
	// Skip lists racks never cleaned. Pinned formulae are always skipped.
	Skip   []string
	DryRun bool
}

// Cleanup removes only marked, unreferenced kegs. Shared etc/var are never
// candidates. Runtime receipt facts and actual Mach-O imports protect old kegs.
func (i Installer) Cleanup(ctx context.Context) ([]string, error) {
	removed, err := i.CleanupWith(ctx, CleanupOptions{})
	paths := make([]string, len(removed))
	for n, r := range removed {
		paths[n] = r.Path
	}
	return paths, err
}

func (i Installer) CleanupWith(ctx context.Context, opts CleanupOptions) ([]model.Removed, error) {
	removed := []model.Removed{}
	err := (state.Store{Prefix: i.Prefix}).WithLock(ctx, func(tx *state.Session) error {
		if !opts.DryRun {
			if err := i.recover(tx); err != nil {
				return err
			}
		}
		snap, err := tx.Load()
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		cellar, err := state.SecureDir(i.Prefix, "Cellar")
		if err != nil {
			return err
		}
		protect := func(path string) {
			rel, err := filepath.Rel(cellar, path)
			if err != nil || !filepath.IsLocal(rel) {
				return
			}
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) >= 2 {
				keep[filepath.Join(cellar, parts[0], parts[1])] = true
			}
		}
		for _, rec := range snap.Records {
			if _, err := i.kegPath(rec.Keg); err != nil {
				return err
			}
			if !owns(rec.Keg, rec.Owner) {
				return fmt.Errorf("%w: missing or unowned keg %s", model.ErrNeedsRepair, rec.Keg)
			}
			keep[rec.Keg] = true
			for _, dep := range rec.Dependencies {
				name := strings.TrimPrefix(dep.Name, "homebrew/core/")
				version := dep.PkgVersion
				if version == "" {
					version = dep.Version
					if dep.Revision > 0 {
						version = fmt.Sprintf("%s_%d", version, dep.Revision)
					}
				}
				if validPart(name) && validPart(version) {
					keep[filepath.Join(cellar, name, version)] = true
				}
			}
			err = filepath.WalkDir(rec.Keg, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !d.Type().IsRegular() {
					return nil
				}
				if fat, err := macho.OpenFat(path); err == nil {
					defer fat.Close()
					for _, arch := range fat.Arches {
						libs, err := arch.File.ImportedLibraries()
						if err != nil {
							return err
						}
						for _, lib := range libs {
							protect(lib)
						}
					}
					return nil
				}
				if thin, err := macho.Open(path); err == nil {
					defer thin.Close()
					libs, err := thin.ImportedLibraries()
					if err != nil {
						return err
					}
					for _, lib := range libs {
						protect(lib)
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		racks, err := os.ReadDir(cellar)
		if err != nil {
			return err
		}
		j := &state.Journal{ID: newID(), Phase: "switching", Before: snap, After: snap}
		skip := map[string]bool{}
		for _, name := range opts.Skip {
			skip[name] = true
		}
		for name, rec := range snap.Records {
			skip[name] = skip[name] || rec.Pinned
		}
		for _, rack := range racks {
			if !rack.IsDir() || skip[rack.Name()] || len(opts.Formulae) > 0 && !slices.Contains(opts.Formulae, rack.Name()) {
				continue
			}
			versions, err := os.ReadDir(filepath.Join(cellar, rack.Name()))
			if err != nil {
				return err
			}
			for _, version := range versions {
				path := filepath.Join(cellar, rack.Name(), version.Name())
				if !version.IsDir() || keep[path] {
					continue
				}
				if _, err := i.kegPath(path); err != nil {
					return err
				}
				owner, err := readOwner(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if !validOwner(owner, rack.Name()) {
					continue
				}
				trash := filepath.Join(i.Prefix, ".gobrew", "trash", j.ID, rack.Name(), version.Name())
				if _, err := i.kegPath(trash); err != nil {
					return err
				}
				j.Moves = append(j.Moves, state.Move{From: path, To: trash, Owner: owner})
			}
		}
		slices.SortFunc(j.Moves, func(a, b state.Move) int { return strings.Compare(a.From, b.From) })
		sizes := make(map[string]model.Removed, len(j.Moves))
		for _, m := range j.Moves {
			sizes[m.From] = measure(m.From)
		}
		if opts.DryRun {
			for _, m := range j.Moves {
				removed = append(removed, sizes[m.From])
			}
			return nil
		}
		if len(j.Moves) > 0 {
			if err = tx.WriteJournal(j); err != nil {
				return err
			}
			if err = i.switchFiles(j); err != nil {
				return errors.Join(err, i.recover(tx))
			}
			if err = tx.Save(snap); err != nil {
				return err
			}
			if err = tx.WriteJournal(nil); err != nil {
				return err
			}
			for _, m := range j.Moves {
				if err = i.removeOwned(m.To, m.Owner); err != nil {
					return err
				}
				removed = append(removed, sizes[m.From])
			}
		}
		if len(opts.Formulae) > 0 {
			return nil
		}
		// These roots contain only previously owned staging/backup trees. Leave any
		// unmarked content alone; a crash can leave a partially written staging tree.
		for _, kind := range []string{"staging", "backups", "trash"} {
			root, err := state.SecureDir(i.Prefix, filepath.Join(".gobrew", kind))
			if err != nil {
				return err
			}
			err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !d.IsDir() {
					return nil
				}
				data, err := readOwner(path)
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				if err != nil {
					return err
				}
				parts := strings.SplitN(data, ":", 2)
				if len(parts) != 2 || !validOwner(data, parts[1]) {
					return nil
				}
				size := measure(path)
				if err = i.removeOwned(path, data); err != nil {
					return err
				}
				removed = append(removed, size)
				return filepath.SkipDir
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return removed, err
}
// measure counts regular files and their bytes, like Homebrew's abv.
func measure(path string) model.Removed {
	r := model.Removed{Path: path}
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				r.Files, r.Bytes = r.Files+1, r.Bytes+info.Size()
			}
		}
		return nil
	})
	return r
}

func validOwner(owner, name string) bool {
	parts := strings.Split(owner, ":")
	if len(parts) != 2 || len(parts[0]) != 32 || parts[1] != name {
		return false
	}
	for _, r := range parts[0] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return validPart(name)
}
