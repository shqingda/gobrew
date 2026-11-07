package engine

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/shqingda/gobrew/internal/catalog"
	"github.com/shqingda/gobrew/internal/model"
)

// wget's real shape: gettext reaches libunistring again through a second path.
var wgetDeps = map[string][]string{
	"wget":         {"libidn2", "openssl", "gettext", "libunistring"},
	"libidn2":      {"libunistring", "gettext"},
	"gettext":      {"json-c", "libunistring"},
	"openssl":      {"ca-certificates"},
	"libunistring": nil, "json-c": nil, "ca-certificates": nil,
}

func mapSource(graph map[string][]string) depSource {
	return func(name string) (string, []model.DeclaredDependency, error) {
		deps, ok := graph[name]
		if !ok {
			return "", nil, errors.New("unknown " + name)
		}
		declared := make([]model.DeclaredDependency, len(deps))
		for i, dep := range deps {
			declared[i].Name = dep
		}
		return name, declared, nil
	}
}

func depNames(deps []model.DeclaredDependency) []string {
	names := make([]string, len(deps))
	for i, dep := range deps {
		names[i] = dep.Name
	}
	return names
}

func TestDependencyOrderIsTopologicalLikeBrew(t *testing.T) {
	got, err := dependencyOrder("wget", mapSource(wgetDeps), DepsOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"libunistring", "json-c", "gettext", "libidn2", "ca-certificates", "openssl"}
	if !slices.Equal(depNames(got), want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	got, err = dependencyOrder("wget", mapSource(wgetDeps), DepsOptions{Direct: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := wgetDeps["wget"]; !slices.Equal(depNames(got), want) {
		t.Fatalf("direct = %v, want %v", got, want)
	}
	records := map[string]model.Record{"gettext": {}, "openssl": {}}
	got, err = dependencyOrder("wget", mapSource(wgetDeps), DepsOptions{Installed: true}, records)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gettext", "openssl"}; !slices.Equal(depNames(got), want) {
		t.Fatalf("installed = %v, want %v", got, want)
	}
}

func TestDependencyTreePrunesRepeatsAndMarksCycles(t *testing.T) {
	var cycle error
	full, err := dependencyTree("libidn2", mapSource(wgetDeps), map[string]bool{}, DepsOptions{}, &cycle)
	if err != nil || cycle != nil {
		t.Fatal(err, cycle)
	}
	gettext := DepNode{Name: "gettext", Dependencies: []DepNode{
		{Name: "json-c", Dependencies: []DepNode{}},
		{Name: "libunistring", Dependencies: []DepNode{}},
	}}
	if want := []DepNode{{Name: "libunistring", Dependencies: []DepNode{}}, gettext}; !reflect.DeepEqual(full, want) {
		t.Fatalf("full tree = %+v", full)
	}

	pruned, err := dependencyTree("libidn2", mapSource(wgetDeps), map[string]bool{}, DepsOptions{Prune: true}, &cycle)
	if err != nil {
		t.Fatal(err)
	}
	if repeat := pruned[1].Dependencies[1]; repeat.Name != "libunistring" || !repeat.Pruned {
		t.Fatalf("repeat not pruned: %+v", pruned)
	}

	loop := map[string][]string{"a": {"b"}, "b": {"a"}}
	nodes, err := dependencyTree("a", mapSource(loop), map[string]bool{}, DepsOptions{}, &cycle)
	if err != nil {
		t.Fatal(err)
	}
	if cycle == nil || !nodes[0].Dependencies[0].Circular {
		t.Fatalf("cycle not reported: %+v, %v", nodes, cycle)
	}
	got, err := dependencyOrder("a", mapSource(loop), DepsOptions{}, nil)
	if err != nil || !slices.Equal(depNames(got), []string{"b"}) {
		t.Fatalf("cycle listing = %v, %v", got, err)
	}
}

func TestDepsPreferRecordedRuntimeDependenciesWhenInstalled(t *testing.T) {
	records := map[string]model.Record{
		"jq":        {Dependencies: []model.RuntimeDependency{{Name: "homebrew/core/oniguruma"}}},
		"oniguruma": {Dependencies: []model.RuntimeDependency{{Name: "gone"}}},
	}
	got, err := dependencyOrder("homebrew/core/jq", runtimeDeps(records), DepsOptions{}, records)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gone", "oniguruma"}; !slices.Equal(depNames(got), want) {
		t.Fatalf("runtime deps = %v, want %v", got, want)
	}
}

func TestEveryFormulaDepsPicksTheSourcePerFormula(t *testing.T) {
	cat := catalog.Snapshot{Formulae: map[string]model.Formula{
		"app":  {Name: "app", Dependencies: []string{"lib"}},
		"lib":  {Name: "lib", Dependencies: []string{"base"}},
		"base": {Name: "base"},
	}}
	records := map[string]model.Record{"lib": {Dependencies: []model.RuntimeDependency{{Name: "homebrew/core/linked"}}}}
	want := []FormulaDeps{{"app", []string{"base", "lib"}}, {"base", []string{}}, {"lib", []string{"linked"}}}
	got, err := everyFormulaDeps(cat, records, DepsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	want[2].Dependencies = []string{"base"}
	want[0].Dependencies = []string{"lib"}
	if got, err = everyFormulaDeps(cat, records, DepsOptions{Direct: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct: got %v, want %v", got, want)
	}
}

func TestDepsOptionsSelectLikeBrew(t *testing.T) {
	test := model.DeclaredDependency{Name: "t", Test: true}
	if (DepsOptions{IncludeTest: true}).keep(test, false) || !(DepsOptions{IncludeTest: true}).keep(test, true) {
		t.Fatal("test dependencies must only count at the root")
	}
	recommended := model.DeclaredDependency{Name: "r", Recommended: true}
	if !(DepsOptions{}).keep(recommended, true) || (DepsOptions{SkipRecommended: true}).keep(recommended, true) {
		t.Fatal("recommended dependencies are on by default and can be skipped")
	}
	unpack := model.DeclaredDependency{Name: "lzip", Build: true, Test: true, Implicit: true}
	if (DepsOptions{}).keep(unpack, true) || !(DepsOptions{IncludeImplicit: true}).keep(unpack, false) {
		t.Fatal("implicit dependencies need a flag")
	}
	got := DepsOptions{Annotate: true}.labels([]model.DeclaredDependency{unpack, recommended, {Name: "plain"}})
	if want := []string{"lzip [build] [test] [implicit]", "r [recommended]", "plain"}; !slices.Equal(got, want) {
		t.Fatalf("labels = %v", got)
	}
}

func TestMergeRepeatsKeepsTheWeakestTags(t *testing.T) {
	got := mergeRepeats([]model.DeclaredDependency{
		{Name: "a", Build: true}, {Name: "b", Build: true}, {Name: "c", Optional: true}, {Name: "d", Optional: true},
		{Name: "a"}, {Name: "b", Build: true, Test: true}, {Name: "c", Recommended: true}, {Name: "d", Optional: true, NoLinkage: true},
	})
	want := []model.DeclaredDependency{{Name: "a"}, {Name: "b", Build: true, Test: true}, {Name: "c", Recommended: true}, {Name: "d", Optional: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("merged = %+v", got)
	}
}

func TestMissingPrunesSatisfiedDependencies(t *testing.T) {
	current := model.Formula{Name: "lib", Version: "2", Dependencies: []string{"base"}}
	cat := catalog.Snapshot{Formulae: map[string]model.Formula{
		"app":   {Name: "app", Dependencies: []string{"lib", "stale"}},
		"lib":   current,
		"base":  {Name: "base"},
		"stale": {Name: "stale", Version: "2"},
	}}
	records := map[string]model.Record{"lib": {Formula: current}, "stale": {Formula: model.Formula{Name: "stale", Version: "1"}}}
	opts := DepsOptions{Missing: true}
	got, err := dependencyOrder("app", declaredDeps(cat, records, opts), opts, records)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(depNames(got), []string{"stale"}) {
		t.Fatalf("missing = %v", depNames(got))
	}
}

func TestDependencyGraphVisitsEachFormulaOnce(t *testing.T) {
	got, err := dependencyGraph([]string{"libidn2"}, mapSource(wgetDeps), DepsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var arrows []string
	for _, edge := range got {
		arrows = append(arrows, edge.From+">"+edge.Dependency.Name)
	}
	if want := []string{"libidn2>libunistring", "libidn2>gettext", "gettext>json-c", "gettext>libunistring"}; !slices.Equal(arrows, want) {
		t.Fatalf("arrows = %v", arrows)
	}
}

func TestDeclaredUsesFollowsBrewFilters(t *testing.T) {
	cat := catalog.Snapshot{Formulae: map[string]model.Formula{
		"app":  {Name: "app", DeclaredDependencies: []model.DeclaredDependency{{Name: "mid"}, {Name: "tool", Build: true}}},
		"mid":  {Name: "mid", DeclaredDependencies: []model.DeclaredDependency{{Name: "base", Recommended: true}, {Name: "check", Test: true}}},
		"base": {Name: "base"}, "tool": {Name: "tool"}, "check": {Name: "check"},
	}}
	records := map[string]model.Record{"mid": {Formula: cat.Formulae["mid"]}}
	for _, test := range []struct {
		name string
		opts DepsOptions
		want []string
	}{
		{"base", DepsOptions{Direct: true}, []string{"mid"}},
		{"base", DepsOptions{}, []string{"app", "mid"}},
		{"base", DepsOptions{SkipRecommended: true}, []string{}},
		{"base", DepsOptions{Missing: true}, []string{"app"}},
		{"tool", DepsOptions{Direct: true}, []string{}},
		{"tool", DepsOptions{Direct: true, IncludeBuild: true}, []string{"app"}},
		{"check", DepsOptions{IncludeTest: true}, []string{"mid"}},
		{"mid", DepsOptions{Installed: true, IncludeBuild: true}, []string{}},
	} {
		got, err := declaredUses(cat, records, []string{test.name}, test.opts)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, test.want) {
			t.Fatalf("uses %s %+v = %v, want %v", test.name, test.opts, got, test.want)
		}
	}
}
