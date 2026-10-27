// Package resolver computes reproducible installation plans without I/O.
package resolver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+_.@-]*$`)

func Build(req model.Request, generation string, formulae map[string]model.Formula, state model.Snapshot, host model.Platform, prefix string) (model.Plan, error) {
	plan := model.Plan{Request: req, CatalogGeneration: generation, StateGeneration: state.Generation, Platform: host, Prefix: prefix}
	if req.Action != "install" && req.Action != "upgrade" && req.Action != "reinstall" {
		return plan, fmt.Errorf("%w: resolver action %q", model.ErrUnsupported, req.Action)
	}
	aliases := map[string]string{}
	for name, f := range formulae {
		for _, alias := range f.Aliases {
			if prior, ok := aliases[alias]; ok && prior != name {
				aliases[alias] = ""
			} else {
				aliases[alias] = name
			}
		}
	}
	canonical := func(input string) (string, error) {
		name := strings.TrimPrefix(input, "homebrew/core/")
		if !namePattern.MatchString(name) {
			return "", fmt.Errorf("%w: only named official core formulae are supported: %q", model.ErrUnsupported, input)
		}
		if _, ok := formulae[name]; ok {
			return name, nil
		}
		if alias, ok := aliases[name]; ok {
			if alias == "" {
				return "", fmt.Errorf("%w: ambiguous alias %s", model.ErrConflict, name)
			}
			return alias, nil
		}
		return "", fmt.Errorf("%w: formula %s", model.ErrNotFound, name)
	}
	requested := map[string]bool{}
	exact := map[string]bool{}
	for _, name := range req.Exact {
		exact[name] = true
	}
	roots := []string{}
	if len(req.Names) == 0 {
		if req.Action != "upgrade" {
			return plan, fmt.Errorf("%w: specify at least one formula", model.ErrNotFound)
		}
		for name, record := range state.Records {
			if !record.Pinned {
				roots = append(roots, name)
			}
		}
		sort.Strings(roots)
	} else {
		for _, input := range req.Names {
			name, err := canonical(input)
			if err != nil {
				return plan, err
			}
			if req.Action != "install" {
				if _, installed := state.Records[name]; !installed {
					return plan, fmt.Errorf("%w: cannot %s %s because it is not installed", model.ErrNotFound, req.Action, name)
				}
			}
			if !requested[name] {
				roots = append(roots, name)
				requested[name] = true
			}
		}
	}
	selected := map[string]model.Package{}
	unchanged := map[string]bool{}
	runtimeChecks := map[string]bool{}
	held := map[string]bool{}
	upgrading := map[string]string{}
	rootNames := make(map[string]bool, len(roots))
	for _, name := range roots {
		rootNames[name] = true
	}
	visiting := map[string]bool{}
	var visit func(string, bool) error
	visit = func(name string, force bool) error {
		if visiting[name] {
			return fmt.Errorf("%w: dependency cycle through %s", model.ErrConflict, name)
		}
		if _, ok := selected[name]; ok {
			return nil
		}
		f, ok := formulae[name]
		if !ok {
			return fmt.Errorf("%w: dependency %s", model.ErrNotFound, name)
		}
		record, installed := state.Records[name]
		if installed && record.Status != model.Ready {
			return fmt.Errorf("%w: %s has status %s", model.ErrNeedsRepair, name, record.Status)
		}
		if installed && !force && exact[name] {
			// An explicit version replaces any other installed version.
			if Compare(record.Formula, f) == 0 {
				unchanged[name] = true
				return nil
			}
		} else if installed && !force {
			if Compare(record.Formula, f) >= 0 {
				unchanged[name] = true
				return nil
			}
			// Like brew install, an outdated requested formula is upgraded unless
			// it is pinned or HOMEBREW_NO_INSTALL_UPGRADE's equivalent is set.
			if req.Action == "install" && requested[name] {
				if record.Pinned || req.NoInstallUpgrade {
					unchanged[name] = true
					held[name] = true
					return nil
				}
				upgrading[name] = record.Formula.PkgVersion()
			}
			oldABI, newABI := record.Formula.CompatibilityVersion, f.CompatibilityVersion
			if !rootNames[name] && oldABI != nil && newABI != nil && *oldABI == *newABI && record.Formula.VersionScheme == f.VersionScheme {
				// An ABI hint permits provisional reuse, including a pinned keg.
				// The plan explicitly requires a bottle minimum before activation.
				unchanged[name] = true
				runtimeChecks[name] = true
				return nil
			}
		}
		if installed && record.Pinned && exact[name] && Compare(record.Formula, f) != 0 {
			return fmt.Errorf("%w: %s is pinned at %s; run gobrew unpin %s first", model.ErrConflict, name, record.Formula.PkgVersion(), name)
		}
		if installed && record.Pinned && (Compare(record.Formula, f) != 0 || req.Action == "upgrade") {
			return fmt.Errorf("%w: %s is pinned; unpin it before changing this dependency closure", model.ErrConflict, name)
		}
		if installed && Compare(record.Formula, f) > 0 && !exact[name] {
			return fmt.Errorf("%w: refusing to downgrade %s", model.ErrConflict, name)
		}
		bottle, err := SelectBottle(f, host, prefix)
		if err != nil {
			return err
		}
		visiting[name] = true
		for _, depName := range f.Dependencies {
			dep, err := canonical(depName)
			if err != nil {
				return fmt.Errorf("%s dependency: %w", name, err)
			}
			if err = visit(dep, false); err != nil {
				return err
			}
		}
		delete(visiting, name)
		delete(unchanged, name)
		delete(runtimeChecks, name)
		selected[name] = model.Package{Formula: f, Bottle: bottle, Requested: requested[name] || record.Requested}
		return nil
	}
	for _, name := range roots {
		if _, ok := formulae[name]; !ok {
			return plan, fmt.Errorf("%w: installed formula %s is absent from the catalog", model.ErrNotFound, name)
		}
		if err := visit(name, req.Action == "reinstall"); err != nil {
			return plan, err
		}
	}
	// Old receipt edges remain authoritative for the reverse graph, even if
	// today's recipe removed a dependency. Iterate to include newly affected
	// dependents and their forward dependencies in the same checked plan.
	for changed := true; changed; {
		changed = false
		names := make([]string, 0, len(state.Records))
		for name := range state.Records {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			record := state.Records[name]
			if _, ok := selected[name]; ok {
				continue
			}
			affected := false
			deps := record.Dependencies
			if len(deps) == 0 {
				for _, dep := range record.Formula.Dependencies {
					deps = append(deps, model.RuntimeDependency{Name: dep})
				}
			}
			for _, dep := range deps {
				depName := strings.TrimPrefix(dep.Name, "homebrew/core/")
				if slices.Contains(record.Formula.NoLinkageDependencies, depName) {
					continue
				}
				target, ok := selected[depName]
				previous, existed := state.Records[depName]
				if !ok || !existed || Compare(previous.Formula, target.Formula) == 0 {
					continue
				}
				a, b := previous.Formula.CompatibilityVersion, target.Formula.CompatibilityVersion
				if a != nil && b != nil && *a == *b {
					continue
				}
				affected = true
				break
			}
			if !affected {
				continue
			}
			if record.Pinned {
				return plan, fmt.Errorf("%w: upgrading a dependency would affect pinned %s", model.ErrConflict, name)
			}
			if err := visit(name, true); err != nil {
				return plan, fmt.Errorf("reverse dependency %s: %w", name, err)
			}
			changed = true
		}
	}
	for name, pkg := range selected {
		for _, conflict := range pkg.Formula.Conflicts {
			conflict = strings.TrimPrefix(conflict, "homebrew/core/")
			_, active := state.Records[conflict]
			_, planned := selected[conflict]
			if active || planned {
				return plan, fmt.Errorf("%w: %s conflicts with %s", model.ErrConflict, name, conflict)
			}
		}
	}
	for name, record := range state.Records {
		if _, replaced := selected[name]; replaced {
			continue
		}
		for _, conflict := range record.Formula.Conflicts {
			conflict = strings.TrimPrefix(conflict, "homebrew/core/")
			if _, planned := selected[conflict]; planned {
				return plan, fmt.Errorf("%w: installed %s conflicts with %s", model.ErrConflict, name, conflict)
			}
		}
	}
	// Stable topological order makes plans reproducible regardless of map order.
	ordered := map[string]bool{}
	var order func(string) error
	stack := map[string]bool{}
	order = func(name string) error {
		if ordered[name] {
			return nil
		}
		if stack[name] {
			return fmt.Errorf("%w: dependency cycle through %s", model.ErrConflict, name)
		}
		pkg, ok := selected[name]
		if !ok {
			return nil
		}
		stack[name] = true
		deps := slices.Clone(pkg.Formula.Dependencies)
		sort.Strings(deps)
		for _, depName := range deps {
			dep, err := canonical(depName)
			if err != nil {
				return err
			}
			if err = order(dep); err != nil {
				return err
			}
		}
		delete(stack, name)
		ordered[name] = true
		plan.Packages = append(plan.Packages, pkg)
		return nil
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := order(name); err != nil {
			return plan, err
		}
	}
	for name := range unchanged {
		if _, ok := selected[name]; !ok {
			plan.Unchanged = append(plan.Unchanged, name)
		}
	}
	for name := range runtimeChecks {
		if _, changed := selected[name]; !changed {
			plan.RuntimeChecks = append(plan.RuntimeChecks, name)
		}
	}
	for name := range held {
		if _, changed := selected[name]; !changed {
			plan.Held = append(plan.Held, name)
		}
	}
	for name, version := range upgrading {
		if _, changed := selected[name]; changed {
			if plan.Upgrading == nil {
				plan.Upgrading = map[string]string{}
			}
			plan.Upgrading[name] = version
		}
	}
	for name := range requested {
		if rec, ok := state.Records[name]; ok && !rec.Requested {
			if _, changed := selected[name]; !changed {
				plan.Promote = append(plan.Promote, name)
			}
		}
	}
	sort.Strings(plan.Promote)
	sort.Strings(plan.Unchanged)
	sort.Strings(plan.RuntimeChecks)
	sort.Strings(plan.Held)
	encoded, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	sum := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(sum[:])
	return plan, nil
}

// ValidateRuntime checks the actual bottle receipts after Prepare. Metadata
// compatibility hints never override a bottle's recorded minimum version.
// The caller must re-plan, not mutate this plan, if additional upgrades are
// needed. Linkage inspection is performed by the artifact/activation layer.
func ValidateRuntime(plan model.Plan, prepared []model.PreparedPackage, state model.Snapshot) error {
	if err := validateRuntimeChecks(plan, prepared); err != nil {
		return err
	}
	available := runtimeFormulae(plan, state)
	for _, pkg := range prepared {
		for _, dep := range pkg.RuntimeDependencies {
			name := strings.TrimPrefix(dep.Name, "homebrew/core/")
			installed, ok := available[name]
			if !ok {
				return fmt.Errorf("%w: %s bottle needs runtime dependency %s; refresh and re-plan", model.ErrConflict, pkg.Package.Formula.Name, name)
			}
			if dep.Version == "" {
				return fmt.Errorf("%w: %s bottle has an unversioned runtime dependency %s", model.ErrUnsupported, pkg.Package.Formula.Name, name)
			}
			comparison := CompareVersions(installed.Version, dep.Version)
			if comparison < 0 || (comparison == 0 && installed.Revision < dep.Revision) {
				remedy := "upgrade " + name + " and create a new plan"
				if record, ok := state.Records[name]; ok && record.Pinned {
					remedy = "unpin and " + remedy
				}
				return fmt.Errorf("%w: %s bottle requires %s >= %s_%d; plan provides %s; %s", model.ErrConflict, pkg.Package.Formula.Name, name, dep.Version, dep.Revision, installed.PkgVersion(), remedy)
			}
		}
	}
	return nil
}
