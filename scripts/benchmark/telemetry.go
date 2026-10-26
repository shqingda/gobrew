package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

type sample struct {
	Tool      string         `json:"tool"`
	Scenario  string         `json:"scenario"`
	Round     int            `json:"round"`
	WallMS    float64        `json:"wall_ms"`
	TimingsMS *model.Timings `json:"timings_ms,omitempty"`
	Disk      diskUsage      `json:"disk_logical_bytes"`
}

type diskUsage struct {
	PrefixTotal int64            `json:"prefix_total"`
	CacheTotal  int64            `json:"cache_total"`
	Prefix      map[string]int64 `json:"prefix"`
	Cache       map[string]int64 `json:"cache"`
}

func (r *report) sample(ctx context.Context, tool, scenario string, round int, wall time.Duration, prefix, cache string, output []byte) error {
	s := sample{Tool: tool, Scenario: scenario, Round: round, WallMS: float64(wall) / float64(time.Millisecond)}
	if tool == "gobrew" {
		timings, err := installTimings(output)
		if err != nil {
			return err
		}
		s.TimingsMS = &timings
	}
	var err error
	s.Disk, err = measureDisk(ctx, tool, prefix, cache)
	if err != nil {
		return fmt.Errorf("%s %s disk sample: %w", tool, scenario, err)
	}
	r.Samples = append(r.Samples, s)
	return nil
}

func installTimings(output []byte) (model.Timings, error) {
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Command       string          `json:"command"`
		Error         json.RawMessage `json:"error"`
		Data          struct {
			Timings     map[string]float64 `json:"timings_ms"`
			NeedsRepair []string           `json:"needs_repair"`
			NotExecuted []string           `json:"not_executed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return model.Timings{}, fmt.Errorf("install timing response: %w", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != "install" {
		return model.Timings{}, errors.New("install timing response has an unexpected schema or command")
	}
	if len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return model.Timings{}, fmt.Errorf("install response reported an error: %s", envelope.Error)
	}
	if len(envelope.Data.NeedsRepair) != 0 || len(envelope.Data.NotExecuted) != 0 {
		return model.Timings{}, errors.New("install response has incomplete packages")
	}
	for _, stage := range []string{"plan", "prepare", "apply"} {
		value, exists := envelope.Data.Timings[stage]
		if !exists || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return model.Timings{}, fmt.Errorf("install response is missing a valid %s timing; rebuild gobrew with stage timing support", stage)
		}
	}
	return model.Timings{Plan: envelope.Data.Timings["plan"], Prepare: envelope.Data.Timings["prepare"], Apply: envelope.Data.Timings["apply"]}, nil
}

// Each partition is disjoint. Total sizes include regular files only, counting
// logical file lengths regardless of APFS compression or cloned/shared blocks.
func measureDisk(ctx context.Context, tool, prefix, cache string) (diskUsage, error) {
	prefixPaths := map[string]string{"kegs": "Cellar", "configuration": "etc", "mutable_data": "var"}
	cachePaths := map[string]string{"raw": "downloads", "catalog": "api"}
	if tool == "gobrew" {
		prefixPaths["state"] = ".gobrew"
		cachePaths = map[string]string{"raw": "artifacts/blobs", "prepared": "artifacts/trees", "catalog": "catalog"}
	}
	usage := diskUsage{}
	var err error
	usage.PrefixTotal, usage.Prefix, err = partitions(ctx, prefix, prefixPaths)
	if err != nil {
		return usage, err
	}
	usage.CacheTotal, usage.Cache, err = partitions(ctx, cache, cachePaths)
	return usage, err
}

func partitions(ctx context.Context, root string, paths map[string]string) (int64, map[string]int64, error) {
	parts := make(map[string]int64, len(paths)+1)
	for name := range paths {
		parts[name] = 0
	}
	parts["other"] = 0
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && path == root {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		total += info.Size()
		for name, partPath := range paths {
			partPath = filepath.FromSlash(partPath)
			if relative == partPath || strings.HasPrefix(relative, partPath+string(filepath.Separator)) {
				parts[name] += info.Size()
				return nil
			}
		}
		parts["other"] += info.Size()
		return nil
	})
	return total, parts, err
}
