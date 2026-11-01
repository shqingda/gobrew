package engine

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/shqingda/gobrew/internal/artifact"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/resolver"
)

func (e *Engine) manager() (*artifact.Manager, error) {
	host, err := e.Host()
	if err != nil {
		return nil, err
	}
	return artifact.New(artifact.Config{CacheDir: filepath.Join(e.config.CacheDir, "artifacts"), Prefix: e.config.Prefix, Platform: host, HTTP: e.config.HTTP, DownloadConcurrency: e.config.Downloads, PrepareConcurrency: e.config.Jobs, BlobBudget: e.config.BlobBudget, TreeBudget: e.config.TreeBudget}), nil
}

// PreparedOperation owns cache leases. Close must be called even if Apply fails.
// Leases are operation-scoped; the engine owns no long-lived database handle.
type PreparedOperation struct {
	Value    model.Prepared
	manager  *artifact.Manager
	once     sync.Once
	closeErr error
}

func (p *PreparedOperation) Close() error {
	p.once.Do(func() {
		if p.manager != nil {
			p.closeErr = p.manager.Release(p.Value)
		}
	})
	return p.closeErr
}
func (e *Engine) Prepare(ctx context.Context, plan model.Plan, events chan<- model.Event) (*PreparedOperation, error) {
	if len(plan.Packages) == 0 {
		return &PreparedOperation{Value: model.Prepared{Plan: plan}}, nil
	}
	m, err := e.manager()
	if err != nil {
		return nil, err
	}
	value, err := m.Prepare(ctx, plan, events)
	if err != nil {
		return nil, err
	}
	p := &PreparedOperation{Value: value, manager: m}
	snap, err := e.Installed()
	if err == nil && snap.RecoveryRequired {
		err = model.ErrRecoveryRequired
	}
	if err == nil {
		value.Packages, err = resolver.NormalizeRuntime(plan, value.Packages, snap)
		p.Value = value
	}
	if err == nil {
		err = e.installer().ValidateState(value, snap)
	}
	if err == nil {
		err = resolver.ValidateRuntime(plan, value.Packages, snap)
	}
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}
func (e *Engine) Apply(ctx context.Context, p *PreparedOperation, events chan<- model.Event) (model.Result, error) {
	if err := e.checkMutation(); err != nil {
		return model.Result{}, err
	}
	return e.installer().Apply(ctx, p.Value, events)
}
func (e *Engine) Execute(ctx context.Context, req model.Request, events chan<- model.Event) (result model.Result, err error) {
	var timings model.Timings
	defer func() { result.Timings = &timings }()
	if err := e.checkMutation(); err != nil {
		return model.Result{}, err
	}
	start := time.Now()
	plan, err := e.Plan(ctx, req)
	if errors.Is(err, model.ErrRecoveryRequired) {
		if err = e.Recover(ctx); err != nil {
			timings.Plan = float64(time.Since(start)) / float64(time.Millisecond)
			return model.Result{}, err
		}
		plan, err = e.Plan(ctx, req)
	}
	timings.Plan = float64(time.Since(start)) / float64(time.Millisecond)
	if err != nil {
		return model.Result{}, err
	}
	if len(plan.Packages) == 0 && len(plan.Promote) == 0 {
		return model.Result{Unchanged: plan.Unchanged, Held: plan.Held}, nil
	}
	for _, name := range slices.Sorted(maps.Keys(plan.Upgrading)) {
		model.Emit(events, model.Event{Phase: "outdated", Package: name, Message: plan.Upgrading[name]})
	}
	start = time.Now()
	p, err := e.Prepare(ctx, plan, events)
	timings.Prepare = float64(time.Since(start)) / float64(time.Millisecond)
	if err != nil {
		notExecuted := make([]string, 0, len(plan.Packages))
		for _, pkg := range plan.Packages {
			notExecuted = append(notExecuted, pkg.Formula.Name)
		}
		return model.Result{Unchanged: plan.Unchanged, NotExecuted: notExecuted, Held: plan.Held}, err
	}
	defer func() { err = errors.Join(err, p.Close()) }()
	start = time.Now()
	result, err = e.Apply(ctx, p, events)
	timings.Apply = float64(time.Since(start)) / float64(time.Millisecond)
	result.Held = plan.Held
	for _, pkg := range plan.Packages {
		if pkg.Historic && slices.Contains(result.Succeeded, pkg.Formula.Name) {
			result.Historic = append(result.Historic, pkg.Formula.Name)
		}
	}
	return result, err
}

// Recover resolves an interrupted filesystem transaction without rerunning
// post-install commands. Planning remains read-only for interactive clients.
func (e *Engine) Recover(ctx context.Context) error {
	if err := e.checkMutation(); err != nil {
		return err
	}
	return e.installer().Recover(ctx)
}
