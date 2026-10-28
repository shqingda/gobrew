package catalog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/resolver"
)

func planFormula(name, version string, deps ...string) model.Formula {
	digest := strings.Repeat("a", 64)
	return model.Formula{Name: name, Version: version, Dependencies: deps, Bottles: map[string]model.Bottle{"arm64_sequoia": {Tag: "arm64_sequoia", SHA256: digest, Cellar: ":any", URL: "https://ghcr.io/v2/homebrew/core/" + name + "/blobs/sha256:" + digest}}}
}

func TestLoadForPlanPreservesReverseDependencyPlanning(t *testing.T) {
	lib, app, leaf := planFormula("lib", "2"), planFormula("app", "2", "lib", "leaf"), planFormula("leaf", "1")
	snapshot := Snapshot{Generation: strings.Repeat("a", 64), Formulae: map[string]model.Formula{"lib": lib, "app": app, "leaf": leaf, "unrelated": planFormula("unrelated", "1")}}
	client := Client{Dir: t.TempDir(), Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 15}}
	if err := client.publish(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	full, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Requested roots plus all installed names must be supplied: app is an
	// installed reverse dependent, and its newer recipe introduces leaf.
	subset, err := client.LoadForPlan([]string{"homebrew/core/lib", "app", "lib"})
	if err != nil {
		t.Fatal(err)
	}
	if len(subset.Formulae) != 3 || subset.Formulae["leaf"].Name != "leaf" || subset.Generation != full.Generation {
		t.Fatalf("wrong dependency subset: %+v", subset)
	}
	state := model.Snapshot{Records: map[string]model.Record{
		"lib": {Formula: planFormula("lib", "1"), Status: model.Ready},
		"app": {Formula: planFormula("app", "1", "lib"), Status: model.Ready, Dependencies: []model.RuntimeDependency{{Name: "lib", Version: "1"}}},
	}}
	request := model.Request{Action: "upgrade", Names: []string{"lib"}}
	want, err := resolver.Build(request, full.Generation, full.Formulae, state, client.Platform, "/opt/gobrew")
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolver.Build(request, subset.Generation, subset.Formulae, state, client.Platform, "/opt/gobrew")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || len(got.Packages) != 3 {
		t.Fatalf("subset changed reverse dependency plan: got %+v, want %+v", got, want)
	}
}

func TestLoadForPlanFallsBackForAliasesAndMissingKeys(t *testing.T) {
	lib, app := planFormula("lib", "1"), planFormula("app", "1", "l")
	lib.Aliases = []string{"l"}
	snapshot := Snapshot{Generation: strings.Repeat("b", 64), Formulae: map[string]model.Formula{"lib": lib, "app": app, "unrelated": planFormula("unrelated", "1")}}
	client := Client{Dir: t.TempDir(), Platform: model.Platform{OS: "darwin", Arch: "arm64", Major: 15}}
	if err := client.publish(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	full, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"l", "missing", "app"} {
		t.Run(name, func(t *testing.T) {
			got, err := client.LoadForPlan([]string{name})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, full) {
				t.Fatalf("missing key/alias did not preserve full lookup semantics: %+v", got)
			}
		})
	}
}

func TestSearchMatchesNamesAliasesDescriptionsAndRegexLikeBrew(t *testing.T) {
	s := Snapshot{Formulae: map[string]model.Formula{
		"uv":          {Name: "uv", Aliases: []string{"astral-uv"}, Description: "Extremely fast Python package installer"},
		"ruff":        {Name: "ruff", Description: "Extremely fast Python linter"},
		"python@3.13": {Name: "python@3.13", Description: "Interpreted language"},
		"linux-only":  {Name: "linux-only", UnsupportedReason: "requires Linux"},
	}}
	names := func(query string, desc bool) string {
		t.Helper()
		result, err := s.Search(query, desc)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range result {
			out = append(out, f.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		query string
		desc  bool
		want  string
	}{
		{"UV", false, "uv"},                // case-insensitive name
		{"astral_uv", false, "uv"},         // aliases, punctuation simplified away
		{"python", false, "python@3.13"},   // names only: not uv's description
		{"python", true, "ruff,uv"},        // --desc searches descriptions
		{"/^p.*@3/", false, "python@3.13"}, // regex
		{"linux", false, ""},               // other platforms omitted
	} {
		if got := names(tc.query, tc.desc); got != tc.want {
			t.Fatalf("Search(%q, %v) = %q, want %q", tc.query, tc.desc, got, tc.want)
		}
	}
	if _, err := s.Search("/(/", false); err == nil {
		t.Fatal("invalid regex accepted")
	}
}
