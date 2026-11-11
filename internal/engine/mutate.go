package engine

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

func (e *Engine) Pin(ctx context.Context, names []string, pinned bool) error {
	if err := e.checkMutation(); err != nil {
		return err
	}
	return e.installer().MutateRecords(ctx, func(s *model.Snapshot) error {
		for _, name := range names {
			r, ok := s.Records[name]
			if !ok {
				return fmt.Errorf("%w: %s not installed", model.ErrNotFound, name)
			}
			r.Pinned = pinned
			s.Records[name] = r
		}
		return nil
	})
}
func (e *Engine) Link(ctx context.Context, names []string, linked, force bool) ([]model.LinkResult, error) {
	if err := e.checkMutation(); err != nil {
		return nil, err
	}
	return e.installer().SetLinked(ctx, names, linked, force)
}

// PreviewLink is the read-only form of Link behind link and unlink --dry-run.
func (e *Engine) PreviewLink(names []string, linked bool) ([]model.LinkResult, error) {
	return e.installer().PreviewLinked(names, linked)
}

func (e *Engine) PostInstall(ctx context.Context, names []string, events chan<- model.Event) error {
	if err := e.checkMutation(); err != nil {
		return err
	}
	return e.installer().PostInstall(ctx, names, events)
}

func (e *Engine) Uninstall(ctx context.Context, names []string) ([]string, error) {
	if err := e.checkMutation(); err != nil {
		return nil, err
	}
	removed := slices.Clone(names)
	err := e.installer().MutateRecords(ctx, func(s *model.Snapshot) error {
		removing := make(map[string]bool)
		for _, n := range names {
			if _, ok := s.Records[n]; !ok {
				return fmt.Errorf("%w: No such keg: %s", model.ErrNotFound, filepath.Join(e.config.Prefix, "Cellar", n))
			}
			removing[n] = true
		}
		// Like brew, name every installed dependent of each refused formula.
		dependents := map[string][]string{}
		for name, r := range s.Records {
			if removing[name] {
				continue
			}
			for _, dep := range r.Dependencies {
				if removing[dep.Name] {
					dependents[dep.Name] = append(dependents[dep.Name], name)
				}
			}
		}
		if len(dependents) > 0 {
			var refusals []string
			for _, required := range slices.Sorted(maps.Keys(dependents)) {
				users := dependents[required]
				slices.Sort(users)
				verb := "is"
				if len(users) > 1 {
					verb = "are"
				}
				refusals = append(refusals, fmt.Sprintf("Refusing to uninstall %s because it is required by %s, which %s currently installed.", required, strings.Join(users, ", "), verb))
			}
			return fmt.Errorf("%w: %s\nUninstall the formulae that require it first, or keep it; gobrew never leaves an installed formula without its dependencies.", model.ErrConflict, strings.Join(refusals, "\n"))
		}
		for _, n := range names {
			delete(s.Records, n)
		}
		unused := orphaned(*s)
		for _, n := range unused {
			delete(s.Records, n)
		}
		removed = append(removed, unused...)
		return nil
	})
	return removed, err
}
func (e *Engine) Autoremove(ctx context.Context, dryRun bool) ([]string, error) {
	if dryRun {
		snap, err := e.Installed()
		if err != nil {
			return nil, err
		}
		return orphaned(snap), nil
	}
	if err := e.checkMutation(); err != nil {
		return nil, err
	}
	var removed []string
	err := e.installer().MutateRecords(ctx, func(s *model.Snapshot) error {
		removed = orphaned(*s)
		for _, name := range removed {
			delete(s.Records, name)
		}
		return nil
	})
	return removed, err
}
func orphaned(s model.Snapshot) []string {
	keep := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		if keep[name] {
			return
		}
		keep[name] = true
		for _, dep := range s.Records[name].Dependencies {
			visit(dep.Name)
		}
	}
	for name, r := range s.Records {
		if r.Requested || r.Pinned {
			visit(name)
		}
	}
	names := make([]string, 0)
	for name := range s.Records {
		if !keep[name] {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
