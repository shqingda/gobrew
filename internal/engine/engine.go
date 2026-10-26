// Package engine is the application boundary shared by the CLI and future GUI.
package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shqingda/gobrew/internal/artifact"
	"github.com/shqingda/gobrew/internal/catalog"
	"github.com/shqingda/gobrew/internal/installer"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"github.com/shqingda/gobrew/internal/postinstall"
	"github.com/shqingda/gobrew/internal/resolver"
	"github.com/shqingda/gobrew/internal/state"
)

type Config struct {
	Prefix     string
	CacheDir   string
	Platform   model.Platform
	HTTP       *http.Client
	Downloads  int
	Jobs       int
	BlobBudget int64
	TreeBudget int64
}

func Defaults() (Config, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return Config{}, err
	}
	return Config{Prefix: "/opt/gobrew", CacheDir: filepath.Join(cache, "gobrew"), Downloads: 8, Jobs: min(4, runtime.GOMAXPROCS(0)), BlobBudget: 2 << 30, TreeBudget: 8 << 30}, nil
}

type Engine struct {
	config   Config
	hostOnce sync.Once
	host     model.Platform
	hostErr  error
}

func New(c Config) *Engine       { return &Engine{config: c} }
func (e *Engine) Prefix() string { return e.config.Prefix }
func (e *Engine) Host() (model.Platform, error) {
	e.hostOnce.Do(func() {
		e.host = e.config.Platform
		if e.host.OS == "" {
			e.host, e.hostErr = platform.Detect()
		}
	})
	return e.host, e.hostErr
}
func (e *Engine) store() state.Store             { return state.Store{Prefix: e.config.Prefix} }
func (e *Engine) installer() installer.Installer { return installer.Installer{Prefix: e.config.Prefix} }
func (e *Engine) client() (*catalog.Client, error) {
	p, err := e.Host()
	if err != nil {
		return nil, err
	}
	return &catalog.Client{Dir: filepath.Join(e.config.CacheDir, "catalog"), HTTP: e.config.HTTP, Platform: p}, nil
}
func (e *Engine) Catalog(ctx context.Context) (catalog.Snapshot, error) {
	c, err := e.client()
	if err != nil {
		return catalog.Snapshot{}, err
	}
	s, err := c.Load()
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, model.ErrNotFound) {
		return c.Update(ctx)
	}
	return s, err
}
func (e *Engine) Update(ctx context.Context) (catalog.Snapshot, error) {
	c, err := e.client()
	if err != nil {
		return catalog.Snapshot{}, err
	}
	return c.Update(ctx)
}
func (e *Engine) Installed() (model.Snapshot, error) { return e.store().Load() }

func (e *Engine) Init() error {
	if _, err := e.Host(); err != nil {
		return err
	}
	prefix := filepath.Clean(e.config.Prefix)
	if !filepath.IsAbs(prefix) {
		return errors.New("prefix must be absolute")
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		if prefix != "/opt/gobrew" {
			return errors.New("root bootstrap is restricted to /opt/gobrew")
		}
		var err error
		uid, err = strconv.Atoi(os.Getenv("SUDO_UID"))
		if err != nil || uid <= 0 {
			return errors.New("bootstrap requires the invoking non-root SUDO_UID")
		}
		gid, err = strconv.Atoi(os.Getenv("SUDO_GID"))
		if err != nil || gid < 0 {
			return errors.New("bootstrap requires SUDO_GID")
		}
		if _, err = os.Lstat(prefix); !errors.Is(err, os.ErrNotExist) {
			return errors.New("root bootstrap only creates a missing prefix; it never changes existing ownership")
		}
		info, err := os.Lstat("/opt")
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe /opt directory")
		}
		if err = os.Mkdir(prefix, 0755); err != nil {
			return err
		}
		if err = os.Chown(prefix, uid, gid); err != nil {
			return err
		}
		// No network, package code or database runs in the privileged process.
		return nil
	}
	if info, err := os.Lstat(prefix); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("prefix must be a real directory")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != uid {
			return errors.New("prefix is owned by another user")
		}
		entries, err := os.ReadDir(prefix)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			if _, err = os.Stat(filepath.Join(prefix, ".gobrew", "state.db")); err != nil {
				return errors.New("refusing to adopt an existing directory; choose an empty prefix")
			}
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(prefix, 0755); err != nil {
			return fmt.Errorf("create prefix: %w; for the default location run sudo gobrew init, then gobrew init", err)
		}
	} else {
		return err
	}
	return e.store().Initialize()
}

