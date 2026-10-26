package resolver

import (
	"errors"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

func TestNormalizeRuntimeDropsOnlyHistoricalTransitiveFacts(t *testing.T) {
	for _, test := range []struct {
		name         string
		declared     bool
		linked       bool
		installed    bool
		wantKept     int
		wantConflict bool
	}{
		{name: "obsolete transitive", wantKept: 1},
		{name: "unrelated old dependency installed", installed: true, wantKept: 1},
		{name: "declared direct", declared: true, wantKept: 2, wantConflict: true},
		{name: "actual direct binary linkage", linked: true, wantKept: 2, wantConflict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := formula("app", "1", "simdutf")
			utf := formula("simdutf", "7", "icu4c@79")
			icu := formula("icu4c@79", "79")
			plan := model.Plan{Packages: []model.Package{{Formula: icu}, {Formula: utf}, {Formula: app}}}
			state := model.Snapshot{Records: map[string]model.Record{}}
			if test.installed {
				state.Records["icu4c@78"] = model.Record{Formula: formula("icu4c@78", "78"), Status: model.Ready}
			}
			prepared := []model.PreparedPackage{{Package: model.Package{Formula: app}, LinkageChecked: true, LinkedDependencies: []string{"simdutf"}, RuntimeDependencies: []model.RuntimeDependency{
				{Name: "simdutf", Version: "7", DeclaredDirectly: true},
				{Name: "icu4c@78", Version: "78", DeclaredDirectly: test.declared},
			}}}
			if test.linked {
				prepared[0].LinkedDependencies = append(prepared[0].LinkedDependencies, "icu4c@78")
			}
			normalized, err := NormalizeRuntime(plan, prepared, state)
			if err != nil {
				t.Fatal(err)
			}
			if len(normalized[0].RuntimeDependencies) != test.wantKept || len(prepared[0].RuntimeDependencies) != 2 {
				t.Fatalf("normalization kept wrong facts or mutated input: %+v", normalized)
			}
			err = ValidateRuntime(plan, normalized, state)
			if errors.Is(err, model.ErrConflict) != test.wantConflict || (err != nil && !test.wantConflict) {
				t.Fatalf("runtime validation = %v, want conflict %t", err, test.wantConflict)
			}
		})
	}
}

func TestNormalizeRuntimeRequiresActualLinkageEvidence(t *testing.T) {
	app := formula("app", "1")
	plan := model.Plan{Packages: []model.Package{{Formula: app}}}
	for _, prepared := range []model.PreparedPackage{
		{Package: model.Package{Formula: app}},
		{Package: model.Package{Formula: app}, LinkageChecked: true, LinkedDependencies: []string{"missing-receipt-fact"}},
	} {
		if _, err := NormalizeRuntime(plan, []model.PreparedPackage{prepared}, model.Snapshot{}); !errors.Is(err, model.ErrUnsupported) {
			t.Fatalf("unsupported linkage evidence accepted: %v", err)
		}
	}
}

func TestNormalizeRuntimePreservesCurrentTransitiveMinimum(t *testing.T) {
	app, middle, leaf := formula("app", "1", "middle"), formula("middle", "1", "leaf"), formula("leaf", "1")
	plan := model.Plan{Packages: []model.Package{{Formula: app}, {Formula: middle}}}
	state := model.Snapshot{Records: map[string]model.Record{"leaf": {Formula: leaf, Status: model.Ready}}}
	prepared := []model.PreparedPackage{{Package: model.Package{Formula: app}, LinkageChecked: true, RuntimeDependencies: []model.RuntimeDependency{{Name: "leaf", Version: "2"}}}}
	normalized, err := NormalizeRuntime(plan, prepared, state)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateRuntime(plan, normalized, state); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("current transitive minimum ignored: %v", err)
	}
}
