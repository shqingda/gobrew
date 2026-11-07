package engine

import (
	"cmp"
	"context"
	"maps"
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

// combine folds one name list into the running result, keeping its order.
func combine(result, names []string, first, union bool) []string {
	switch {
	case first:
		return names
	case union:
		for _, name := range names {
			if !slices.Contains(result, name) {
				result = append(result, name)
			}
		}
		return result
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
