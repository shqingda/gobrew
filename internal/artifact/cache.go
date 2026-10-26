package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/platform"
)

// Cleanup evicts the oldest completed, unleased entries until both logical-byte
// budgets are met. Installed kegs are independent of these disposable caches.
func (m *Manager) Cleanup(ctx context.Context) ([]string, error) {
	var removed []string
	for _, tier := range []struct {
		name, lock string
		budget     int64
	}{{"blobs", "blob-", m.cfg.BlobBudget}, {"trees", "tree-", m.cfg.TreeBudget}} {
		if tier.budget < 0 {
			continue
		}
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
			if total <= tier.budget {
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
			err = os.RemoveAll(filepath.Join(dir, entry.key))
			closeErr := release()
			if err != nil || closeErr != nil {
				return removed, errors.Join(err, closeErr)
			}
			removed = append(removed, filepath.Join(dir, entry.key))
			total -= entry.size
		}
	}
	return removed, nil
}
