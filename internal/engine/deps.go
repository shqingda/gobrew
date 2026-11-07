package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/catalog"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/resolver"
)

// DepsOptions mirrors the listing options of brew deps.
type DepsOptions struct {
	Direct, Union, Installed, Topological, Prune                bool
	IncludeBuild, IncludeTest, IncludeOptional, IncludeImplicit bool
	SkipRecommended, Missing, Annotate                          bool
}

// scoped reports options that change which dependencies count.
func (o DepsOptions) scoped() bool {
	return o.SkipRecommended || o.IncludeBuild || o.IncludeTest || o.IncludeOptional || o.IncludeImplicit
}

// declaredOnly reports options that, as in brew, rule out installed receipts.
func (o DepsOptions) declaredOnly() bool {
	return o.Direct || o.Missing || o.scoped()
}

// keep ports brew's includes and ignores. Listings pass root false for indirect
// dependencies, whose test dependencies brew leaves out.
func (o DepsOptions) keep(dep model.DeclaredDependency, root bool) bool {
	if o.SkipRecommended && dep.Recommended {
		return false
	}
	required := !dep.Build && !dep.Test && !dep.Optional && !dep.Recommended
	return required || dep.Recommended || o.IncludeBuild && dep.Build || o.IncludeTest && root && dep.Test || o.IncludeOptional && dep.Optional || o.IncludeImplicit && dep.Implicit
}

func (o DepsOptions) tags(dep model.DeclaredDependency) []string {
	if !o.Annotate {
		return nil
	}
	var tags []string
	for _, tag := range []struct {
		set  bool
		name string
	}{{dep.Build, "build"}, {dep.Test, "test"}, {dep.Optional, "optional"}, {dep.Recommended, "recommended"}, {dep.Implicit, "implicit"}} {
		if tag.set {
			tags = append(tags, tag.name)
		}
	}
	return tags
}

func (o DepsOptions) labels(deps []model.DeclaredDependency) []string {
	labels := make([]string, 0, len(deps))
	for _, dep := range deps {
		label := dep.Name
		for _, tag := range o.tags(dep) {
			label += " [" + tag + "]"
		}
		if !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
	}
	return labels
}

// FormulaDeps is one "name: deps" line of brew deps --for-each.
type FormulaDeps struct {
	Name         string   `json:"name"`
	Dependencies []string `json:"dependencies"`
}

// DepNode is one line of brew deps --tree.
type DepNode struct {
	Name         string    `json:"name"`
	Tags         []string  `json:"tags,omitempty"`
	Circular     bool      `json:"circular,omitempty"`
	Pruned       bool      `json:"pruned,omitempty"`
	Dependencies []DepNode `json:"dependencies,omitempty"`
}

// DepEdge is one arrow of brew deps --graph.
type DepEdge struct {
	From       string                   `json:"from"`
	Dependency model.DeclaredDependency `json:"dependency"`
}

// depSource returns a formula's canonical name and direct dependencies.
type depSource func(name string) (string, []model.DeclaredDependency, error)

// declaredDeps reads the catalog. A formula it no longer contains keeps the
// dependencies recorded when it was installed, as brew reads an installed keg.
// Missing drops dependencies whose latest version is installed, and with them
// everything below, like brew's ignored satisfied dependencies.
func declaredDeps(cat catalog.Snapshot, records map[string]model.Record, opts DepsOptions) depSource {
	satisfied := func(name string) bool {
		r, ok := records[strings.TrimPrefix(name, "homebrew/core/")]
		if !ok {
			return false
		}
		latest, err := cat.Lookup(name)
		return err != nil || resolver.Compare(r.Formula, latest) >= 0
	}
	return func(name string) (string, []model.DeclaredDependency, error) {
		f, err := cat.Lookup(name)
		if err != nil {
			r, ok := records[strings.TrimPrefix(name, "homebrew/core/")]
			if !ok {
				return "", nil, err
			}
			f = r.Formula
		}
		deps := f.DeclaredDependencies
		if len(deps) == 0 {
			// Catalogs indexed by older builds only have runtime dependencies.
			for _, dep := range f.Dependencies {
				deps = append(deps, model.DeclaredDependency{Name: dep})
			}
		}
		if opts.Missing {
			deps = slices.DeleteFunc(slices.Clone(deps), func(dep model.DeclaredDependency) bool { return satisfied(dep.Name) })
		}
		return f.Name, deps, nil
	}
}

