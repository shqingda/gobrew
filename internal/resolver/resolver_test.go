package resolver

import (
	"errors"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

var host = model.Platform{OS: "darwin", Arch: "arm64", Major: 15}

func formula(name, version string, deps ...string) model.Formula {
	return model.Formula{Name: name, Version: version, Dependencies: deps, Bottles: map[string]model.Bottle{"arm64_sequoia": {Tag: "arm64_sequoia", Cellar: ":any", URL: "https://ghcr.io/v2/homebrew/core/" + name + "/blobs/sha256:" + strings.Repeat("a", 64), SHA256: strings.Repeat("a", 64)}}}
}

func TestPlanDependencyOrderAliasAndCycle(t *testing.T) {
	a, b := formula("a", "1", "b"), formula("b", "1")
	a.Aliases = []string{"alias"}
	plan, err := Build(model.Request{Action: "install", Names: []string{"alias"}}, "snapshot", map[string]model.Formula{"a": a, "b": b}, model.Snapshot{}, host, "/opt/gobrew")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Packages) != 2 || plan.Packages[0].Formula.Name != "b" || plan.Packages[1].Formula.Name != "a" {
		t.Fatalf("wrong order: %+v", plan.Packages)
	}
	b.Dependencies = []string{"a"}
	if _, err = Build(model.Request{Action: "install", Names: []string{"a"}}, "snapshot", map[string]model.Formula{"a": a, "b": b}, model.Snapshot{}, host, "/opt/gobrew"); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestUpgradePinnedReverseDependencyBlocksBeforeChanges(t *testing.T) {
	lib, app := formula("lib", "2"), formula("app", "2", "lib")
	state := model.Snapshot{Records: map[string]model.Record{"lib": {Formula: formula("lib", "1"), Status: model.Ready}, "app": {Formula: formula("app", "1", "lib"), Pinned: true, Status: model.Ready, Dependencies: []model.RuntimeDependency{{Name: "lib", Version: "1"}}}}}
	_, err := Build(model.Request{Action: "upgrade", Names: []string{"lib"}}, "snapshot", map[string]model.Formula{"lib": lib, "app": app}, state, host, "/opt/gobrew")
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("expected pinned conflict, got %v", err)
	}
	appRecord := state.Records["app"]
	appRecord.Pinned = false
	state.Records["app"] = appRecord
	plan, err := Build(model.Request{Action: "upgrade", Names: []string{"lib"}}, "snapshot", map[string]model.Formula{"lib": lib, "app": app}, state, host, "/opt/gobrew")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Packages) != 2 {
		t.Fatalf("missing reverse dependency: %+v", plan)
	}
}

func TestVersionOrdering(t *testing.T) {
	for _, pair := range [][2]string{{"1.9", "1.10"}, {"1.0rc1", "1.0"}, {"1.0", "1.0p1"}, {"2025.1", "2026.1"}, {"1.0alpha1", "1.0beta1"}} {
		if Compare(formula("a", pair[0]), formula("a", pair[1])) >= 0 {
			t.Errorf("%s must be older than %s", pair[0], pair[1])
		}
	}
	a, b := formula("a", "13"), formula("a", "1.0")
	b.VersionScheme = 1
	if Compare(a, b) >= 0 {
		t.Fatal("version_scheme not respected")
	}
}

func TestCompatibleOutdatedDependentIsNotUpgradedIncidentally(t *testing.T) {
	abi := 1
	oldLib, newLib := formula("lib", "1"), formula("lib", "2")
	oldLib.CompatibilityVersion, newLib.CompatibilityVersion = &abi, &abi
	oldApp, newApp := formula("app", "1", "lib"), formula("app", "2", "lib")
	snapshot := model.Snapshot{Records: map[string]model.Record{
		"lib": {Formula: oldLib, Status: model.Ready},
		"app": {Formula: oldApp, Status: model.Ready, Dependencies: []model.RuntimeDependency{{Name: "lib", Version: "1"}}},
	}}
	for _, action := range []string{"install", "upgrade"} {
		plan, err := Build(model.Request{Action: action, Names: []string{"lib"}}, "snapshot", map[string]model.Formula{"lib": newLib, "app": newApp}, snapshot, host, "/opt/gobrew")
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Packages) != 1 || plan.Packages[0].Formula.Name != "lib" {
			t.Fatalf("%s also upgraded a compatible dependent: %+v", action, plan.Packages)
		}
	}
}

