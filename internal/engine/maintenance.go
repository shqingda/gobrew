package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/artifact"
	"github.com/shqingda/gobrew/internal/installer"
	"github.com/shqingda/gobrew/internal/model"
)

type Finding struct {
	Package string `json:"package,omitempty"`
	Problem string `json:"problem"`
	// Severity is "warning" for environment advice that does not make the
	// installation unhealthy; empty means a problem that needs repair.
	Severity string `json:"severity,omitempty"`
}

func (e *Engine) Doctor(ctx context.Context, repair bool) ([]Finding, error) {
	if repair {
		if err := e.checkMutation(); err != nil {
			return nil, err
		}
		if err := e.installer().Recover(ctx); err != nil {
			return nil, err
		}
	}
	snap, err := e.Installed()
	if err != nil {
		return nil, err
	}
	findings := make([]Finding, 0)
	if snap.RecoveryRequired {
		findings = append(findings, Finding{Problem: "interrupted operation requires recovery; run gobrew doctor --repair"})
	}
	for name, r := range snap.Records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := e.installer().ValidateState(model.Prepared{Plan: model.Plan{Unchanged: []string{name}}}, snap); err != nil {
			problem := err.Error()
			if r.PostInstallError != "" {
				problem += ": " + r.PostInstallError
			}
			findings = append(findings, Finding{Package: name, Problem: problem})
		}
		for rel, want := range r.Links {
			got, err := os.Readlink(filepath.Join(e.config.Prefix, rel))
			if err != nil || got != want {
				findings = append(findings, Finding{Package: name, Problem: "missing or changed link " + rel})
			}
		}
	}
	slices.SortFunc(findings, func(a, b Finding) int {
		if n := strings.Compare(a.Package, b.Package); n != 0 {
			return n
		}
		return strings.Compare(a.Problem, b.Problem)
	})
	return findings, nil
}

// CleanupOptions mirrors brew cleanup's arguments and install-time cleanup.
type CleanupOptions struct {
	// Formulae limits cleanup to these formulae's old kegs. gobrew's caches
	// are keyed by digest, not formula, so they are left to full cleanups.
	Formulae []string
	// Skip lists formulae never cleaned; pinned formulae are always skipped.
	Skip   []string
	DryRun bool
	// MaxAge prunes cache entries unused for longer; zero disables it.
	MaxAge time.Duration
	// All prunes every unleased cache entry (--prune=all, --scrub).
	All bool
}

func (e *Engine) Cleanup(ctx context.Context, opts CleanupOptions) ([]model.Removed, error) {
	if err := e.checkMutation(); err != nil {
		return nil, err
	}
	removed, err := e.installer().CleanupWith(ctx, installer.CleanupOptions{Formulae: opts.Formulae, Skip: opts.Skip, DryRun: opts.DryRun})
	if err != nil || len(opts.Formulae) > 0 {
		return removed, err
	}
	m, err := e.manager()
	if err != nil {
		return removed, err
	}
	caches, err := m.CleanupWith(ctx, artifact.CleanupOptions{DryRun: opts.DryRun, MaxAge: opts.MaxAge, All: opts.All})
	removed = append(removed, caches...)
	if err != nil {
		return removed, err
	}
	pruned, err := e.installer().PrunePrefix(ctx, opts.DryRun)
	return append(removed, pruned...), err
}

// PrunePrefix is brew cleanup --prune-prefix: it removes nothing else.
func (e *Engine) PrunePrefix(ctx context.Context, dryRun bool) ([]model.Removed, error) {
	if err := e.checkMutation(); err != nil {
		return nil, err
	}
	return e.installer().PrunePrefix(ctx, dryRun)
}

// PeriodicCleanupDue follows brew: a missing marker is created and not due;
// otherwise cleanup is due once the marker is older than period.
func (e *Engine) PeriodicCleanupDue(period time.Duration) (bool, error) {
	marker := filepath.Join(e.config.CacheDir, ".cleaned")
	info, err := os.Stat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return false, e.MarkCleaned()
	}
	if err != nil {
		return false, err
	}
	return time.Since(info.ModTime()) > period, nil
}

// MarkCleaned records a completed full cleanup.
func (e *Engine) MarkCleaned() error {
	if err := os.MkdirAll(e.config.CacheDir, 0700); err != nil {
		return err
	}
	marker := filepath.Join(e.config.CacheDir, ".cleaned")
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	now := time.Now()
	return errors.Join(f.Close(), os.Chtimes(marker, now, now))
}
