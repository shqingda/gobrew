// Package engine is the application boundary shared by the CLI and future GUI.
package engine

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/shqingda/gobrew/internal/catalog"
	"github.com/shqingda/gobrew/internal/installer"
	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
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
	if missingCatalog(err) {
		return c.Update(ctx)
	}
	return s, err
}

func missingCatalog(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, model.ErrNotFound)
}

// EnsureCatalog downloads the catalog if there is none yet, reporting progress
// to events, and otherwise only checks that CURRENT exists. Commands that read
// the catalog call it first so a first download is never silent.
func (e *Engine) EnsureCatalog(ctx context.Context, events chan<- model.Event) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	if _, err := c.Fetched(); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	c.Events = events
	_, err = c.Update(ctx)
	return err
}

// Update refreshes the catalog, reporting progress to events (may be nil).
func (e *Engine) Update(ctx context.Context, events chan<- model.Event) (catalog.Snapshot, error) {
	c, err := e.client()
	if err != nil {
		return catalog.Snapshot{}, err
	}
	c.Events = events
	return c.Update(ctx)
}
func (e *Engine) Installed() (model.Snapshot, error) { return e.store().Load() }
