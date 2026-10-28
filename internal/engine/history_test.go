package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/catalog"
	"github.com/shqingda/gobrew/internal/model"
	bolt "go.etcd.io/bbolt"
)

type historyResponses map[string]string

func (responses historyResponses) RoundTrip(r *http.Request) (*http.Response, error) {
	body, ok := responses[r.URL.Host+r.URL.Path]
	if !ok {
		return nil, fmt.Errorf("unexpected history request: %s", r.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestHistoricalPlanLoadsDependenciesAbsentFromCurrentClosure(t *testing.T) {
	ctx := context.Background()
	host := model.Platform{OS: "darwin", Arch: "arm64", Major: 15}
	cache, prefix := t.TempDir(), t.TempDir()
	digest, commit, generation := strings.Repeat("a", 64), strings.Repeat("b", 40), strings.Repeat("c", 64)
	formulae := map[string]model.Formula{}
	for _, name := range []string{"demo", "oldlib", "leaf"} {
		formulae[name] = model.Formula{Name: name, Version: "2", Bottles: map[string]model.Bottle{"arm64_sequoia": {
			Tag: "arm64_sequoia", Cellar: ":any_skip_relocation", SHA256: digest,
			URL: "https://ghcr.io/v2/homebrew/core/" + name + "/blobs/sha256:" + digest,
		}}}
	}
	oldlib := formulae["oldlib"]
	oldlib.Dependencies = []string{"leaf"}
	formulae["oldlib"] = oldlib
	// Seed the normalized immutable index, as if a signed refresh had completed.
	// The current demo has no dependencies; the historical bottle needs oldlib.
	dir := filepath.Join(cache, "catalog")
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(filepath.Join(dir, "snapshots", generation+".db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket([]byte("meta"))
		if err != nil {
			return err
		}
		platform, _ := json.Marshal(host)
		for key, value := range map[string]string{"generation": generation, "schema": "1", "platform": string(platform)} {
			if err := meta.Put([]byte(key), []byte(value)); err != nil {
				return err
			}
		}
		entries, err := tx.CreateBucket([]byte("formulae"))
		if err != nil {
			return err
		}
		for name, f := range formulae {
			encoded, err := json.Marshal(f)
			if err != nil {
				return err
			}
			if err := entries.Put([]byte(name), encoded); err != nil {
				return err
			}
		}
		return nil
	})
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("seed catalog: %v, %v", err, closeErr)
	}
	if err := os.WriteFile(filepath.Join(dir, "CURRENT"), []byte(generation+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tab := `{"runtime_dependencies":[{"full_name":"oldlib","version":"1","declared_directly":true}]}`
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"annotations":   map[string]string{"org.opencontainers.image.created": "2026-10-22T03:25:16Z", "org.opencontainers.image.source": "https://github.com/homebrew/homebrew-core/blob/" + commit + "/Formula/d/demo.rb"},
		"manifests":     []any{map[string]any{"annotations": map[string]string{"org.opencontainers.image.ref.name": "1.arm64_sequoia", "sh.brew.bottle.digest": digest, "sh.brew.tab": tab}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	responses := historyResponses{
		"ghcr.io/token": `{"token":"fixture"}`,
		"ghcr.io/v2/homebrew/core/demo/tags/list":                                           `{"tags":["1"]}`,
		"ghcr.io/v2/homebrew/core/demo/manifests/1":                                         string(index),
		"api.github.com/repos/Homebrew/homebrew-core/commits":                               `[{"sha":"` + commit + `"}]`,
		"raw.githubusercontent.com/Homebrew/homebrew-core/" + commit + "/Formula/d/demo.rb": "class Demo < Formula\n  url \"https://example.test/demo/1.tar.gz\"\n  bottle do\n    sha256 cellar: :any_skip_relocation, arm64_sequoia: \"" + digest + "\"\n  end\nend\n",
	}
	e := New(Config{Prefix: prefix, CacheDir: cache, Platform: host, HTTP: &http.Client{Transport: responses}})
	plan, err := e.Plan(ctx, model.Request{Action: "install", Names: []string{"demo"}, Versions: map[string]string{"demo": "1"}})
	if err != nil {
		t.Fatalf("historical dependency exists in the signed index but could not be planned: %v", err)
	}
	if len(plan.Packages) != 3 || plan.Packages[0].Formula.Name != "leaf" || plan.Packages[1].Formula.Name != "oldlib" || plan.Packages[2].Formula.Version != "1" || !plan.Packages[2].Pin {
		t.Fatalf("historical dependency closure was not planned: %+v", plan.Packages)
	}
	current, err := (catalog.Client{Dir: dir, Platform: host}).LoadForPlan([]string{"demo"})
	if err != nil || current.Generation != generation || len(current.Formulae) != 1 || current.Formulae["demo"].Version != "2" {
		t.Fatalf("historical plan changed the signed index: %+v, %v", current, err)
	}
}
