package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

func TestPrepareUsesOCIReceiptWhenModernBottleOmitsReceipt(t *testing.T) {
	bottle := bottleFixture(t, []tarEntry{{name: "demo/1.0_2/share/data", body: "payload"}})
	sha := sha256.Sum256(bottle)
	digest := hex.EncodeToString(sha[:])
	tab := `{"runtime_dependencies":[{"full_name":"homebrew/core/dep","version":"2.0","revision":1}]}`
	annotations := map[string]string{"sh.brew.bottle.digest": digest, "org.opencontainers.image.ref.name": "1.0_2.all.3", "sh.brew.tab": tab}
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "annotations": annotations, "layers": []ociDescriptor{{Digest: "sha256:" + digest}}})
	manifestHash := sha256.Sum256(manifest)
	manifestDigest := "sha256:" + hex.EncodeToString(manifestHash[:])
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []ociDescriptor{{Digest: manifestDigest, Annotations: annotations}}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/homebrew/core/demo/blobs/sha256:" + digest:
			_, _ = w.Write(bottle)
		case "/v2/homebrew/core/demo/manifests/1.0_2-3":
			_, _ = w.Write(index)
		case "/v2/homebrew/core/demo/manifests/" + manifestDigest:
			_, _ = w.Write(manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := model.Package{Formula: model.Formula{Name: "demo", Version: "1.0", Revision: 2, BottleRebuild: 3}, Bottle: model.Bottle{Tag: "all", SHA256: digest, URL: server.URL + "/v2/homebrew/core/demo/blobs/sha256:" + digest}}
	root := t.TempDir()
	host := model.Platform{OS: "darwin", Arch: "arm64", Major: 26}
	m := New(Config{CacheDir: filepath.Join(root, "cache"), Prefix: filepath.Join(root, "prefix"), Platform: host, HTTP: server.Client()})
	result, err := m.Prepare(context.Background(), model.Plan{ID: "oci", Prefix: m.cfg.Prefix, Platform: host, Packages: []model.Package{p}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release(result)
	deps := result.Packages[0].RuntimeDependencies
	if len(deps) != 1 || deps[0].Name != "dep" || deps[0].Version != "2.0" || deps[0].Revision != 1 {
		t.Fatalf("runtime facts lost: %#v", deps)
	}
}

func TestOCIReceiptRejectsUnboundOrMissingRuntimeFacts(t *testing.T) {
	for _, failure := range []string{"wrong bottle", "wrong platform", "wrong layer", "wrong manifest hash", "missing dependencies"} {
		t.Run(failure, func(t *testing.T) {
			digest := strings.Repeat("a", 64)
			tab := `{"runtime_dependencies":[]}`
			if failure == "missing dependencies" {
				tab = `{}`
			}
			annotations := map[string]string{"sh.brew.bottle.digest": digest, "org.opencontainers.image.ref.name": "1.all", "sh.brew.tab": tab}
			if failure == "wrong bottle" {
				annotations["sh.brew.bottle.digest"] = strings.Repeat("b", 64)
			}
			if failure == "wrong platform" {
				annotations["org.opencontainers.image.ref.name"] = "1.arm64_sequoia"
			}
			layer := "sha256:" + digest
			if failure == "wrong layer" {
				layer = "sha256:" + strings.Repeat("c", 64)
			}
			manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "annotations": annotations, "layers": []ociDescriptor{{Digest: layer}}})
			h := sha256.Sum256(manifest)
			manifestDigest := "sha256:" + hex.EncodeToString(h[:])
			if failure == "wrong manifest hash" {
				manifestDigest = "sha256:" + strings.Repeat("d", 64)
			}
			index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []ociDescriptor{{Digest: manifestDigest, Annotations: annotations}}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/manifests/1") {
					_, _ = w.Write(index)
				} else {
					_, _ = w.Write(manifest)
				}
			}))
			defer server.Close()
			p := model.Package{Formula: model.Formula{Name: "demo", Version: "1"}, Bottle: model.Bottle{Tag: "all", SHA256: digest, URL: server.URL + "/v2/homebrew/core/demo/blobs/sha256:" + digest}}
			m := New(Config{HTTP: server.Client()})
			if _, err := m.runtimeDependencies(context.Background(), t.TempDir(), p); err == nil {
				t.Fatal("unbound receipt accepted")
			}
		})
	}
}