func TestBottleNeverFallsForwardOrAcrossArchitecture(t *testing.T) {
	f := formula("a", "1")
	f.Bottles = map[string]model.Bottle{"arm64_tahoe": {Tag: "arm64_tahoe"}, "sequoia": {Tag: "sequoia"}}
	if _, err := SelectBottle(f, host, "/opt/gobrew"); !errors.Is(err, model.ErrUnsupported) {
		t.Fatalf("unexpected bottle selection: %v", err)
	}
}

func TestRuntimeMinimumIsNotBypassedByCompatibilityVersion(t *testing.T) {
	compat := 1
	lib := formula("lib", "1")
	lib.CompatibilityVersion = &compat
	state := model.Snapshot{Records: map[string]model.Record{"lib": {Formula: lib, Status: model.Ready}}}
	prepared := []model.PreparedPackage{{Package: model.Package{Formula: formula("app", "1")}, RuntimeDependencies: []model.RuntimeDependency{{Name: "lib", Version: "2", CompatibilityVersion: &compat}}}}
	if err := ValidateRuntime(model.Plan{}, prepared, state); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("runtime lower bound ignored: %v", err)
	}
}

func TestInstallUpgradesOutdatedUnlessHeldLikeHomebrew(t *testing.T) {
	old, current := formula("demo", "1"), formula("demo", "2")
	catalog := map[string]model.Formula{"demo": current}
	build := func(record model.Record, noUpgrade bool) model.Plan {
		t.Helper()
		state := model.Snapshot{Records: map[string]model.Record{"demo": record}}
		plan, err := Build(model.Request{Action: "install", Names: []string{"demo"}, NoInstallUpgrade: noUpgrade}, "snapshot", catalog, state, host, "/opt/gobrew")
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	plan := build(model.Record{Formula: old, Status: model.Ready}, false)
	if len(plan.Packages) != 1 || plan.Upgrading["demo"] != "1" || len(plan.Held) != 0 {
		t.Fatalf("outdated install was not upgraded: %+v", plan)
	}
	for name, record := range map[string]model.Record{"pinned": {Formula: old, Status: model.Ready, Pinned: true}, "no-upgrade": {Formula: old, Status: model.Ready}} {
		plan = build(record, name == "no-upgrade")
		if len(plan.Packages) != 0 || len(plan.Held) != 1 || len(plan.Upgrading) != 0 {
			t.Fatalf("%s: outdated install was not held: %+v", name, plan)
		}
	}
	plan = build(model.Record{Formula: current, Status: model.Ready}, false)
	if len(plan.Packages) != 0 || len(plan.Unchanged) != 1 || len(plan.Held) != 0 {
		t.Fatalf("up-to-date install changed: %+v", plan)
	}
}

func TestExistingPackageActionsRejectMissingRoot(t *testing.T) {
	for _, action := range []string{"upgrade", "reinstall"} {
		t.Run(action, func(t *testing.T) {
			f := formula("demo", "1")
			f.Aliases = []string{"alias"}
			_, err := Build(model.Request{Action: action, Names: []string{"alias"}}, "snapshot", map[string]model.Formula{"demo": f}, model.Snapshot{}, host, "/opt/gobrew")
			if !errors.Is(err, model.ErrNotFound) {
				t.Fatalf("%s installed missing package: %v", action, err)
			}
		})
	}
}

func TestConflictDeclaredByInstalledFormulaIsHonored(t *testing.T) {
	installed := formula("old", "1")
	installed.Conflicts = []string{"new"}
	_, err := Build(model.Request{Action: "install", Names: []string{"new"}}, "snapshot", map[string]model.Formula{"new": formula("new", "1")}, model.Snapshot{Records: map[string]model.Record{"old": {Formula: installed, Status: model.Ready}}}, host, "/opt/gobrew")
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("existing conflict ignored: %v", err)
	}
}

func TestNoLinkageDependencyDoesNotForceReverseUpgrade(t *testing.T) {
	old, new := formula("certs", "1"), formula("certs", "2")
	app := formula("app", "1", "certs")
	app.NoLinkageDependencies = []string{"certs"}
	state := model.Snapshot{Records: map[string]model.Record{"certs": {Formula: old, Status: model.Ready}, "app": {Formula: app, Status: model.Ready, Pinned: true}}}
	plan, err := Build(model.Request{Action: "upgrade", Names: []string{"certs"}}, "snapshot", map[string]model.Formula{"certs": new, "app": app}, state, host, "/opt/gobrew")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Packages) != 1 || plan.Packages[0].Formula.Name != "certs" {
		t.Fatalf("unexpected reverse upgrade: %+v", plan)
	}
}

