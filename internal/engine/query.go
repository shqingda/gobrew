package engine

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/catalog"
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
// Direct stops at declared dependencies, so a caller sets it unless --recursive
// was given. Installed reads installed receipts, unless the options ask for
// dependencies receipts do not record; Missing keeps formulae not installed.
func (e *Engine) Uses(ctx context.Context, names []string, opts DepsOptions) ([]string, error) {
	snap, err := e.Installed()
	if err != nil && opts.Installed {
		return nil, err
	}
	if opts.Installed && !opts.scoped() {
		return runtimeUses(names, snap.Records, !opts.Direct), nil
	}
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
	return declaredUses(cat, snap.Records, names, opts)
}

// declaredUses tests each candidate like brew's select_used_dependents.
func declaredUses(cat catalog.Snapshot, records map[string]model.Record, names []string, opts DepsOptions) ([]string, error) {
	candidates := slices.Sorted(maps.Keys(cat.Formulae))
	if opts.Installed {
		candidates = slices.Sorted(maps.Keys(records))
	}
	walk := opts
	walk.Installed, walk.Missing = false, false
	source := declaredDeps(cat, records, walk)
	result := []string{}
	for _, candidate := range candidates {
		if _, installed := records[candidate]; installed && opts.Missing {
			continue
		}
		deps, err := dependencyOrder(candidate, source, walk, nil)
		if errors.Is(err, model.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(names, func(name string) bool {
			return !slices.ContainsFunc(deps, func(dep model.DeclaredDependency) bool { return dep.Name == name })
		}) {
			result = append(result, candidate)
		}
	}
	return result, nil
}

// runtimeUses reads the dependencies recorded by installed formulae.
func runtimeUses(names []string, records map[string]model.Record, recursive bool) []string {
	edges := map[string][]string{}
	for n, r := range records {
		for _, dep := range r.Dependencies {
			edges[strings.TrimPrefix(dep.Name, "homebrew/core/")] = append(edges[strings.TrimPrefix(dep.Name, "homebrew/core/")], n)
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
	return result
}

// combine folds one name list into the running result, keeping its order.
func combine[T comparable](result, names []T, first, union bool) []T {
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
		return slices.DeleteFunc(result, func(n T) bool { return !slices.Contains(names, n) })
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

// FormulaAnalytics reads the formulae.brew.sh install counts brew info shows.
func (e *Engine) FormulaAnalytics(ctx context.Context, name string) ([]model.Analytics, error) {
	client, err := e.client()
	if err != nil {
		return nil, err
	}
	return client.FormulaAnalytics(ctx, name)
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
