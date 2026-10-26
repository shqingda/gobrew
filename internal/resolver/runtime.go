package resolver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

// NormalizeRuntime removes historical transitive receipt entries that no longer
// belong to the dependency graph being installed. Homebrew also applies receipt
// minima while expanding the current graph, not as independent install requests:
// https://github.com/Homebrew/brew/blob/main/Library/Homebrew/formula_installer.rb
// Direct declarations and observed binary links remain authoritative even when
// a newer recipe omits them. Only verified linkage scans permit filtering.
// The returned runtime slices are independent of prepared; the input is unchanged.
func NormalizeRuntime(plan model.Plan, prepared []model.PreparedPackage, state model.Snapshot) ([]model.PreparedPackage, error) {
	available := runtimeFormulae(plan, state)
	planned := make(map[string]model.Formula, len(plan.Packages))
	for _, pkg := range plan.Packages {
		planned[pkg.Formula.Name] = pkg.Formula
	}
	result := slices.Clone(prepared)
	for i, pkg := range prepared {
		name := pkg.Package.Formula.Name
		if !pkg.LinkageChecked {
			return nil, fmt.Errorf("%w: %s prepared artifact lacks verified linkage metadata; prepare it again", model.ErrUnsupported, name)
		}
		formula, ok := planned[name]
		if !ok {
			return nil, fmt.Errorf("%w: prepared package %s is absent from plan", model.ErrStalePlan, name)
		}
		current, err := dependencyClosure(formula, available)
		if err != nil {
			return nil, err
		}
		linked := make(map[string]bool, len(pkg.LinkedDependencies))
		for _, input := range pkg.LinkedDependencies {
			dep, err := runtimeName(input)
			if err != nil {
				return nil, err
			}
			if dep != name { // A keg can load one of its own libraries.
				linked[dep] = true
			}
		}
		facts := make(map[string]model.RuntimeDependency, len(pkg.RuntimeDependencies))
		kept := make([]model.RuntimeDependency, 0, len(pkg.RuntimeDependencies))
		for _, fact := range pkg.RuntimeDependencies {
			dep, err := runtimeName(fact.Name)
			if err != nil {
				return nil, err
			}
			if _, duplicate := facts[dep]; duplicate {
				return nil, fmt.Errorf("%w: %s receipt repeats runtime dependency %s", model.ErrUnsupported, name, dep)
			}
			fact.Name = dep
			facts[dep] = fact
			if fact.DeclaredDirectly || current[dep] || linked[dep] {
				kept = append(kept, fact)
			}
		}
		for dep := range linked {
			fact, ok := facts[dep]
			if !ok || fact.Version == "" {
				return nil, fmt.Errorf("%w: %s links directly to %s without a versioned receipt fact", model.ErrUnsupported, name, dep)
			}
		}
		result[i].RuntimeDependencies = kept
		result[i].LinkedDependencies = slices.Clone(pkg.LinkedDependencies)
	}
	if err := validateRuntimeChecks(plan, result); err != nil {
		return nil, err
	}
	return result, nil
}

// Every direct consumer of a conditionally reused old keg needs a recorded
// minimum. A minimum from an unrelated bottle cannot stand in for that proof.
func validateRuntimeChecks(plan model.Plan, prepared []model.PreparedPackage) error {
	if len(plan.RuntimeChecks) == 0 {
		return nil
	}
	checks := make(map[string]bool, len(plan.RuntimeChecks))
	for _, name := range plan.RuntimeChecks {
		checks[name] = false
	}
	formulae := make(map[string]model.Formula, len(plan.Packages))
	for _, pkg := range plan.Packages {
		formulae[pkg.Formula.Name] = pkg.Formula
	}
	for _, pkg := range prepared {
		name := pkg.Package.Formula.Name
		formula, ok := formulae[name]
		if !ok {
			return fmt.Errorf("%w: prepared package %s is absent from plan", model.ErrStalePlan, name)
		}
		facts := make(map[string]model.RuntimeDependency, len(pkg.RuntimeDependencies))
		for _, fact := range pkg.RuntimeDependencies {
			facts[strings.TrimPrefix(fact.Name, "homebrew/core/")] = fact
		}
		consumers := append(slices.Clone(formula.Dependencies), pkg.LinkedDependencies...)
		for _, input := range consumers {
			dep := strings.TrimPrefix(input, "homebrew/core/")
			if _, required := checks[dep]; !required {
				continue
			}
			fact, ok := facts[dep]
			if !ok || fact.Version == "" {
				return fmt.Errorf("%w: %s bottle has no minimum version for conditionally reused %s; upgrade %s and create a new plan", model.ErrUnsupported, name, dep, dep)
			}
			checks[dep] = true
		}
	}
	for _, name := range plan.RuntimeChecks {
		if !checks[name] {
			return fmt.Errorf("%w: no bottle minimum proves reuse of %s; upgrade %s and create a new plan", model.ErrUnsupported, name, name)
		}
	}
	return nil
}

func runtimeFormulae(plan model.Plan, state model.Snapshot) map[string]model.Formula {
	available := make(map[string]model.Formula, len(state.Records)+len(plan.Packages))
	for name, record := range state.Records {
		if record.Status == model.Ready {
			available[name] = record.Formula
		}
	}
	for _, pkg := range plan.Packages {
		available[pkg.Formula.Name] = pkg.Formula
	}
	return available
}

func dependencyClosure(root model.Formula, available map[string]model.Formula) (map[string]bool, error) {
	closure := make(map[string]bool)
	pending := slices.Clone(root.Dependencies)
	for len(pending) > 0 {
		input := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		name, err := runtimeName(input)
		if err != nil {
			return nil, err
		}
		if closure[name] {
			continue
		}
		closure[name] = true
		formula, ok := available[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s current dependency %s is neither planned nor ready", model.ErrConflict, root.Name, name)
		}
		pending = append(pending, formula.Dependencies...)
	}
	return closure, nil
}

func runtimeName(input string) (string, error) {
	name := strings.TrimPrefix(input, "homebrew/core/")
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%w: runtime dependency is not an official core name: %q", model.ErrUnsupported, input)
	}
	return name, nil
}
