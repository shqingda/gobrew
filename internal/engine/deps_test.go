package engine

import (
	"errors"
	"reflect"
	"slices"
	"testing"

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
	return func(name string) (string, []string, error) {
		deps, ok := graph[name]
		if !ok {
			return "", nil, errors.New("unknown " + name)
		}
		return name, deps, nil
	}
}

func TestDependencyOrderIsTopologicalLikeBrew(t *testing.T) {
	got, err := dependencyOrder("wget", mapSource(wgetDeps), DepsOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"libunistring", "json-c", "gettext", "libidn2", "ca-certificates", "openssl"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	got, err = dependencyOrder("wget", mapSource(wgetDeps), DepsOptions{Direct: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := wgetDeps["wget"]; !slices.Equal(got, want) {
		t.Fatalf("direct = %v, want %v", got, want)
	}
	records := map[string]model.Record{"gettext": {}, "openssl": {}}
	got, err = dependencyOrder("wget", mapSource(wgetDeps), DepsOptions{Installed: true}, records)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gettext", "openssl"}; !slices.Equal(got, want) {
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
	if _, err := dependencyOrder("a", mapSource(loop), DepsOptions{}, nil); err == nil {
		t.Fatal("listing accepted a dependency cycle")
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
	if want := []string{"gone", "oniguruma"}; !slices.Equal(got, want) {
		t.Fatalf("runtime deps = %v, want %v", got, want)
	}
}
