package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/shqingda/gobrew/internal/catalog"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/postinstall"
	"github.com/shqingda/gobrew/internal/resolver"
)

func (e *Engine) Plan(ctx context.Context, req model.Request) (model.Plan, error) {
	if err := ctx.Err(); err != nil {
		return model.Plan{}, err
	}
	snap, err := e.Installed()
	if err != nil {
		return model.Plan{}, err
	}
	if snap.RecoveryRequired {
		return model.Plan{}, model.ErrRecoveryRequired
	}
	// The common no-op path never starts networking: it reads only the
	// requested formulae from the local catalog to tell up-to-date from outdated.
	if req.Action == "install" && len(req.Names) > 0 && !req.OnlyDependencies {
		if plan, ok, err := e.installedNoop(req, snap); err != nil || ok {
			return plan, err
		}
	}
	host, err := e.Host()
	if err != nil {
		return model.Plan{}, err
	}
	client, err := e.client()
	if err != nil {
		return model.Plan{}, err
	}
	// Include every installed recipe: a changed dependency can require an
	// installed reverse dependent and that recipe's new forward dependencies.
	names := make([]string, 0, len(req.Names)+len(snap.Records))
	names = append(names, req.Names...)
	for name := range snap.Records {
		names = append(names, name)
	}
	var cat catalog.Snapshot
	if len(req.Versions) > 0 {
		// A historical recipe can need dependencies absent from today's closure.
		// Load one complete generation before replacing its private recipe copy.
		cat, err = client.Load()
	} else {
		cat, err = client.LoadForPlan(names)
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, model.ErrNotFound) {
		cat, err = client.Update(ctx)
	}
	if err != nil {
		return model.Plan{}, err
	}
	resolved, historic, err := e.explicitVersions(ctx, req, cat.Formulae, host)
	if err != nil {
		return model.Plan{}, err
	}
	plan, err := resolver.Build(resolved, cat.Generation, cat.Formulae, snap, host, e.config.Prefix)
	plan.Request = req
	if err != nil {
		return plan, err
	}
	for i := range plan.Packages {
		plan.Packages[i].Historic = historic[plan.Packages[i].Formula.Name]
	}
	for _, pkg := range plan.Packages {
		if err = postinstall.Validate(pkg.Formula.Steps); err != nil {
			return model.Plan{}, fmt.Errorf("%w: %s post-install: %v", model.ErrUnsupported, pkg.Formula.Name, err)
		}
	}
	if err := e.installer().ValidateState(model.Prepared{Plan: plan}, snap); err != nil {
		return model.Plan{}, err
	}
	return plan, nil
}

// installedNoop returns a plan without packages when every requested formula
// is installed and either up to date or held back (pinned, or upgrades on
// install disabled). It reports ok=false to fall back to full planning.
func (e *Engine) installedNoop(req model.Request, snap model.Snapshot) (model.Plan, bool, error) {
	var promote, unchanged, held []string
	records := map[string]model.Record{}
	for _, requested := range req.Names {
		name := strings.TrimPrefix(requested, "homebrew/core/")
		r, ok := snap.Records[name]
		if !ok {
			for canonical, installed := range snap.Records {
				if slices.Contains(installed.Formula.Aliases, name) {
					if ok {
						return model.Plan{}, false, fmt.Errorf("%w: ambiguous installed alias %s", model.ErrConflict, requested)
					}
					r, ok = installed, true
					name = canonical
				}
			}
		}
		// version-install is a no-op only when exactly that version is installed.
		if version, exact := req.Versions[name]; ok && exact && r.Formula.Version != version && r.Formula.PkgVersion() != version {
			ok = false
		}
		if !ok {
			return model.Plan{}, false, nil
		}
		if r.Status != model.Ready {
			return model.Plan{}, false, fmt.Errorf("%w: %s", model.ErrNeedsRepair, name)
		}
		records[name] = r
	}
	names := slices.Sorted(maps.Keys(records))
	client, err := e.client()
	if err != nil {
		return model.Plan{}, false, err
	}
	// Without a local catalog (or with a formula it no longer lists) nothing is
	// known to be newer, so the no-op stays offline; auto-update keeps it fresh.
	cat, err := client.LoadForPlan(names)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, model.ErrNotFound) {
		return model.Plan{}, false, err
	}
	for _, name := range names {
		r := records[name]
		if _, exact := req.Versions[name]; !exact {
			if f, known := cat.Formulae[name]; known && resolver.Compare(r.Formula, f) < 0 {
				if !r.Pinned && !req.NoInstallUpgrade {
					return model.Plan{}, false, nil
				}
				held = append(held, name)
				continue
			}
		}
		unchanged = append(unchanged, name)
		if !r.Requested && req.MarksRequested() {
			promote = append(promote, name)
		}
	}
	plan := model.Plan{Request: req, Prefix: e.config.Prefix, StateGeneration: snap.Generation, Unchanged: unchanged, Promote: promote, Held: held}
	if err := e.installer().ValidateState(model.Prepared{Plan: plan}, snap); err != nil {
		return model.Plan{}, false, err
	}
	return plan, true, nil
}

// explicitVersions applies version-install requests. The current catalog
// version keeps its signed metadata; an older version replaces the formula in
// this plan's private catalog copy after History verifies it against GHCR and
// homebrew-core.
func (e *Engine) explicitVersions(ctx context.Context, req model.Request, formulae map[string]model.Formula, host model.Platform) (model.Request, map[string]bool, error) {
	out := req
	out.Exact = nil
	historic := map[string]bool{}
	names := make([]string, 0, len(req.Versions))
	for name := range req.Versions {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		version := req.Versions[name]
		if req.Action != "install" {
			return req, nil, fmt.Errorf("%w: %s does not take versions; use gobrew version-install %s %s", model.ErrUnsupported, req.Action, name, version)
		}
		if !slices.Contains(req.Names, name) {
			return req, nil, fmt.Errorf("%w: version given for unrequested formula %s", model.ErrConflict, name)
		}
		if !catalog.ValidVersion(version) {
			return req, nil, fmt.Errorf("%w: invalid version %q", model.ErrUnsupported, version)
		}
		current, known := formulae[name]
		if !known {
			return req, nil, fmt.Errorf("%w: formula %s", model.ErrNotFound, name)
		}
		out.Exact = append(out.Exact, name)
		if version == current.Version || version == current.PkgVersion() {
			continue
		}
		history := catalog.History{HTTP: e.config.HTTP, Platform: host, Token: e.config.GitHubToken}
		older, err := history.Formula(ctx, current, version)
		if err != nil {
			return req, nil, err
		}
		formulae[name] = older
		historic[name] = true
	}
	return out, historic, nil
}
