package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/shqingda/gobrew/internal/resolver"
)

type Outdated struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
	Pinned    bool   `json:"pinned"`
}

func (e *Engine) Outdated(ctx context.Context) ([]Outdated, error) {
	snap, err := e.Installed()
	if err != nil {
		return nil, err
	}
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Outdated, 0)
	for name, r := range snap.Records {
		f, ok := cat.Formulae[name]
		if ok && resolver.Compare(r.Formula, f) < 0 {
			result = append(result, Outdated{name, r.Formula.PkgVersion(), f.PkgVersion(), r.Pinned})
		}
	}
	slices.SortFunc(result, func(a, b Outdated) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return result, nil
}

type Migration struct {
	Names   []string `json:"names"`
	Skipped []string `json:"skipped"`
}

// Migration reads only Homebrew receipts. It never invokes brew, imports its
// ownership, or executes formula code. Selected roots are freshly installed.
func (e *Engine) Migration(prefix string) (Migration, error) {
	result := Migration{Names: []string{}, Skipped: []string{}}
	if !filepath.IsAbs(prefix) {
		return result, fmt.Errorf("Homebrew prefix must be absolute")
	}
	cellar := filepath.Join(prefix, "Cellar")
	racks, err := os.ReadDir(cellar)
	if err != nil {
		return result, err
	}
	for _, rack := range racks {
		if !rack.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(cellar, rack.Name()))
		if err != nil {
			return result, err
		}
		requested, known := false, false
		for _, version := range versions {
			if !version.IsDir() {
				continue
			}
			path := filepath.Join(cellar, rack.Name(), version.Name(), "INSTALL_RECEIPT.json")
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			var receipt struct {
				Requested  *bool `json:"installed_on_request"`
				Dependency *bool `json:"installed_as_dependency"`
				Source     struct {
					Tap string `json:"tap"`
				} `json:"source"`
			}
			err = json.NewDecoder(file).Decode(&receipt)
			_ = file.Close()
			if err != nil {
				continue
			}
			if receipt.Source.Tap != "" && receipt.Source.Tap != "homebrew/core" {
				continue
			}
			if receipt.Requested != nil {
				known = true
				requested = requested || *receipt.Requested
			} else if receipt.Dependency != nil {
				known = true
				requested = requested || !*receipt.Dependency
			}
		}
		if requested {
			result.Names = append(result.Names, rack.Name())
		} else if !known {
			result.Skipped = append(result.Skipped, rack.Name())
		}
	}
	slices.Sort(result.Names)
	slices.Sort(result.Skipped)
	return result, nil
}

func (e *Engine) Deps(ctx context.Context, name string) ([]string, error) {
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	first, err := cat.Lookup(name)
	if err != nil {
		return nil, err
	}
	seen, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(n string) error {
		f, err := cat.Lookup(n)
		if err != nil {
			return err
		}
		if visiting[f.Name] {
			return fmt.Errorf("dependency cycle at %s", f.Name)
		}
		if seen[f.Name] {
			return nil
		}
		visiting[f.Name] = true
		for _, dep := range f.Dependencies {
			if err = visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, f.Name)
		seen[f.Name] = true
		return nil
	}
	if err = visit(first.Name); err != nil {
		return nil, err
	}
	delete(seen, first.Name)
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	return names, nil
}
func (e *Engine) Uses(ctx context.Context, name string, installed bool) ([]string, error) {
	names := make([]string, 0)
	if installed {
		snap, err := e.Installed()
		if err != nil {
			return nil, err
		}
		for n, r := range snap.Records {
			for _, dep := range r.Dependencies {
				if dep.Name == name {
					names = append(names, n)
					break
				}
			}
		}
	} else {
		cat, err := e.Catalog(ctx)
		if err != nil {
			return nil, err
		}
		f, err := cat.Lookup(name)
		if err != nil {
			return nil, err
		}
		for n, item := range cat.Formulae {
			if slices.Contains(item.Dependencies, f.Name) {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	return names, nil
}