func TestCompatibleOlderDependenciesNeedBottleMinimumProof(t *testing.T) {
	for _, test := range []struct {
		name    string
		minimum string
		want    error
	}{
		{"pinned dependency satisfies minimum", "1", nil},
		{"pinned dependency below minimum", "2", model.ErrConflict},
		{"missing minimum", "", model.ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			compat := 1
			old, latest, app := formula("lib", "1"), formula("lib", "2"), formula("app", "1", "lib")
			old.CompatibilityVersion, latest.CompatibilityVersion = &compat, &compat
			state := model.Snapshot{Records: map[string]model.Record{"lib": {Formula: old, Status: model.Ready, Pinned: true}}}
			plan, err := Build(model.Request{Action: "install", Names: []string{"app"}}, "snapshot", map[string]model.Formula{"lib": latest, "app": app}, state, host, "/opt/gobrew")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Packages) != 1 || plan.Packages[0].Formula.Name != "app" || len(plan.RuntimeChecks) != 1 || plan.RuntimeChecks[0] != "lib" {
				t.Fatalf("wrong conditional reuse plan: %+v", plan)
			}
			prepared := []model.PreparedPackage{{Package: plan.Packages[0], LinkageChecked: true}}
			if test.minimum != "" {
				prepared[0].RuntimeDependencies = []model.RuntimeDependency{{Name: "lib", Version: test.minimum, DeclaredDirectly: true}}
			}
			normalized, err := NormalizeRuntime(plan, prepared, state)
			if err == nil {
				err = ValidateRuntime(plan, normalized, state)
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("minimum result = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCompatibleReuseNeverBypassesExplicitRootsOrVersionEpochs(t *testing.T) {
	for _, test := range []struct {
		name   string
		action string
		names  []string
		epoch  int
	}{
		{"explicit upgrade", "upgrade", []string{"lib"}, 0},
		{"upgrade all", "upgrade", nil, 0},
		{"explicit reinstall", "reinstall", []string{"lib"}, 0},
		{"different epoch", "install", []string{"app"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			compat := 1
			old, latest, app := formula("lib", "1"), formula("lib", "2"), formula("app", "1", "lib")
			old.CompatibilityVersion, latest.CompatibilityVersion = &compat, &compat
			latest.VersionScheme = test.epoch
			state := model.Snapshot{Records: map[string]model.Record{"lib": {Formula: old, Status: model.Ready}}}
			plan, err := Build(model.Request{Action: test.action, Names: test.names}, "snapshot", map[string]model.Formula{"lib": latest, "app": app}, state, host, "/opt/gobrew")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.RuntimeChecks) != 0 || len(plan.Packages) == 0 || plan.Packages[0].Formula.Name != "lib" {
				t.Fatalf("explicit root or epoch change reused the old dependency: %+v", plan)
			}
		})
	}
}

func TestExactVersionMayReplaceButNotOverrideAPin(t *testing.T) {
	older, current := formula("demo", "1"), formula("demo", "2")
	state := model.Snapshot{Records: map[string]model.Record{"demo": {Formula: current, Status: model.Ready}}}
	catalog := map[string]model.Formula{"demo": older}
	if _, err := Build(model.Request{Action: "install", Names: []string{"demo"}}, "s", catalog, state, host, "/opt/gobrew"); err != nil {
		t.Fatal(err) // an implicit request keeps the newer installed version
	}
	exact := model.Request{Action: "install", Names: []string{"demo"}, Exact: []string{"demo"}}
	plan, err := Build(exact, "s", catalog, state, host, "/opt/gobrew")
	if err != nil || len(plan.Packages) != 1 || plan.Packages[0].Formula.Version != "1" {
		t.Fatalf("explicit downgrade = %+v, %v", plan.Packages, err)
	}
	record := state.Records["demo"]
	record.Pinned = true
	state.Records["demo"] = record
	if _, err := Build(exact, "s", catalog, state, host, "/opt/gobrew"); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "unpin") {
		t.Fatalf("pinned version replaced: %v", err)
	}
	state.Records["demo"] = model.Record{Formula: older, Status: model.Ready, Pinned: true}
	if plan, err := Build(exact, "s", catalog, state, host, "/opt/gobrew"); err != nil || len(plan.Unchanged) != 1 {
		t.Fatalf("same pinned version = %+v, %v", plan, err)
	}
}