func (e *Engine) checkMutation() error {
	if os.Geteuid() == 0 {
		return errors.New("package operations must not run as root")
	}
	if _, err := os.Stat(filepath.Join(e.config.Prefix, ".gobrew", "state.db")); err != nil {
		return fmt.Errorf("%w: run gobrew init first", model.ErrNotFound)
	}
	return nil
}

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
	// The common no-op path never opens the catalog or starts networking.
	if req.Action == "install" && len(req.Names) > 0 {
		all := true
		var promote, unchanged []string
		for _, requested := range req.Names {
			name := strings.TrimPrefix(requested, "homebrew/core/")
			r, ok := snap.Records[name]
			if !ok {
				alias := name
				for canonical, installed := range snap.Records {
					if slices.Contains(installed.Formula.Aliases, alias) {
						if ok {
							return model.Plan{}, fmt.Errorf("%w: ambiguous installed alias %s", model.ErrConflict, requested)
						}
						r, ok = installed, true
						name = canonical
					}
				}
			}
			if !ok {
				all = false
				break
			}
			if slices.Contains(unchanged, name) {
				continue
			}
			unchanged = append(unchanged, name)
			if !r.Requested {
				promote = append(promote, name)
			}
			if r.Status != model.Ready {
				return model.Plan{}, fmt.Errorf("%w: %s", model.ErrNeedsRepair, name)
			}
		}
		if all {
			plan := model.Plan{Request: req, Prefix: e.config.Prefix, StateGeneration: snap.Generation, Unchanged: unchanged, Promote: promote}
			if err := e.installer().ValidateState(model.Prepared{Plan: plan}, snap); err != nil {
				return model.Plan{}, err
			}
			return plan, nil
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
	cat, err := client.LoadForPlan(names)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, model.ErrNotFound) {
		cat, err = client.Update(ctx)
	}
	if err != nil {
		return model.Plan{}, err
	}
	plan, err := resolver.Build(req, cat.Generation, cat.Formulae, snap, host, e.config.Prefix)
	if err != nil {
		return plan, err
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
		return model.Result{Unchanged: plan.Unchanged}, nil
	}
	start = time.Now()
	p, err := e.Prepare(ctx, plan, events)
	timings.Prepare = float64(time.Since(start)) / float64(time.Millisecond)
	if err != nil {
		notExecuted := make([]string, 0, len(plan.Packages))
		for _, pkg := range plan.Packages {
			notExecuted = append(notExecuted, pkg.Formula.Name)
		}
		return model.Result{Unchanged: plan.Unchanged, NotExecuted: notExecuted}, err
	}
	defer func() { err = errors.Join(err, p.Close()) }()
	start = time.Now()
	result, err = e.Apply(ctx, p, events)
	timings.Apply = float64(time.Since(start)) / float64(time.Millisecond)
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

func (e *Engine) Pin(ctx context.Context, names []string, pinned bool) error {
	if err := e.checkMutation(); err != nil {
		return err
	}
	return e.installer().MutateRecords(ctx, func(s *model.Snapshot) error {
		for _, name := range names {
			r, ok := s.Records[name]
			if !ok {
				return fmt.Errorf("%w: %s", model.ErrNotFound, name)
			}
			r.Pinned = pinned
			s.Records[name] = r
		}
		return nil
	})
}
func (e *Engine) Link(ctx context.Context, names []string, linked, force bool) error {
	if err := e.checkMutation(); err != nil {
		return err
	}
	return e.installer().SetLinked(ctx, names, linked, force)
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
	err := e.installer().MutateRecords(ctx, func(s *model.Snapshot) error {
		removing := make(map[string]bool)
		for _, n := range names {
			if _, ok := s.Records[n]; !ok {
				return fmt.Errorf("%w: %s", model.ErrNotFound, n)
			}
			removing[n] = true
		}
		for name, r := range s.Records {
			if removing[name] {
				continue
			}
			for _, dep := range r.Dependencies {
				if removing[dep.Name] {
					return fmt.Errorf("%w: %s still requires %s", model.ErrConflict, name, dep.Name)
				}
			}
		}
		for _, n := range names {
			delete(s.Records, n)
		}
		return nil
	})
	return names, err
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

type Finding struct {
	Package string `json:"package,omitempty"`
	Problem string `json:"problem"`
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
			findings = append(findings, Finding{name, problem})
		}
		for rel, want := range r.Links {
			got, err := os.Readlink(filepath.Join(e.config.Prefix, rel))
			if err != nil || got != want {
				findings = append(findings, Finding{name, "missing or changed link " + rel})
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

func (e *Engine) Cleanup(ctx context.Context) ([]string, error) {
	if err := e.checkMutation(); err != nil {
		return nil, err
	}
	m, err := e.manager()
	if err != nil {
		return nil, err
	}
	kegs, err := e.installer().Cleanup(ctx)
	if err != nil {
		return kegs, err
	}
	caches, err := m.Cleanup(ctx)
	return append(kegs, caches...), err
}
