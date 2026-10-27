package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	"github.com/shqingda/gobrew/internal/platform"
)

// CleanupOptions adds brew cleanup's pruning to the budget eviction.
type CleanupOptions struct {
	DryRun bool
	// MaxAge also removes completed entries unused for longer; zero disables it.
	MaxAge time.Duration
	// All removes every completed, unleased entry (brew cleanup --prune=all).
	All bool
}

// Cleanup reclaims identifiable abandoned staging entries, then evicts the
// oldest completed, unleased entries until both logical-byte budgets are met.
// Installed kegs are independent of these disposable caches.
func (m *Manager) Cleanup(ctx context.Context) ([]string, error) {
	removed, err := m.CleanupWith(ctx, CleanupOptions{})
	paths := make([]string, len(removed))
	for i, r := range removed {
		paths[i] = r.Path
	}
	return paths, err
}

func (m *Manager) CleanupWith(ctx context.Context, opts CleanupOptions) ([]model.Removed, error) {
	var removed []model.Removed
	cutoff := time.Time{}
	if opts.MaxAge > 0 {
		cutoff = time.Now().Add(-opts.MaxAge)
	}
	for _, tier := range []struct {
		name, lock, temporary string
		budget                int64
	}{{"blobs", "blob-", ".download-", m.cfg.BlobBudget}, {"trees", "tree-", ".prepare-", m.cfg.TreeBudget}} {
		dir := filepath.Join(m.cfg.CacheDir, tier.name)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		type candidate struct {
			key  string
			size int64
			used time.Time
		}
		var candidates []candidate
		var total int64
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			if key, ok := temporaryKey(e.Name(), tier.temporary); ok {
				// The producer acquires this same lease before creating any
				// temporary entry. An unheld lease proves no operation can still
				// be writing or about to publish it, regardless of its age.
				if tier.name == "trees" && !e.IsDir() || tier.name == "blobs" && !e.Type().IsRegular() {
					continue
				}
				release, acquired, err := platform.TryLock(filepath.Join(m.cfg.CacheDir, "locks", tier.lock+key))
				if err != nil {
					return removed, err
				}
				if acquired {
					path := filepath.Join(dir, e.Name())
					size := int64(0)
					if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
						size = info.Size()
					}
					if !opts.DryRun {
						err = os.RemoveAll(path)
					}
					if err = errors.Join(err, release()); err != nil {
						return removed, err
					}
					removed = append(removed, model.Removed{Path: path, Bytes: size})
				}
				continue
			}
			if tier.budget < 0 && !opts.All && opts.MaxAge == 0 {
				continue // Unlimited completed cache still reclaims abandoned work.
			}
			if len(e.Name()) != 64 || strings.Trim(e.Name(), "0123456789abcdef") != "" {
				continue
			}
			info, err := e.Info()
			if err != nil {
				return removed, err
			}
			size := info.Size()
			if tier.name == "trees" {
				if !e.IsDir() {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, e.Name(), "manifest.json"))
				if err != nil {
					continue
				}
				var manifest treeManifest
				if json.Unmarshal(data, &manifest) != nil || manifest.Key != e.Name() {
					continue
				}
				size = manifest.Size
			} else if !info.Mode().IsRegular() {
				continue
			}
			candidates = append(candidates, candidate{e.Name(), size, info.ModTime()})
			total += size
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].used.Before(candidates[j].used) })
		for _, entry := range candidates {
			expired := opts.All || !cutoff.IsZero() && entry.used.Before(cutoff)
			// Candidates are oldest first, so no later entry is expired either.
			if !expired && (tier.budget < 0 || total <= tier.budget) {
				break
			}
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			release, acquired, err := platform.TryLock(filepath.Join(m.cfg.CacheDir, "locks", tier.lock+entry.key))
			if err != nil {
				return removed, err
			}
			if !acquired {
				continue
			}
			if !opts.DryRun {
				err = os.RemoveAll(filepath.Join(dir, entry.key))
			}
			closeErr := release()
			if err != nil || closeErr != nil {
				return removed, errors.Join(err, closeErr)
			}
			removed = append(removed, model.Removed{Path: filepath.Join(dir, entry.key), Bytes: entry.size})
			total -= entry.size
		}
	}
	return removed, nil
}

// os.CreateTemp/MkdirTemp append a decimal uint32 suffix in the pinned Go
// runtime. Only our exact keyed namespace is owned; old anonymous and unusual
// names are deliberately left alone because no lease can prove their status.
func temporaryKey(name, prefix string) (string, bool) {
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(name, prefix)
	if len(value) < 66 || value[64] != '-' {
		return "", false
	}
	key, suffix := value[:64], value[65:]
	if strings.Trim(key, "0123456789abcdef") != "" || len(suffix) > 10 || strings.Trim(suffix, "0123456789") != "" {
		return "", false
	}
	number, err := strconv.ParseUint(suffix, 10, 32)
	if err != nil || strconv.FormatUint(number, 10) != suffix {
		return "", false
	}
	return key, true
}
