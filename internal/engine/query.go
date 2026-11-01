package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
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

// DepsOptions mirrors brew deps --direct, --union and --installed.
type DepsOptions struct {
	Direct, Union, Installed bool
}

// Deps lists the dependencies of each named formula like brew deps: the
// intersection for several names unless Union is set. Installed keeps only
// installed dependencies.
func (e *Engine) Deps(ctx context.Context, names []string, opts DepsOptions) ([]string, error) {
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	var installed map[string]model.Record
	if opts.Installed {
		snap, err := e.Installed()
		if err != nil {
			return nil, err
		}
		installed = snap.Records
	}
	var result []string
	for i, name := range names {
		first, err := cat.Lookup(name)
		if err != nil {
			return nil, err
		}
		seen, visiting := map[string]bool{}, map[string]bool{}
		var visit func(string, int) error
		visit = func(n string, depth int) error {
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
			if !opts.Direct || depth == 0 {
				for _, dep := range f.Dependencies {
					if err = visit(dep, depth+1); err != nil {
						return err
					}
				}
			}
			delete(visiting, f.Name)
			seen[f.Name] = true
			return nil
		}
		if err = visit(first.Name, 0); err != nil {
			return nil, err
		}
		delete(seen, first.Name)
		deps := make([]string, 0, len(seen))
		for n := range seen {
			if _, ok := installed[n]; ok || !opts.Installed {
				deps = append(deps, n)
			}
		}
		slices.Sort(deps)
		result = combine(result, deps, i == 0, opts.Union)
	}
	return result, nil
}

// Uses lists formulae that depend on every named formula, like brew uses.
// Installed reads installed receipts instead of the catalog; Recursive also
// follows dependents of dependents.
func (e *Engine) Uses(ctx context.Context, names []string, installed, recursive bool) ([]string, error) {
	edges := map[string][]string{}
	if installed {
		snap, err := e.Installed()
		if err != nil {
			return nil, err
		}
		for n, r := range snap.Records {
			for _, dep := range r.Dependencies {
				edges[strings.TrimPrefix(dep.Name, "homebrew/core/")] = append(edges[strings.TrimPrefix(dep.Name, "homebrew/core/")], n)
			}
		}
	} else {
		cat, err := e.Catalog(ctx)
		if err != nil {
			return nil, err
		}
		for i, name := range names {
			f, err := cat.Lookup(name)
			if err != nil {
				return nil, err
			}
			names[i] = f.Name
		}
		for n, item := range cat.Formulae {
			for _, dep := range item.Dependencies {
				edges[dep] = append(edges[dep], n)
			}
		}
	}
	var result []string
	for i, name := range names {
		seen := map[string]bool{}
		queue := slices.Clone(edges[name])
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			if seen[n] {
				continue
			}
			seen[n] = true
			if recursive {
				queue = append(queue, edges[n]...)
			}
		}
		delete(seen, name)
		uses := slices.Sorted(maps.Keys(seen))
		result = combine(result, uses, i == 0, false)
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

// combine folds one sorted name list into the running result.
func combine(result, names []string, first, union bool) []string {
	switch {
	case first:
		return names
	case union:
		return slices.Compact(slices.Sorted(slices.Values(append(result, names...))))
	default:
		return slices.DeleteFunc(result, func(n string) bool { return !slices.Contains(names, n) })
	}
}

// Available reads the named formulae from the local catalog without
// networking. Formulae absent from the catalog are omitted.
func (e *Engine) Available(names []string) (map[string]model.Formula, error) {
	client, err := e.client()
	if err != nil {
		return nil, err
	}
	cat, err := client.LoadForPlan(names)
	if err != nil {
		return nil, err
	}
	return cat.Formulae, nil
}

// CatalogFetched reports when the local catalog was last refreshed.
func (e *Engine) CatalogFetched() (time.Time, error) {
	client, err := e.client()
	if err != nil {
		return time.Time{}, err
	}
	return client.Fetched()
}

// Search runs brew search over the local catalog (fetching it if missing) and
// orders results like Homebrew: by base name, unversioned formulae first, then
// versioned ones such as python@3.13 by version.
func (e *Engine) Search(ctx context.Context, query string, descriptions bool) ([]model.Formula, error) {
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	result, err := cat.Search(query, descriptions)
	slices.SortStableFunc(result, func(a, b model.Formula) int {
		baseA, versionA, _ := strings.Cut(a.Name, "@")
		baseB, versionB, _ := strings.Cut(b.Name, "@")
		if c := strings.Compare(baseA, baseB); c != 0 {
			return c
		}
		return resolver.CompareVersions(cmp.Or(versionA, "0"), cmp.Or(versionB, "0"))
	})
	return result, err
}

// FormulaNames lists catalog names starting with prefix for shell completion.
// It reads only the local catalog and never touches the network.
func (e *Engine) FormulaNames(prefix string) ([]string, error) {
	c, err := e.client()
	if err != nil {
		return nil, err
	}
	return c.Names(prefix, 500)
}
