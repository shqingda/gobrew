package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/catalog"
	"github.com/shqingda/gobrew/internal/model"
)

// DepsOptions mirrors brew deps --direct, --union, --installed, --topological
// and --prune.
type DepsOptions struct {
	Direct, Union, Installed, Topological, Prune bool
}

// FormulaDeps is one "name: deps" line of brew deps --for-each.
type FormulaDeps struct {
	Name         string   `json:"name"`
	Dependencies []string `json:"dependencies"`
}

// DepNode is one line of brew deps --tree.
type DepNode struct {
	Name         string    `json:"name"`
	Circular     bool      `json:"circular,omitempty"`
	Pruned       bool      `json:"pruned,omitempty"`
	Dependencies []DepNode `json:"dependencies,omitempty"`
}

// depSource returns a formula's canonical name and direct dependencies.
type depSource func(name string) (string, []string, error)

// declaredDeps reads the catalog. A formula it no longer contains keeps the
// dependencies recorded when it was installed, as brew reads an installed keg.
func declaredDeps(cat catalog.Snapshot, records map[string]model.Record) depSource {
	return func(name string) (string, []string, error) {
		f, err := cat.Lookup(name)
		if err != nil {
			r, ok := records[strings.TrimPrefix(name, "homebrew/core/")]
			if !ok {
				return "", nil, err
			}
			f = r.Formula
		}
		return f.Name, f.Dependencies, nil
	}
}

// runtimeDeps reads what the installed bottles actually link against. A
// dependency without a record stays listed, with nothing below it.
func runtimeDeps(records map[string]model.Record) depSource {
	return func(name string) (string, []string, error) {
		name = strings.TrimPrefix(name, "homebrew/core/")
		deps := make([]string, 0, len(records[name].Dependencies))
		for _, dep := range records[name].Dependencies {
			deps = append(deps, strings.TrimPrefix(dep.Name, "homebrew/core/"))
		}
		return name, deps, nil
	}
}

// loadDeps picks brew's source for a listing: actual runtime dependencies when
// every named formula is installed, declared ones otherwise. No names with
// Installed means every installed formula.
func (e *Engine) loadDeps(ctx context.Context, names []string, opts DepsOptions, declared bool) ([]string, depSource, map[string]model.Record, error) {
	snap, err := e.Installed()
	if err != nil && opts.Installed {
		return nil, nil, nil, err
	}
	if len(names) == 0 && opts.Installed {
		names = slices.Sorted(maps.Keys(snap.Records))
	}
	installed := !slices.ContainsFunc(names, func(name string) bool {
		_, ok := snap.Records[strings.TrimPrefix(name, "homebrew/core/")]
		return !ok
	})
	// Nothing named means nothing installed here, so no catalog is needed.
	if installed && !opts.Direct && !declared || len(names) == 0 {
		return names, runtimeDeps(snap.Records), snap.Records, nil
	}
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	return names, declaredDeps(cat, snap.Records), snap.Records, nil
}

// Deps lists the dependencies of each named formula like brew deps: the
// intersection for several names unless Union is set, sorted unless
// Topological is set. Installed keeps only installed dependencies.
func (e *Engine) Deps(ctx context.Context, names []string, opts DepsOptions) ([]string, error) {
	names, source, records, err := e.loadDeps(ctx, names, opts, false)
	if err != nil {
		return nil, err
	}
	var result []string
	for i, name := range names {
		deps, err := dependencyOrder(name, source, opts, records)
		if err != nil {
			return nil, err
		}
		result = combine(result, deps, i == 0, opts.Union)
	}
	if !opts.Topological {
		slices.Sort(result)
	}
	return result, nil
}

// DepsEach lists each formula with its sorted dependencies, like brew deps
// --for-each and brew deps --installed without names.
func (e *Engine) DepsEach(ctx context.Context, names []string, opts DepsOptions) ([]FormulaDeps, error) {
	names, source, records, err := e.loadDeps(ctx, names, opts, false)
	if err != nil {
		return nil, err
	}
	roots, err := sortedRoots(names, source)
	if err != nil {
		return nil, err
	}
	result := make([]FormulaDeps, 0, len(roots))
	for _, root := range roots {
		deps, err := dependencyOrder(root, source, opts, records)
		if err != nil {
			return nil, err
		}
		slices.Sort(deps)
		result = append(result, FormulaDeps{root, deps})
	}
	return result, nil
}

// DepsTree builds one tree per formula in name order from declared
// dependencies, which brew also uses for --tree. A cycle is marked in the
// returned trees and reported as an error, so callers can show both.
func (e *Engine) DepsTree(ctx context.Context, names []string, opts DepsOptions) ([]DepNode, error) {
	names, source, _, err := e.loadDeps(ctx, names, opts, true)
	if err != nil {
		return nil, err
	}
	roots, err := sortedRoots(names, source)
	if err != nil {
		return nil, err
	}
	result := make([]DepNode, 0, len(roots))
	var cycle error
	for _, root := range roots {
		deps, err := dependencyTree(root, source, map[string]bool{}, opts, &cycle)
		if err != nil {
			return nil, err
		}
		result = append(result, DepNode{Name: root, Dependencies: deps})
	}
	return result, cycle
}

func sortedRoots(names []string, source depSource) ([]string, error) {
	roots := make([]string, 0, len(names))
	for _, name := range names {
		root, _, err := source(name)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	slices.Sort(roots)
	return slices.Compact(roots), nil
}

// dependencyOrder walks depth first and lists each dependency after its own
// dependencies, which is the order brew deps --topological prints.
func dependencyOrder(name string, source depSource, opts DepsOptions, records map[string]model.Record) ([]string, error) {
	seen, visiting := map[string]bool{}, map[string]bool{}
	order := []string{}
	var visit func(string, int) error
	visit = func(n string, depth int) error {
		current, deps, err := source(n)
		if err != nil {
			return err
		}
		if visiting[current] {
			return fmt.Errorf("dependency cycle at %s", current)
		}
		if seen[current] {
			return nil
		}
		visiting[current] = true
		if !opts.Direct || depth == 0 {
			for _, dep := range deps {
				if err = visit(dep, depth+1); err != nil {
					return err
				}
			}
		}
		delete(visiting, current)
		seen[current] = true
		if depth > 0 {
			order = append(order, current)
		}
		return nil
	}
	if err := visit(name, 0); err != nil {
		return nil, err
	}
	if opts.Installed {
		order = slices.DeleteFunc(order, func(n string) bool {
			_, ok := records[n]
			return !ok
		})
	}
	return order, nil
}

// dependencyTree ports brew's recursive_deps_tree. seen is true for formulae on
// the current path, which are cycles, and false for those already finished,
// which Prune cuts short.
func dependencyTree(name string, source depSource, seen map[string]bool, opts DepsOptions, cycle *error) ([]DepNode, error) {
	current, deps, err := source(name)
	if err != nil {
		return nil, err
	}
	seen[current] = true
	nodes := make([]DepNode, 0, len(deps))
	for _, dep := range deps {
		child, _, err := source(dep)
		if err != nil {
			return nil, err
		}
		node := DepNode{Name: child}
		onPath, visited := seen[child]
		switch {
		case onPath:
			node.Circular = true
			*cycle = fmt.Errorf("dependency cycle at %s", child)
		case opts.Prune && visited:
			node.Pruned = true
		case !opts.Direct:
			if node.Dependencies, err = dependencyTree(child, source, seen, opts, cycle); err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, node)
	}
	seen[current] = false
	return nodes, nil
}
