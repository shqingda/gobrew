// Package artifact prepares verified, immutable bottle trees independently of
// the installed prefix. Call Release after applying a successful Prepare result.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
	"golang.org/x/sync/errgroup"
)

const relocationVersion = 8

type Config struct {
	CacheDir, Prefix                        string
	Platform                                model.Platform
	HTTP                                    *http.Client
	DownloadConcurrency, PrepareConcurrency int
	BlobBudget, TreeBudget                  int64
}

type Manager struct {
	cfg    Config
	mu     sync.Mutex
	leases map[string][]func() error
}

func New(cfg Config) *Manager {
	if cfg.DownloadConcurrency <= 0 {
		cfg.DownloadConcurrency = 6
	}
	if cfg.PrepareConcurrency <= 0 {
		cfg.PrepareConcurrency = min(4, max(1, runtime.GOMAXPROCS(0)/2))
	}
	if cfg.BlobBudget == 0 {
		cfg.BlobBudget = 2 << 30
	}
	if cfg.TreeBudget == 0 {
		cfg.TreeBudget = 8 << 30
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 15 * time.Minute}
	}
	return &Manager{cfg: cfg, leases: make(map[string][]func() error)}
}

func (m *Manager) treeKey(p model.Package) string {
	identity := struct {
		Schema                                  int
		SHA, Name, Version, Prefix, Cellar, Tag string
		Rebuild                                 int
		Platform                                model.Platform
	}{
		relocationVersion, p.Bottle.SHA256, p.Formula.Name, p.Formula.PkgVersion(), filepath.Clean(m.cfg.Prefix), filepath.Join(m.cfg.Prefix, "Cellar"), p.Bottle.Tag, p.Formula.BottleRebuild, m.cfg.Platform,
	}
	b, _ := json.Marshal(identity)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Prepare holds all required tree leases in a globally stable order, avoiding
// deadlocks when two processes prepare overlapping dependency graphs.
func (m *Manager) Prepare(ctx context.Context, plan model.Plan, events chan<- model.Event) (prepared model.Prepared, err error) {
	prepared.Plan = plan
	if plan.Prefix != m.cfg.Prefix || plan.Platform != m.cfg.Platform {
		return prepared, fmt.Errorf("artifact configuration differs from plan")
	}
	if plan.ID == "" {
		return prepared, errors.New("plan ID is empty")
	}
	m.mu.Lock()
	if _, exists := m.leases[plan.ID]; exists {
		m.mu.Unlock()
		return prepared, model.ErrBusy
	}
	m.leases[plan.ID] = nil
	m.mu.Unlock()
	defer func() {
		if err != nil {
			_ = m.Release(prepared)
		}
	}()
	keys := make([]string, 0, len(plan.Packages))
	seen := map[string]bool{}
	for _, p := range plan.Packages {
		if err = validatePackage(p); err != nil {
			return prepared, err
		}
		key := m.treeKey(p)
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		var release func() error
		release, err = platform.AcquireLock(ctx, filepath.Join(m.cfg.CacheDir, "locks", "tree-"+key))
		if err != nil {
			return prepared, err
		}
		m.mu.Lock()
		m.leases[plan.ID] = append(m.leases[plan.ID], release)
		m.mu.Unlock()
	}
	prepared.Packages = make([]model.PreparedPackage, len(plan.Packages))
	downloads := make(chan struct{}, m.cfg.DownloadConcurrency)
	preparers := make(chan struct{}, m.cfg.PrepareConcurrency)
	group, workCtx := errgroup.WithContext(ctx)
	group.SetLimit(m.cfg.DownloadConcurrency + m.cfg.PrepareConcurrency)
	for i, p := range plan.Packages {
		group.Go(func() error {
			item, e := m.prepare(workCtx, p, downloads, preparers, events)
			if e != nil {
				return fmt.Errorf("prepare %s: %w", p.Formula.Name, e)
			}
			prepared.Packages[i] = item
			return nil
		})
	}
	if err = group.Wait(); err != nil {
		return prepared, err
	}
	// Best-effort cleanup must never invalidate artifacts protected by leases.
	_, _ = m.Cleanup(ctx)
	return prepared, nil
}

func (m *Manager) Release(prepared model.Prepared) error {
	// Coarse recency is enough for eviction; avoid a write on every cache hit.
	for _, item := range prepared.Packages {
		touchRecency(filepath.Join(m.cfg.CacheDir, "trees", m.treeKey(item.Package)))
	}
	m.mu.Lock()
	locks := m.leases[prepared.Plan.ID]
	delete(m.leases, prepared.Plan.ID)
	m.mu.Unlock()
	var err error
	for i := len(locks) - 1; i >= 0; i-- {
		err = errors.Join(err, locks[i]())
	}
	if len(locks) > 0 {
		_, cleanupErr := m.Cleanup(context.Background())
		err = errors.Join(err, cleanupErr)
	}
	return err
}

func touchRecency(path string) {
	if info, err := os.Lstat(path); err == nil && time.Since(info.ModTime()) > time.Hour {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	}
}

type treeManifest struct {
	Schema             int                       `json:"schema"`
	Key                string                    `json:"key"`
	Size               int64                     `json:"size"`
	Dependencies       []model.RuntimeDependency `json:"runtime_dependencies"`
	LinkedDependencies []string                  `json:"linked_dependencies"`
	LinkageChecked     bool                      `json:"linkage_checked"`
}

func (m *Manager) prepare(ctx context.Context, p model.Package, downloads, preparers chan struct{}, events chan<- model.Event) (model.PreparedPackage, error) {
	key := m.treeKey(p)
	entry := filepath.Join(m.cfg.CacheDir, "trees", key)
	result := model.PreparedPackage{Package: p, Tree: filepath.Join(entry, "tree")}
	if data, err := os.ReadFile(filepath.Join(entry, "manifest.json")); err == nil {
		var manifest treeManifest
		if json.Unmarshal(data, &manifest) == nil && manifest.Schema == relocationVersion && manifest.Key == key && manifest.LinkageChecked {
			if info, err := os.Lstat(result.Tree); err == nil && info.IsDir() {
				result.RuntimeDependencies = manifest.Dependencies
				result.LinkedDependencies = manifest.LinkedDependencies
				result.LinkageChecked = true
				result.CacheHit = true
				model.Emit(events, model.Event{Phase: "cache", Package: p.Formula.Name, Message: "prepared tree"})
				return result, nil
			}
		}
	}
	// Only an entry with the exact derived identity is ever replaced.
	if err := os.RemoveAll(entry); err != nil {
		return result, err
	}
	// Hold the blob lease across both stages so eviction cannot race the
	// handoff from a completed download to an extraction worker.
	unlock, err := platform.AcquireLock(ctx, filepath.Join(m.cfg.CacheDir, "locks", "blob-"+p.Bottle.SHA256))
	if err != nil {
		return result, err
	}
	defer unlock()
	select {
	case downloads <- struct{}{}:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	blob, err := m.downloadLocked(ctx, p, events)
	<-downloads
	if err != nil {
		return result, err
	}
	select {
	case preparers <- struct{}{}:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	defer func() { <-preparers }()
	if _, err := os.Stat(blob); err != nil {
		return result, fmt.Errorf("cached download disappeared; retry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(entry), ".prepare-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	model.Emit(events, model.Event{Phase: "extract", Package: p.Formula.Name})
	unpacked := filepath.Join(stage, "unpacked")
	if err := extractBottle(ctx, blob, unpacked, extractionOptions{SHA256: p.Bottle.SHA256, Prefix: m.cfg.Prefix}); err != nil {
		if errors.Is(err, errChecksum) {
			_ = os.Remove(blob)
		}
		return result, err
	}
	keg := filepath.Join(unpacked, p.Formula.Name, p.Formula.PkgVersion())
	if info, err := os.Lstat(keg); err != nil || !info.IsDir() {
		return result, fmt.Errorf("bottle does not contain %s/%s", p.Formula.Name, p.Formula.PkgVersion())
	}
	deps, err := m.runtimeDependencies(ctx, keg, p)
	if err != nil {
		return result, err
	}
	if err := os.Rename(keg, filepath.Join(stage, "tree")); err != nil {
		return result, err
	}
	if err := os.RemoveAll(unpacked); err != nil {
		return result, err
	}
	model.Emit(events, model.Event{Phase: "relocate", Package: p.Formula.Name})
	linked, err := relocateTreeChecked(ctx, filepath.Join(stage, "tree"), m.cfg.Prefix, p)
	if err != nil {
		return result, err
	}
	if err := platform.SyncTree(ctx, filepath.Join(stage, "tree")); err != nil {
		return result, fmt.Errorf("sync prepared tree: %w", err)
	}
	size, err := logicalSize(filepath.Join(stage, "tree"))
	if err != nil {
		return result, err
	}
	manifest := treeManifest{Schema: relocationVersion, Key: key, Size: size, Dependencies: deps, LinkedDependencies: linked, LinkageChecked: true}
	b, err := json.Marshal(manifest)
	if err != nil {
		return result, err
	}
	marker, err := os.OpenFile(filepath.Join(stage, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	_, writeErr := marker.Write(b)
	if err := errors.Join(writeErr, platform.SyncFile(marker), marker.Close()); err != nil {
		return result, err
	}
	if err := platform.SyncDir(stage); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := os.Rename(stage, entry); err != nil {
		return result, err
	}
	if err := platform.SyncDir(filepath.Dir(entry)); err != nil {
		return result, err
	}
	result.RuntimeDependencies = deps
	result.LinkedDependencies = linked
	result.LinkageChecked = true
	return result, nil
}

func validatePackage(p model.Package) error {
	validComponent := func(s string) bool { return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00") }
	if !validComponent(p.Formula.Name) || !validComponent(p.Formula.PkgVersion()) {
		return errors.New("invalid package name or version")
	}
	if len(p.Bottle.SHA256) != 64 {
		return errors.New("invalid bottle SHA256")
	}
	b, err := hex.DecodeString(p.Bottle.SHA256)
	if err != nil || len(b) != 32 || p.Bottle.SHA256 != strings.ToLower(p.Bottle.SHA256) {
		return errors.New("invalid bottle SHA256")
	}
	return nil
}

func readReceipt(keg string, p model.Package) ([]model.RuntimeDependency, error) {
	info, err := os.Lstat(filepath.Join(keg, "INSTALL_RECEIPT.json"))
	if err != nil {
		return nil, fmt.Errorf("read bottle receipt: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, errors.New("bottle receipt must be a bounded regular file")
	}
	b, err := os.ReadFile(filepath.Join(keg, "INSTALL_RECEIPT.json"))
	if err != nil {
		return nil, fmt.Errorf("read bottle receipt: %w", err)
	}
	return parseRuntimeDependencies(b)
}

func logicalSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}