// runtimeDeps reads what the installed bottles actually link against. A
// dependency without a record stays listed, with nothing below it.
func runtimeDeps(records map[string]model.Record) depSource {
	return func(name string) (string, []model.DeclaredDependency, error) {
		name = strings.TrimPrefix(name, "homebrew/core/")
		deps := make([]model.DeclaredDependency, 0, len(records[name].Dependencies))
		for _, dep := range records[name].Dependencies {
			deps = append(deps, model.DeclaredDependency{Name: strings.TrimPrefix(dep.Name, "homebrew/core/")})
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
	if installed && !opts.declaredOnly() && !declared || len(names) == 0 {
		return names, runtimeDeps(snap.Records), snap.Records, nil
	}
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	return names, declaredDeps(cat, snap.Records, opts), snap.Records, nil
}

// Deps lists the dependencies of each named formula like brew deps: the
// intersection for several names unless Union is set, sorted unless
// Topological is set. Installed keeps only installed dependencies.
func (e *Engine) Deps(ctx context.Context, names []string, opts DepsOptions) ([]string, error) {
	names, source, records, err := e.loadDeps(ctx, names, opts, false)
	if err != nil {
		return nil, err
	}
	var combined []model.DeclaredDependency
	for i, name := range names {
		deps, err := dependencyOrder(name, source, opts, records)
		if err != nil {
			return nil, err
		}
		combined = combine(combined, deps, i == 0, opts.Union)
	}
	result := opts.labels(combined)
	if !opts.Topological {
		slices.Sort(result)
	}
	return result, nil
}

// DepsEach lists each formula with its sorted dependencies, like brew deps
// --for-each and brew deps --installed. Without names or Installed it covers
// the whole catalog.
func (e *Engine) DepsEach(ctx context.Context, names []string, opts DepsOptions) ([]FormulaDeps, error) {
	if len(names) == 0 && !opts.Installed {
		return e.catalogDeps(ctx, opts)
	}
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
		result = append(result, FormulaDeps{root, sortedLabels(deps, opts)})
	}
	return result, nil
}

func (e *Engine) catalogDeps(ctx context.Context, opts DepsOptions) ([]FormulaDeps, error) {
	cat, err := e.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	snap, _ := e.Installed()
	return everyFormulaDeps(cat, snap.Records, opts)
}

// everyFormulaDeps is brew deps without arguments: like brew, it picks the
// source per formula, so installed ones report what they link against.
func everyFormulaDeps(cat catalog.Snapshot, records map[string]model.Record, opts DepsOptions) ([]FormulaDeps, error) {
	declared, runtime := declaredDeps(cat, records, opts), runtimeDeps(records)
	result := make([]FormulaDeps, 0, len(cat.Formulae))
	for _, name := range slices.Sorted(maps.Keys(cat.Formulae)) {
		source := declared
		if _, installed := records[name]; installed && !opts.declaredOnly() {
			source = runtime
		}
		deps, err := dependencyOrder(name, source, opts, records)
		if err != nil {
			return nil, err
		}
		result = append(result, FormulaDeps{name, sortedLabels(deps, opts)})
	}
	return result, nil
}

func sortedLabels(deps []model.DeclaredDependency, opts DepsOptions) []string {
	slices.SortStableFunc(deps, func(a, b model.DeclaredDependency) int { return strings.Compare(a.Name, b.Name) })
	return opts.labels(deps)
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

// DepsGraph lists the arrows of brew deps --graph, each formula's in the order
// the graph first reaches it.
func (e *Engine) DepsGraph(ctx context.Context, names []string, opts DepsOptions) ([]DepEdge, error) {
	names, source, _, err := e.loadDeps(ctx, names, opts, true)
	if err != nil {
		return nil, err
	}
	roots, err := sortedRoots(names, source)
	if err != nil {
		return nil, err
	}
	return dependencyGraph(roots, source, opts)
}

func dependencyGraph(roots []string, source depSource, opts DepsOptions) ([]DepEdge, error) {
	edges := []DepEdge{}
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		current, deps, err := source(name)
		if err != nil || seen[current] {
			return err
		}
		seen[current] = true
		deps = slices.DeleteFunc(slices.Clone(deps), func(dep model.DeclaredDependency) bool { return !opts.keep(dep, true) })
		for _, dep := range deps {
			edges = append(edges, DepEdge{current, dep})
		}
		for _, dep := range deps {
			if opts.Direct {
				break
			}
			if err := visit(dep.Name); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := visit(root); err != nil {
			return nil, err
		}
	}
	return edges, nil
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

// dependencyOrder ports brew's Dependency.expand: each dependency follows its
// own dependencies, which is the order brew deps --topological prints. A
// dependency back onto the current path is skipped, and build dependencies do
// form such cycles. Direct lists the formula's own dependencies unmerged.
func dependencyOrder(name string, source depSource, opts DepsOptions, records map[string]model.Record) ([]model.DeclaredDependency, error) {
	root, direct, err := source(name)
	if err != nil {
		return nil, err
	}
	var order []model.DeclaredDependency
	if opts.Direct {
		order = slices.DeleteFunc(slices.Clone(direct), func(dep model.DeclaredDependency) bool { return !opts.keep(dep, true) })
	} else {
		var path []string
		expanded := map[string][]model.DeclaredDependency{}
		var expand func(string) ([]model.DeclaredDependency, error)
		expand = func(n string) ([]model.DeclaredDependency, error) {
			current, deps, err := source(n)
			if err != nil {
				return nil, err
			}
			path = append(path, current)
			defer func() { path = path[:len(path)-1] }()
			if result, ok := expanded[current]; ok {
				return result, nil
			}
			var all []model.DeclaredDependency
			for _, dep := range deps {
				if dep.Name == current || !opts.keep(dep, current == root) || slices.Contains(path, dep.Name) {
					continue
				}
				below, err := expand(dep.Name)
				if err != nil {
					return nil, err
				}
				if dep.Name, _, err = source(dep.Name); err != nil {
					return nil, err
				}
				all = append(append(all, below...), dep)
			}
			expanded[current] = mergeRepeats(all)
			return expanded[current], nil
		}
		if order, err = expand(root); err != nil {
			return nil, err
		}
	}
	if opts.Installed {
		order = slices.DeleteFunc(slices.Clone(order), func(dep model.DeclaredDependency) bool {
			_, ok := records[dep.Name]
			return !ok
		})
	}
	return order, nil
}

// mergeRepeats ports brew's Dependency.merge_repeats: a dependency reached more
// than once keeps its first place and the weakest of its tags.
func mergeRepeats(all []model.DeclaredDependency) []model.DeclaredDependency {
	grouped := map[string][]model.DeclaredDependency{}
	var names []string
	for _, dep := range all {
		if _, ok := grouped[dep.Name]; !ok {
			names = append(names, dep.Name)
		}
		grouped[dep.Name] = append(grouped[dep.Name], dep)
	}
	merged := make([]model.DeclaredDependency, 0, len(names))
	for _, name := range names {
		deps := grouped[name]
		result := model.DeclaredDependency{
			Name:     name,
			Build:    !slices.ContainsFunc(deps, func(d model.DeclaredDependency) bool { return !d.Build }),
			Test:     slices.ContainsFunc(deps, func(d model.DeclaredDependency) bool { return d.Test }),
			Implicit: !slices.ContainsFunc(deps, func(d model.DeclaredDependency) bool { return !d.Implicit }),
		}
		if !slices.ContainsFunc(deps, func(d model.DeclaredDependency) bool { return !d.Recommended && !d.Optional }) {
			result.Recommended = slices.ContainsFunc(deps, func(d model.DeclaredDependency) bool { return d.Recommended })
			result.Optional = !result.Recommended
		}
		merged = append(merged, result)
	}
	return merged
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
		if !opts.keep(dep, true) {
			continue
		}
		child, _, err := source(dep.Name)
		if err != nil {
			return nil, err
		}
		node := DepNode{Name: child, Tags: opts.tags(dep)}
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
