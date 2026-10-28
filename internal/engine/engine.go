// Package engine is the application boundary shared by the CLI and future GUI.
package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

	// GitHubToken optionally raises GitHub API limits for explicit versions.
	GitHubToken string
}

func Defaults() (Config, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return Config{}, err
	}
	return Config{Prefix: SystemPrefix, CacheDir: filepath.Join(cache, "gobrew"), Downloads: 8, Jobs: min(4, runtime.GOMAXPROCS(0)), BlobBudget: 2 << 30, TreeBudget: 8 << 30}, nil
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

// SystemPrefix is the default Formula prefix and the only directory the
// privileged bootstrap may create.
const SystemPrefix = "/opt/gobrew"

// Initialized reports whether the prefix already has a state database. It is a
// single stat so the installed no-op path stays cheap.
func (e *Engine) Initialized() (bool, error) {
	_, err := os.Lstat(filepath.Join(e.config.Prefix, ".gobrew", "state.db"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Init creates or completes a user-owned prefix. It is idempotent and safe to
// run concurrently. When the parent directory is not writable it returns
// model.ErrPrivilegeRequired; obtaining privilege belongs to the caller. Run as
// root, it only performs the privileged half for SystemPrefix.
func (e *Engine) Init() error {
	if _, err := e.Host(); err != nil {
		return err
	}
	prefix := filepath.Clean(e.config.Prefix)
	if !filepath.IsAbs(prefix) {
		return errors.New("prefix must be absolute")
	}
	if os.Geteuid() == 0 {
		if prefix != SystemPrefix {
			return errors.New("root bootstrap is restricted to " + SystemPrefix)
		}
		uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
		if err != nil || uid <= 0 {
			return errors.New("bootstrap requires the invoking non-root SUDO_UID")
		}
		gid, err := strconv.Atoi(os.Getenv("SUDO_GID"))
		if err != nil || gid < 0 {
			return errors.New("bootstrap requires SUDO_GID")
		}
		// No network, package code or database runs in the privileged process.
		return createOwnedPrefix(prefix, uid, gid)
	}
	// Mkdir first: it reports an existing path before checking write access,
	// and makes concurrent initializers agree on a single creator.
	err := os.Mkdir(prefix, 0755)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrExist):
		if err = e.checkOwnedPrefix(prefix, os.Getuid()); err != nil {
			return err
		}
	case errors.Is(err, os.ErrPermission), prefix == SystemPrefix && errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: cannot create %s: %v", model.ErrPrivilegeRequired, prefix, err)
	default:
		return fmt.Errorf("create prefix: %w", err)
	}
	return e.store().Initialize()
}

func (e *Engine) checkOwnedPrefix(prefix string, uid int) error {
	info, err := os.Lstat(prefix)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prefix must be a real directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != uid {
		return fmt.Errorf("%s is owned by another user; gobrew never takes over existing directories", prefix)
	}
	if ok, err := e.Initialized(); err != nil || ok {
		return err
	}
	// A crash or a concurrent Init can leave the layout without a database.
	return e.store().Pristine()
}

// createOwnedPrefix is the privileged half of Init. It creates a missing parent
// (such as /opt on a fresh system, left owned by root) and the missing prefix,
// then hands only that new prefix to the invoking user. An existing prefix is
// accepted only if that user already owns it; nothing is ever chowned twice or
// adopted, and a prefix whose handover failed is removed again.
func createOwnedPrefix(prefix string, uid, gid int) error {
	parent := filepath.Dir(prefix)
	if err := os.Mkdir(parent, 0755); err == nil {
		if err = platform.SyncDir(filepath.Dir(parent)); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe %s directory", parent)
	}
	if err = os.Mkdir(prefix, 0755); errors.Is(err, os.ErrExist) {
		info, err = os.Lstat(prefix)
		if err != nil {
			return err
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && int(stat.Uid) == uid {
			return nil
		}
		return errors.New("root bootstrap only creates a missing prefix; it never changes existing ownership")
	} else if err != nil {
		return err
	}
	if err = os.Chown(prefix, uid, gid); err != nil {
		return errors.Join(err, os.Remove(prefix))
	}
	return platform.SyncDir(parent)
}

func (e *Engine) checkMutation() error {
	if os.Geteuid() == 0 {
		return errors.New("package operations must not run as root")
	}
	if ok, err := e.Initialized(); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: nothing is installed in %s", model.ErrNotFound, e.config.Prefix)
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
	// The common no-op path never starts networking: it reads only the
	// requested formulae from the local catalog to tell up-to-date from outdated.
	if req.Action == "install" && len(req.Names) > 0 {
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
		plan.Packages[i].Pin = historic[plan.Packages[i].Formula.Name]
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
		if !r.Requested {
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
		if pkg.Pin && slices.Contains(result.Succeeded, pkg.Formula.Name) {
			result.Pinned = append(result.Pinned, pkg.Formula.Name)
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
	removed := slices.Clone(names)
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
	return append(removed, caches...), err
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
